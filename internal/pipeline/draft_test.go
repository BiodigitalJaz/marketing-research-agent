package pipeline_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

const portfolioURL = "https://benchwright.dev/blog/containment"

// opportunity processes one Forum question and returns its opportunity.
func (e *env) opportunity(body string) uuid.UUID {
	e.t.Helper()
	raw := e.item(e.rss, uuid.NewString(), "Is an AI planning agent safe?", body, time.Now().Add(-time.Hour), "Forum")
	if _, err := e.svc.Process(e.ctx, e.ws, raw); err != nil {
		e.t.Fatal(err)
	}
	var id uuid.UUID
	if err := e.svc.Pool.QueryRow(e.ctx, "SELECT id FROM opportunities WHERE raw_item_id = $1", raw).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) rules(json string) {
	e.t.Helper()
	if _, err := e.q.UpdateSource(e.ctx, store.UpdateSourceParams{WorkspaceID: e.ws, ID: e.rss, Name: "Forum",
		Config: []byte(`{"feed_url":"https://x","rules":` + json + `}`), Enabled: true}); err != nil {
		e.t.Fatal(err)
	}
}

// portfolio creates a portfolio item; with twin, its embedding matches items
// whose text contains "twin" exactly.
func (e *env) portfolio(title string, url *string, twin bool) uuid.UUID {
	e.t.Helper()
	p, err := e.q.CreatePortfolioItem(e.ctx, store.CreatePortfolioItemParams{WorkspaceID: e.ws, Kind: "blog_post", Title: title, Url: url, Summary: title + " summary", Tags: []byte(`{}`)})
	if err != nil {
		e.t.Fatal(err)
	}
	text := title
	if twin {
		text = "twin"
	}
	vecs, _ := fakeEmbedder{}.Embed(e.ctx, []string{text})
	if err := e.q.UpsertEmbedding(e.ctx, store.UpsertEmbeddingParams{EntityType: "portfolio_item", EntityID: p.ID, Model: "fake-embed", Dims: 32,
		Embedding: pgvector.NewVector(vecs[0]), WorkspaceID: e.ws}); err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

func draftsJSON(drafts ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"drafts": drafts})
	return string(b)
}

func reply(variant, body string, link bool, ids ...string) map[string]any {
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{"variant": variant, "subject": nil, "body": body, "portfolio_item_ids": ids, "include_link": link}
}

func flagRules(d store.Draft) []string {
	var flags []pipeline.RuleFlag
	_ = json.Unmarshal(d.RuleFlags, &flags)
	var out []string
	for _, f := range flags {
		out = append(out, f.Rule+":"+f.Severity)
	}
	return out
}

