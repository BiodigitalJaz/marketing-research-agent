package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestResearchAPI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.workspace("A")
	other := e.workspace("B")
	e.user("admin@a.com", ws, store.MemberRoleAdmin)
	e.user("viewer@a.com", ws, store.MemberRoleViewer)
	e.user("admin@b.com", other, store.MemberRoleAdmin)
	admin, viewer, outsider := e.client(), e.client(), e.client()
	admin.login("admin@a.com")
	viewer.login("viewer@a.com")
	outsider.login("admin@b.com")

	if code, _ := viewer.do(http.MethodPost, "/research", map[string]any{"mode": "chat", "question": "q"}); code != http.StatusForbidden {
		t.Fatalf("viewer create = %d", code)
	}
	for _, bad := range []map[string]any{{"mode": "think", "question": "q"}, {"mode": "deep", "question": ""}, {"mode": "deep", "question": "q", "template": "novel"}} {
		if code, _ := admin.do(http.MethodPost, "/research", bad); code != http.StatusBadRequest {
			t.Fatalf("create %v = %d", bad, code)
		}
	}
	code, body := admin.do(http.MethodPost, "/research", map[string]any{"mode": "chat", "question": "What do users fear?", "use_web": false})
	var job struct {
		ID     uuid.UUID `json:"id"`
		Status string    `json:"status"`
		UseWeb bool      `json:"use_web"`
	}
	_ = json.Unmarshal(body, &job)
	if code != http.StatusCreated || job.Status != "queued" || job.UseWeb {
		t.Fatalf("create = %d %s", code, body)
	}
	var riverJobs int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'research' AND queue = 'research'").Scan(&riverJobs)
	if riverJobs != 1 {
		t.Fatalf("river jobs = %d, want the job enqueued with the row", riverJobs)
	}
	path := "/research/" + job.ID.String()
	if code, _ := outsider.do(http.MethodGet, path, nil); code != http.StatusNotFound {
		t.Fatalf("other workspace = %d", code)
	}
	code, body = viewer.do(http.MethodGet, path, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"role":"user"`) {
		t.Fatalf("get = %d %s", code, body)
	}
	if code, body := viewer.do(http.MethodGet, "/research", nil); code != http.StatusOK || !strings.Contains(string(body), job.ID.String()) || !strings.Contains(string(body), "pain_points") {
		t.Fatalf("list = %d %s", code, body)
	}
	// Follow-ups need a finished conversation.
	if code, _ := admin.do(http.MethodPost, path+"/messages", map[string]any{"content": "more"}); code != http.StatusConflict {
		t.Fatalf("follow-up while queued = %d", code)
	}

	// Steps stream: replay, resume with Last-Event-ID, live notifications.
	step := func(kind string) {
		t.Helper()
		st, err := q(e).InsertResearchStep(ctx, store.InsertResearchStepParams{WorkspaceID: ws, JobID: job.ID, Kind: kind, Payload: []byte(`{"n":1}`)})
		if err != nil {
			t.Fatal(err)
		}
		_ = q(e).NotifyResearch(ctx, fmt.Sprintf("%s:%d", job.ID, st.Seq))
	}
	step("plan")
	step("tool_call")
	events := make(chan string, 10)
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, e.srv.URL+"/api/v1"+path+"/stream?workspace="+ws.String(), nil)
		req.Header.Set("Last-Event-ID", "1")
		resp, err := viewer.http.Do(req)
		if err != nil {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		var id string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				var s struct{ Kind string }
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &s)
				events <- id + ":" + s.Kind
			}
		}
		close(events)
	}()
	next := func() string {
		t.Helper()
		select {
		case ev, ok := <-events:
			if !ok {
				return "<closed>"
			}
			return ev
		case <-time.After(10 * time.Second):
			t.Fatal("no event")
			return ""
		}
	}
	if ev := next(); ev != "2:tool_call" {
		t.Fatalf("resume must replay only steps after Last-Event-ID, got %s", ev)
	}
	step("tool_result")
	if ev := next(); ev != "3:tool_result" {
		t.Fatalf("live step = %s", ev)
	}
	step("done")
	if ev := next(); ev != "4:done" {
		t.Fatalf("done = %s", ev)
	}
	if ev := next(); ev != "<closed>" {
		t.Fatalf("the stream must close after done, got %s", ev)
	}

	// Cancel.
	if code, _ := viewer.do(http.MethodPost, path+"/cancel", nil); code != http.StatusForbidden {
		t.Fatal("viewers cannot cancel")
	}
	code, body = admin.do(http.MethodPost, path+"/cancel", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"status":"cancelled"`) {
		t.Fatalf("cancel = %d %s", code, body)
	}
	var riverState string
	_ = e.pool.QueryRow(ctx, "SELECT state FROM river_job WHERE kind = 'research'").Scan(&riverState)
	if riverState != "cancelled" {
		t.Errorf("river job state = %s", riverState)
	}
	if code, _ := admin.do(http.MethodPost, path+"/cancel", nil); code != http.StatusConflict {
		t.Fatal("cancelling twice must conflict")
	}
	// A finished chat takes a follow-up and is queued again.
	code, body = admin.do(http.MethodPost, path+"/messages", map[string]any{"content": "and on HN?"})
	if code != http.StatusAccepted || !strings.Contains(string(body), `"status":"queued"`) {
		t.Fatalf("follow-up = %d %s", code, body)
	}
}

