package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestInboxAPI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.workspace("A")
	e.user("admin@a.com", ws, store.MemberRoleAdmin)
	e.user("viewer@a.com", ws, store.MemberRoleViewer)
	q := e.q
	var minScore pgtype.Numeric
	_ = minScore.Scan("0.5")
	for _, l := range []store.Lane{store.LaneInbound, store.LaneMedia, store.LaneThoughtLeadership} {
		if err := q.InsertLaneConfigIfMissing(ctx, store.InsertLaneConfigIfMissingParams{WorkspaceID: ws, Lane: l, Weights: []byte(`{"intent":1}`), DailyQuota: 3, FreshnessHours: 48, MinScore: minScore, DraftKind: "reply"}); err != nil {
			t.Fatal(err)
		}
	}
	src, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws, Connector: "rss", Name: "Feed", Config: []byte(`{"feed_url":"https://x"}`), Enabled: true})
	mkOpp := func(ext string, lane store.Lane, score string) uuid.UUID {
		h := sha256.Sum256([]byte(ext))
		title := "Item " + ext
		now := time.Now()
		rid, err := q.InsertRawItem(ctx, store.InsertRawItemParams{WorkspaceID: ws, SourceID: src.ID, ExternalID: ext, Url: "https://e.com/" + ext, Title: &title,
			Body: "body text long enough", Metrics: []byte(`{}`), ContentHash: h[:], PublishedAt: &now})
		if err != nil {
			t.Fatal(err)
		}
		var n pgtype.Numeric
		_ = n.Scan(score)
		id, err := q.InsertOpportunity(ctx, store.InsertOpportunityParams{WorkspaceID: ws, RawItemID: rid, Lane: lane, OppType: "pain_point", Score: n,
			ScoreBreakdown: []byte(`{"factors":{"intent":0.9}}`), Explanation: "Because."})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := mkOpp("a", store.LaneInbound, "0.910")
	mkOpp("b", store.LaneInbound, "0.600")
	mkOpp("c", store.LaneMedia, "0.700")

	admin := e.client()
	admin.login("admin@a.com")
	viewer := e.client()
	viewer.login("viewer@a.com")

	code, body := viewer.do(http.MethodGet, "/opportunities?lane=inbound", nil)
	var list struct {
		Items []struct {
			ID    string  `json:"id"`
			Score float64 `json:"score"`
		} `json:"items"`
		Counts map[string]struct{ New int } `json:"counts"`
	}
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Items) != 2 || list.Items[0].Score != 0.91 || list.Counts["inbound"].New != 2 || list.Counts["media"].New != 1 {
		t.Fatalf("list = %d %s", code, body)
	}
	if code, _ := viewer.do(http.MethodGet, "/opportunities?min_score=0.8", nil); code != http.StatusOK {
		t.Fatal("min_score filter")
	}

	// Viewers cannot change status; dismissal needs a reason.
	if code, _ := viewer.do(http.MethodPost, "/opportunities/"+a.String()+"/status", map[string]any{"status": "saved"}); code != http.StatusForbidden {
		t.Fatalf("viewer status = %d", code)
	}
	if code, _ := admin.do(http.MethodPost, "/opportunities/"+a.String()+"/status", map[string]any{"status": "dismissed"}); code != http.StatusBadRequest {
		t.Fatal("dismiss without reason must be rejected")
	}
	if code, _ := admin.do(http.MethodPost, "/opportunities/"+a.String()+"/status", map[string]any{"status": "dismissed", "reason": "because"}); code != http.StatusBadRequest {
		t.Fatal("unknown reason must be rejected")
	}
	if code, body := admin.do(http.MethodPost, "/opportunities/"+a.String()+"/status", map[string]any{"status": "dismissed", "reason": "low_intent"}); code != http.StatusNoContent {
		t.Fatalf("dismiss = %d %s", code, body)
	}
	if code, _ := admin.do(http.MethodPost, "/opportunities/"+a.String()+"/status", map[string]any{"status": "snoozed", "snoozed_until": time.Now().Add(-time.Hour)}); code != http.StatusBadRequest {
		t.Fatal("snooze in the past must be rejected")
	}
	if code, _ := viewer.do(http.MethodPost, "/opportunities/"+a.String()+"/events", map[string]any{"event": "opened_link"}); code != http.StatusNoContent {
		t.Fatal("viewers may log events")
	}
	if code, _ := viewer.do(http.MethodPost, "/opportunities/"+a.String()+"/events", map[string]any{"event": "outcome", "outcome": "rich"}); code != http.StatusBadRequest {
		t.Fatal("bad outcome must be rejected")
	}
	code, body = viewer.do(http.MethodGet, "/opportunities/"+a.String(), nil)
	var detail struct {
		Status string `json:"status"`
		Events []struct {
			Event  string  `json:"event"`
			Reason *string `json:"reason"`
		} `json:"events"`
		Item struct{ Title string } `json:"item"`
	}
	_ = json.Unmarshal(body, &detail)
	if code != http.StatusOK || detail.Status != "dismissed" || len(detail.Events) != 2 || detail.Events[0].Event != "dismissed" || *detail.Events[0].Reason != "low_intent" {
		t.Fatalf("detail = %d %s", code, body)
	}

	// Lane settings validation.
	if code, _ := admin.do(http.MethodPut, "/settings/lanes/inbound", map[string]any{"weights": map[string]float64{"vibes": 1}, "daily_quota": 5, "freshness_hours": 48, "min_score": 0.5, "draft_kind": "reply"}); code != http.StatusBadRequest {
		t.Fatal("unknown weight must be rejected")
	}
	if code, body := admin.do(http.MethodPut, "/settings/lanes/inbound", map[string]any{"weights": map[string]float64{"intent": 0.5, "fit": 0.5}, "daily_quota": 7, "freshness_hours": 72, "min_score": 0.6, "draft_kind": "reply"}); code != http.StatusNoContent {
		t.Fatalf("save lane = %d %s", code, body)
	}

	// Process pending enqueues only unprocessed items.
	code, body = admin.do(http.MethodPost, "/items/process", nil)
	var pr struct{ Enqueued int }
	_ = json.Unmarshal(body, &pr)
	if code != http.StatusAccepted || pr.Enqueued != 3 {
		t.Fatalf("process = %d %s", code, body)
	}

	// Prompt edits must render.
	if code, body := admin.do(http.MethodPost, "/settings/prompts", map[string]any{"task": "classify", "body": "Hello {{.Nope}}"}); code != http.StatusBadRequest {
		t.Fatalf("broken template = %d %s", code, body)
	}
	if code, body := admin.do(http.MethodPost, "/settings/prompts", map[string]any{"task": "classify", "body": "About {{.BrandName}}: {{.Brand}}"}); code != http.StatusCreated {
		t.Fatalf("valid template = %d %s", code, body)
	}
}