func TestDraftReply(t *testing.T) {
	e := setup(t)
	e.rules(`{"notes":["Answer the question first"]}`)
	url := portfolioURL
	other := e.portfolio("Costs explainer", nil, false)
	match := e.portfolio("Containment write-up", &url, true)
	opp := e.opportunity("twin: is it safe to let an AI agent trade my Trello account? I want to approve each order.")

	var first, revision string
	e.fr.draft = func(req llm.ChatRequest) string {
		if !strings.Contains(req.System, "three variants") || !strings.Contains(req.System, "include_link: true only when") {
			t.Errorf("reply prompt not rendered for reply kind with links allowed:\n%s", req.System)
		}
		if len(req.Messages) == 1 {
			first = req.Messages[0].Parts[0].Text
			return draftsJSON( // out of order, a duplicate variant, short missing, a banned phrase
				reply("story", "Profits are guaranteed if you wait.", false),
				reply("helpful", "Use approval mode. I wrote up how the risk engine refuses orders: "+portfolioURL, true, match.String(), uuid.NewString()),
				reply("helpful", "duplicate variant is dropped", false),
			)
		}
		revision = req.Messages[2].Parts[0].Text
		return draftsJSON(
			reply("helpful", "Use approval mode. I wrote up how the risk engine refuses orders: "+portfolioURL, true, match.String()),
			reply("story", "I let an agent propose trades for a month and approved each one.", false),
			reply("short", "Approve each order yourself.", false),
		)
	}
	e.fr.check = func(msg string) string {
		if !strings.Contains(msg, "<external_content") || !strings.Contains(msg, `Variant "short"`) {
			t.Errorf("check must see the wrapped drafts: %s", msg)
		}
		return `{"flags":[
			{"variant":"short","rule":"Answer the question first","quote":"Approve each order yourself","severity":"warning","note":"Too terse to answer the question."},
			{"variant":"story","rule":"Answer the question first","quote":"Approve each order yourself","severity":"block","note":"Copied from another variant."},
			{"variant":"helpful","rule":"Answer the question first","quote":"words that are not there","severity":"block","note":"Hallucinated."}]}`
	}

	drafts, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp, Instruction: "Mention approval mode"})
	if err != nil {
		t.Fatal(err)
	}
	var variants []string
	for _, d := range drafts {
		variants = append(variants, d.Variant)
	}
	if !slices.Equal(variants, []string{"helpful", "story", "short"}) {
		t.Fatalf("variants = %q, want helpful, story, short", variants)
	}
	if !strings.Contains(revision, "guaranteed") {
		t.Errorf("banned phrase must trigger one revision naming it, got %q", revision)
	}
	helpful, story, short := drafts[0], drafts[1], drafts[2]
	if !helpful.IncludeLink || !slices.Equal(helpful.PortfolioItemIds, []uuid.UUID{match}) || !strings.Contains(helpful.Body, portfolioURL) {
		t.Errorf("helpful should keep its matched portfolio link: %+v", helpful)
	}
	if strings.Contains(story.Body, "guaranteed") || len(flagRules(story)) != 0 {
		t.Errorf("story after revision: %q %q", story.Body, flagRules(story))
	}
	if got := flagRules(short); !slices.Equal(got, []string{"Answer the question first:warning"}) {
		t.Errorf("short flags = %q", got)
	}
	if helpful.Kind != pipeline.KindReply || helpful.WordCount == 0 || helpful.BatchID != short.BatchID || helpful.Instruction == nil {
		t.Errorf("draft fields: %+v", helpful)
	}
	for _, want := range []string{"Instruction from the reviewer: Mention approval mode", "<external_content", portfolioURL, "no public URL"} {
		if !strings.Contains(first, want) {
			t.Errorf("draft message lacks %q", want)
		}
	}
	if strings.Index(first, "Containment write-up") > strings.Index(first, "Costs explainer") {
		t.Error("the closest portfolio item must be listed first")
	}
	_ = other

	// The opportunity no longer needs eager drafts once it has some.
	today := pgtype.Date{Time: time.Now().UTC().Truncate(24 * time.Hour), Valid: true}
	if _, err := e.svc.Pool.Exec(e.ctx, "UPDATE opportunities SET featured_on = $1 WHERE id = $2", today, opp); err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.q.FeaturedWithoutDrafts(e.ctx, store.FeaturedWithoutDraftsParams{WorkspaceID: e.ws, Today: today}); len(ids) != 0 {
		t.Errorf("featured with drafts listed for eager drafting: %v", ids)
	}
	// Latest batch only.
	if _, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp}); err != nil {
		t.Fatal(err)
	}
	latest, _ := e.q.ListLatestDrafts(e.ctx, store.ListLatestDraftsParams{WorkspaceID: e.ws, OpportunityID: opp})
	if len(latest) != 3 || latest[0].BatchID == helpful.BatchID {
		t.Errorf("latest batch: %d drafts, same batch=%v", len(latest), latest[0].BatchID == helpful.BatchID)
	}
}

func TestDraftCommunityForbidsLinks(t *testing.T) {
	e := setup(t)
	e.rules(`{"no_links":true,"disclose_affiliation":true}`)
	url := portfolioURL
	p := e.portfolio("Containment write-up", &url, true)
	opp := e.opportunity("twin: is it safe to let an AI agent trade my Trello account? I want to approve each order.")
	e.fr.draft = func(req llm.ChatRequest) string {
		if !strings.Contains(req.System, "No links of any kind") {
			t.Error("prompt must forbid links when the community does")
		}
		return draftsJSON(
			reply("helpful", "Disclosure: I work on Benchwright. Approval mode helps. Details at "+portfolioURL+" or benchwright.dev.", true, p.String()),
			reply("story", "Benchwright refused my order once and told me why.", false),
			reply("short", "Approve every order.", false),
		)
	}
	drafts, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp})
	if err != nil {
		t.Fatal(err)
	}
	h := drafts[0]
	if h.IncludeLink || strings.Contains(h.Body, "http") || strings.Contains(h.Body, "benchwright.dev") {
		t.Errorf("forbidden link survived: include_link=%v body=%q", h.IncludeLink, h.Body)
	}
	if got := flagRules(h); !slices.Equal(got, []string{"no_links:info"}) {
		t.Errorf("helpful flags = %q", got)
	}
	if got := flagRules(drafts[1]); !slices.Equal(got, []string{"disclose_affiliation:block"}) {
		t.Errorf("undisclosed mention in a disclose_affiliation community must block: %q", got)
	}
}