func q(e *env) *store.Queries { return store.New(e.pool) }

func TestReportsAPI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.workspace("A")
	other := e.workspace("B")
	e.user("viewer@a.com", ws, store.MemberRoleViewer)
	e.user("viewer@b.com", other, store.MemberRoleViewer)
	viewer, outsider := e.client(), e.client()
	viewer.login("viewer@a.com")
	outsider.login("viewer@b.com")

	charts := `[{"id":"c1","type":"bar","title":"By lane","x":"lane","y":["opportunities"],"data":[{"lane":"inbound","opportunities":3}],"dataset":"d1"}]`
	tpl := "market_scan"
	rep, err := q(e).InsertReport(ctx, store.InsertReportParams{WorkspaceID: ws, Title: "AI agents: market scan", Markdown: "# AI agents\n\nPeople worry [1].\n\n{{chart:c1}}\n", Charts: []byte(charts), Template: &tpl})
	if err != nil {
		t.Fatal(err)
	}
	title := "A worried user"
	if err := q(e).InsertCitation(ctx, store.InsertCitationParams{WorkspaceID: ws, ReportID: rep.ID, Marker: "[1]", Url: "https://example.com/q", Title: &title}); err != nil {
		t.Fatal(err)
	}
	code, body := viewer.do(http.MethodGet, "/reports", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"citation_count":1`) {
		t.Fatalf("list = %d %s", code, body)
	}
	if code, _ := outsider.do(http.MethodGet, "/reports/"+rep.ID.String(), nil); code != http.StatusNotFound {
		t.Fatalf("other workspace = %d", code)
	}
	code, body = viewer.do(http.MethodGet, "/reports/"+rep.ID.String(), nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"marker":"[1]"`) || !strings.Contains(string(body), `"dataset":"d1"`) {
		t.Fatalf("get = %d %s", code, body)
	}
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/reports/"+rep.ID.String()+"/export?format=md", nil)
	resp, err := viewer.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
	}
	md := b.String()
	if !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="ai-agents-market-scan.md"`) {
		t.Errorf("disposition = %q", resp.Header.Get("Content-Disposition"))
	}
	for _, want := range []string{"People worry [1].", "| lane | opportunities |", "| inbound | 3 |", "## Sources", "[1] A worried user — https://example.com/q", "AI-generated"} {
		if !strings.Contains(md, want) {
			t.Errorf("export lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "{{chart") {
		t.Error("chart references must be rendered as tables in the export")
	}
	if code, _ := viewer.do(http.MethodGet, "/reports/"+rep.ID.String()+"/export?format=pdf", nil); code != http.StatusBadRequest {
		t.Error("only md export is supported")
	}
}
