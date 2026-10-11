package research

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/agent"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

type countingFetch struct{ calls int }

func (c *countingFetch) Article(context.Context, string) (*fetch.Page, error) {
	c.calls++
	return nil, &fetch.StatusError{Code: 403}
}
func (c *countingFetch) Image(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("no")
}

func TestFetchURLReusesStoredItemsAndFailures(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "A")
	src, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws.ID, Connector: "rss", Name: "Feed", Config: []byte(`{}`), Enabled: true})
	title := "Stored question"
	sum := sha256.Sum256([]byte(title))
	rid, err := q.InsertRawItem(ctx, store.InsertRawItemParams{WorkspaceID: ws.ID, SourceID: src.ID, ExternalID: "x", Url: "https://stackoverflow.com/q/1", Title: &title,
		Body: "The stored body text.", Metrics: []byte(`{}`), ContentHash: sum[:]})
	if err != nil {
		t.Fatal(err)
	}
	cf := &countingFetch{}
	s := &Service{Pool: pool, Fetch: cf, Log: pgtest.DiscardLogger()}
	nb := agent.NewNotebook()
	reg := s.builtinTools(ws.ID, nb, true)
	fetchURL, _ := reg.Get(ToolFetchURL)
	n := nb.Cite(agent.Source{Kind: "corpus", RawItemID: &rid, URL: "https://stackoverflow.com/q/1", Title: title})
	out, err := fetchURL.Handler(ctx, json.RawMessage(`{"source":`+itoa(n)+`}`))
	if err != nil || !strings.Contains(out.Text, "The stored body text.") || cf.calls != 0 {
		t.Fatalf("stored item: %q err=%v fetches=%d", out.Text, err, cf.calls)
	}
	web := nb.Cite(agent.Source{Kind: "web", URL: "https://blocked.example/page"})
	for i := 0; i < 2; i++ {
		_, err := fetchURL.Handler(ctx, json.RawMessage(`{"source":`+itoa(web)+`}`))
		if err == nil {
			t.Fatal("expected an error")
		}
		if i == 1 && !strings.Contains(err.Error(), "already failed") {
			t.Errorf("second attempt: %v", err)
		}
	}
	if cf.calls != 1 {
		t.Errorf("fetches = %d, want 1 (failures are not retried)", cf.calls)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestKnowledgeTools(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "A")
	src, err := q.CreateKnowledgeSource(ctx, store.CreateKnowledgeSourceParams{WorkspaceID: ws.ID, Name: "docs", Repo: "o/docs", Branch: "main", PathPrefix: "docs/", Exclude: []string{}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := q.UpsertKnowledgeDoc(ctx, store.UpsertKnowledgeDocParams{WorkspaceID: ws.ID, SourceID: src.ID, Path: "docs/SECURITY.md", BlobSha: "b1", CommitSha: "abcdef123", Title: "Security", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct{ h, a, body string }{
		{"SECURITY.md › Instance isolation", "instance-isolation", "Each customer agent runs in its own namespace with default-deny network policy."},
		{"SECURITY.md › Secrets", "secrets", "Vendor tokens are encrypted at rest."},
	} {
		if err := q.InsertKnowledgeChunk(ctx, store.InsertKnowledgeChunkParams{WorkspaceID: ws.ID, DocID: doc, Ord: int32(i), Heading: c.h, Anchor: c.a, Content: c.body}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.UpsertMemoryBankFile(ctx, store.UpsertMemoryBankFileParams{WorkspaceID: ws.ID, Name: "systemPatterns", Content: "# systemPatterns\n\nNamespaces per customer (SECURITY.md).", Sources: []string{}, CommitSha: strPtr("abcdef123")}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Pool: pool, Log: pgtest.DiscardLogger()}
	nb := agent.NewNotebook()
	reg := s.builtinTools(ws.ID, nb, false)
	call := func(name, args string) (agent.Output, error) {
		tool, _ := reg.Get(name)
		return tool.Handler(ctx, json.RawMessage(args))
	}
	out, err := call(ToolSearchKnowledge, `{"query":"namespace isolation"}`)
	if err != nil || !strings.Contains(out.Text, "[1] SECURITY.md › Instance isolation") || !strings.Contains(out.Text, "Internal document") {
		t.Fatalf("search = %q %v", out.Text, err)
	}
	src1, _ := nb.Source(1)
	if src1.Kind != "internal" || src1.URL != "https://github.com/o/docs/blob/abcdef123/docs/SECURITY.md#instance-isolation" {
		t.Errorf("citation = %+v", src1)
	}
	if out, err := call(ToolReadDoc, `{"source":1}`); err != nil || !strings.Contains(out.Text, "default-deny network policy") {
		t.Errorf("read by source = %q %v", out.Text, err)
	}
	if out, err := call(ToolReadDoc, `{"path":"SECURITY.md"}`); err != nil || !strings.Contains(out.Text, "- SECURITY.md › Secrets") {
		t.Errorf("table of contents = %q %v", out.Text, err)
	}
	if out, err := call(ToolReadDoc, `{"path":"SECURITY.md","section":"secrets"}`); err != nil || !strings.Contains(out.Text, "encrypted at rest") || !strings.Contains(out.Text, "[2]") {
		t.Errorf("read section = %q %v", out.Text, err)
	}
	if out, err := call(ToolReadMemoryBank, `{"file":"systemPatterns"}`); err != nil || !strings.Contains(out.Text, "commit abcdef1") || !strings.Contains(out.Text, "cite doc sections") {
		t.Errorf("memory bank = %q %v", out.Text, err)
	}
	// The memory bank is optional and builds incrementally: a missing file is
	// a note to the model, not a tool error.
	if out, err := call(ToolReadMemoryBank, `{"file":"progress"}`); err != nil || !strings.Contains(out.Text, "no progress file yet") {
		t.Errorf("missing file = %q %v", out.Text, err)
	}
	if ex, err := s.unusedKnowledgeTools(ctx, ws.ID); err != nil || len(ex) != 0 {
		t.Errorf("a workspace with docs and memory bank files uses every knowledge tool: %v %v", ex, err)
	}
	empty, _ := q.CreateWorkspace(ctx, "Empty")
	if ex, err := s.unusedKnowledgeTools(ctx, empty.ID); err != nil || strings.Join(ex, ",") != "search_knowledge,read_doc,read_memory_bank" {
		t.Errorf("a workspace without knowledge should leave the knowledge tools out: %v %v", ex, err)
	}
}

func strPtr(s string) *string { return &s }

func TestSearchBrandIsCitable(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "Benchwright")
	if _, err := q.UpsertBrandProfile(ctx, store.UpsertBrandProfileParams{WorkspaceID: ws.ID, Profile: []byte(`{"services":["Client portals"]}`)}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Pool: pool, Log: pgtest.DiscardLogger()}
	nb := agent.NewNotebook()
	tool, _ := s.builtinTools(ws.ID, nb, false).Get(ToolSearchBrand)
	out, err := tool.Handler(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Text, "[1] Benchwright brand profile") || !strings.Contains(out.Text, "Client portals") {
		t.Fatalf("brand output = %q", out.Text)
	}
	src, ok := nb.Source(1)
	if !ok || src.Kind != "brand" || src.URL != "/brand" {
		t.Fatalf("brand source = %+v %v", src, ok)
	}
	// Asking again reuses the same number.
	again, _ := tool.Handler(ctx, json.RawMessage(`{}`))
	if !strings.HasPrefix(again.Text, "[1] ") {
		t.Errorf("second call = %q", again.Text)
	}
}