func TestDraftChecks(t *testing.T) {
	e := setup(t)
	opp := e.opportunity("Is it safe to let an AI agent trade my Trello account? I want to approve each order first.")
	ok := draftsJSON(reply("helpful", "Use approval mode.", false), reply("story", "I approve each order.", false), reply("short", "Approve orders.", false))
	e.fr.draft = func(llm.ChatRequest) string { return ok }

	t.Run("failed model check is flagged, not fatal", func(t *testing.T) {
		e.rules(`{"notes":["Be kind"]}`)
		e.fr.check = func(string) string { return "not json" }
		drafts, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp})
		if err != nil {
			t.Fatal(err)
		}
		if got := flagRules(drafts[0]); !slices.Equal(got, []string{"rules_check:warning"}) {
			t.Errorf("flags = %q", got)
		}
	})
	t.Run("pitch needs a subject", func(t *testing.T) {
		pitch := func(subject any) string {
			b, _ := json.Marshal(map[string]any{"drafts": []map[string]any{{"variant": "pitch", "subject": subject, "body": "Angle. [availability]", "portfolio_item_ids": []string{}, "include_link": false}}})
			return string(b)
		}
		e.fr.draft = func(req llm.ChatRequest) string {
			if !strings.Contains(req.System, "one pitch") {
				t.Error("pitch prompt not rendered")
			}
			return pitch("Why read-only vendor APIs push builders to workarounds")
		}
		drafts, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp, Kind: pipeline.KindPitch})
		if err != nil || len(drafts) != 1 || drafts[0].Subject == nil || drafts[0].Kind != "pitch" {
			t.Fatalf("pitch = %+v, %v", drafts, err)
		}
		e.fr.draft = func(llm.ChatRequest) string { return pitch(nil) }
		if _, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp, Kind: pipeline.KindPitch}); err == nil {
			t.Error("a pitch without a subject must fail schema validation")
		}
		e.fr.draft = func(llm.ChatRequest) string { return ok }
	})
	t.Run("unknown kind", func(t *testing.T) {
		if _, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp, Kind: "tweet"}); !errors.Is(err, pipeline.ErrUnknownKind) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no usable drafts retries", func(t *testing.T) {
		e.fr.draft = func(llm.ChatRequest) string { return `{"drafts":[]}` }
		if _, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp}); err == nil || errors.Is(err, pipeline.ErrUnknownKind) {
			t.Errorf("err = %v, want a retryable error", err)
		}
		e.fr.draft = func(llm.ChatRequest) string { return ok }
	})
	t.Run("below the lane minimum", func(t *testing.T) {
		var n pgtype.Numeric
		_ = n.Scan("0.99")
		if _, err := e.q.UpdateLaneConfig(e.ctx, store.UpdateLaneConfigParams{WorkspaceID: e.ws, Lane: store.LaneInbound, Weights: []byte(`{"intent":1}`),
			DailyQuota: 2, FreshnessHours: 48, MinScore: n, DraftKind: "reply"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: e.ws, Opportunity: opp}); !errors.Is(err, pipeline.ErrBelowMinScore) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("other workspace", func(t *testing.T) {
		other, _ := e.q.CreateWorkspace(e.ctx, "Other")
		if _, err := e.svc.Draft(e.ctx, pipeline.DraftRequest{Workspace: other.ID, Opportunity: opp}); !errors.Is(err, pipeline.ErrNoOpportunity) {
			t.Errorf("err = %v", err)
		}
	})
}
