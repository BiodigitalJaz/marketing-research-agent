package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestDraftsAPI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	q := e.q
	mk := func(name string) (uuid.UUID, uuid.UUID) {
		ws := e.workspace(name)
		var minScore pgtype.Numeric
		_ = minScore.Scan("0.5")
		if err := q.InsertLaneConfigIfMissing(ctx, store.InsertLaneConfigIfMissingParams{WorkspaceID: ws, Lane: store.LaneInbound, Weights: []byte(`{"intent":1}`),
			DailyQuota: 3, FreshnessHours: 48, MinScore: minScore, DraftKind: "reply"}); err != nil {
			t.Fatal(err)
		}
		src, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws, Connector: "rss", Name: "Feed", Config: []byte(`{"feed_url":"https://x"}`), Enabled: true})
		h := sha256.Sum256([]byte(name))
		title, now := "Item", time.Now()
		rid, err := q.InsertRawItem(ctx, store.InsertRawItemParams{WorkspaceID: ws, SourceID: src.ID, ExternalID: name, Url: "https://e.com/" + name, Title: &title,
			Body: "body text long enough", Metrics: []byte(`{}`), ContentHash: h[:], PublishedAt: &now})
		if err != nil {
			t.Fatal(err)
		}
		var n pgtype.Numeric
		_ = n.Scan("0.8")
		opp, err := q.InsertOpportunity(ctx, store.InsertOpportunityParams{WorkspaceID: ws, RawItemID: rid, Lane: store.LaneInbound, OppType: "pain_point", Score: n,
			ScoreBreakdown: []byte(`{}`), Explanation: "Because."})
		if err != nil {
			t.Fatal(err)
		}
		return ws, opp
	}
	ws, opp := mk("A")
	_, otherOpp := mk("B")
	e.user("admin@a.com", ws, store.MemberRoleAdmin)
	e.user("viewer@a.com", ws, store.MemberRoleViewer)
	admin, viewer := e.client(), e.client()
	admin.login("admin@a.com")
	viewer.login("viewer@a.com")
	path := "/opportunities/" + opp.String()

	if code, _ := viewer.do(http.MethodPost, path+"/drafts", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("viewer request = %d", code)
	}
	if code, _ := admin.do(http.MethodPost, path+"/drafts", map[string]any{"kind": "tweet"}); code != http.StatusBadRequest {
		t.Fatal("unknown kind must be rejected")
	}
	if code, _ := admin.do(http.MethodPost, path+"/drafts", map[string]any{"instruction": strings.Repeat("x", 1001)}); code != http.StatusBadRequest {
		t.Fatal("long instruction must be rejected")
	}
	if code, _ := admin.do(http.MethodPost, "/opportunities/"+otherOpp.String()+"/drafts", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("other workspace's opportunity = %d", code)
	}
	queued := func(body map[string]any) bool {
		t.Helper()
		code, b := admin.do(http.MethodPost, path+"/drafts", body)
		var r struct{ Queued bool }
		_ = json.Unmarshal(b, &r)
		if code != http.StatusAccepted {
			t.Fatalf("request drafts = %d %s", code, b)
		}
		return r.Queued
	}
	if !queued(map[string]any{}) || queued(map[string]any{}) {
		t.Fatal("first request queues; an identical pending request does not")
	}
	if !queued(map[string]any{"instruction": "Shorter"}) {
		t.Fatal("a different instruction is a new request")
	}

	type detail struct {
		DraftKind string `json:"draft_kind"`
		Drafting  struct {
			State string `json:"state"`
		} `json:"drafting"`
		Drafts []struct {
			ID        string            `json:"id"`
			Variant   string            `json:"variant"`
			WordLimit int               `json:"word_limit"`
			RuleFlags []json.RawMessage `json:"rule_flags"`
			Portfolio []json.RawMessage `json:"portfolio_items"`
		} `json:"drafts"`
	}
	get := func() detail {
		t.Helper()
		code, b := viewer.do(http.MethodGet, path, nil)
		var d detail
		if err := json.Unmarshal(b, &d); err != nil || code != http.StatusOK {
			t.Fatalf("detail = %d %s", code, b)
		}
		return d
	}
	if d := get(); d.DraftKind != "reply" || d.Drafting.State != "pending" || d.Drafts == nil || len(d.Drafts) != 0 {
		t.Fatalf("pending detail = %+v", d)
	}

	draft, err := q.InsertDraft(ctx, store.InsertDraftParams{WorkspaceID: ws, OpportunityID: opp, BatchID: uuid.New(), Kind: "reply", Variant: "short",
		Body: "Approve each order.", PortfolioItemIds: []uuid.UUID{}, RuleFlags: []byte(`[]`), WordCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if d := get(); len(d.Drafts) != 1 || d.Drafts[0].Variant != "short" || d.Drafts[0].WordLimit != 70 || d.Drafts[0].RuleFlags == nil || d.Drafts[0].Portfolio == nil {
		t.Fatalf("drafts = %+v", d.Drafts)
	}

	// copied_draft must name one of this opportunity's drafts.
	for _, meta := range []map[string]any{nil, {"draft_id": uuid.NewString()}, {"draft_id": "nope"}} {
		if code, _ := viewer.do(http.MethodPost, path+"/events", map[string]any{"event": "copied_draft", "meta": meta}); code != http.StatusBadRequest {
			t.Fatalf("copied_draft with meta %v = %d", meta, code)
		}
	}
	if code, b := viewer.do(http.MethodPost, path+"/events", map[string]any{"event": "copied_draft", "meta": map[string]any{"draft_id": draft.ID}}); code != http.StatusNoContent {
		t.Fatalf("copied_draft = %d %s", code, b)
	}
	if code, b := admin.do(http.MethodGet, "/settings/prompts?task=draft_check", nil); code != http.StatusOK {
		t.Fatalf("draft_check prompts = %d %s", code, b)
	}
}
