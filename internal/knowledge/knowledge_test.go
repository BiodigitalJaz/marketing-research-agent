package knowledge_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/knowledge"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

// fakeGitHub serves branches, trees, and blobs for one repository.
type fakeGitHub struct {
	mu     sync.Mutex
	head   string
	files  map[string]string // path under docs/ -> content
	blobs  map[string]string // sha -> content
	hits   map[string]int    // request kind -> count
	tokens []string
}

func (f *fakeGitHub) sha(content string) string {
	h := sha256.Sum256([]byte(content))
	s := hex.EncodeToString(h[:8])
	f.blobs[s] = content
	return s
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, r.Header.Get("Authorization"))
	p := r.URL.Path
	switch {
	case p == "/repos/o/docs/branches/main":
		f.hits["head"]++
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"sha": f.head}})
	case p == "/repos/o/docs/git/trees/"+f.head:
		f.hits["root"]++
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": []map[string]any{{"path": "docs", "type": "tree", "sha": "docs-" + f.head}, {"path": "src", "type": "tree", "sha": "x"}}})
	case p == "/repos/o/docs/git/trees/docs-"+f.head:
		f.hits["tree"]++
		var tree []map[string]any
		for path, c := range f.files {
			tree = append(tree, map[string]any{"path": path, "type": "blob", "sha": f.sha(c), "size": len(c)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": tree})
	case strings.HasPrefix(p, "/repos/o/docs/git/blobs/"):
		f.hits["blob"]++
		c := f.blobs[strings.TrimPrefix(p, "/repos/o/docs/git/blobs/")]
		_ = json.NewEncoder(w).Encode(map[string]any{"content": base64.StdEncoding.EncodeToString([]byte(c)), "encoding": "base64"})
	default:
		http.NotFound(w, r)
	}
}

type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake-embed" }
func (fakeEmbedder) Dims() int     { return 4 }
func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := []float32{1, 0, 0, 0}
		if strings.Contains(strings.ToLower(t), "isolation") {
			v = []float32{0, 1, 0, 0}
		}
		out[i] = v
	}
	return out, nil
}

// fakeRouter answers memory bank prompts and counts core-file builds.
type fakeRouter struct {
	mu    sync.Mutex
	files map[string]int
}

func (f *fakeRouter) Resolve(context.Context, uuid.UUID, string) (*router.Resolved, error) {
	return &router.Resolved{Targets: []router.Target{{Caps: llm.Capabilities{MaxContext: 8192}}}}, nil
}

func (f *fakeRouter) ChatTargets(_ context.Context, _ router.Call, _ []router.Target, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text := "summary"
	user := req.Messages[0].Parts[0].Text
	switch {
	case req.SchemaName == "knowledge_digest":
		informs := `["projectbrief","productContext"]`
		if strings.Contains(user, "SECURITY.md") {
			informs = `["systemPatterns","techContext"]`
		}
		text = `{"digest":"Digest of the doc.","informs":` + informs + `}`
	case strings.Contains(req.System, "memory bank: a concise"):
		for _, cf := range knowledge.CoreFileNames() {
			if strings.Contains(req.System, "this file is "+cf+".md") {
				f.files[cf]++
				text = "# " + cf + "\n\nGenerated."
			}
		}
	}
	return &llm.ChatResponse{Parts: []llm.Part{llm.TextPart(text)}}, []router.Attempt{{}}, nil
}

func setup(t *testing.T, gh *fakeGitHub) (*knowledge.Service, *store.Queries, uuid.UUID, store.KnowledgeSource, *fakeRouter) {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "Benchwright")
	defs, _ := prompts.Defaults()
	for _, d := range defs {
		_ = q.InsertDefaultPromptIfMissing(ctx, store.InsertDefaultPromptIfMissingParams{Task: d.Task, Version: d.Version, Body: d.Body})
	}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	env := "GH_TOKEN"
	src, err := q.CreateKnowledgeSource(ctx, store.CreateKnowledgeSourceParams{WorkspaceID: ws.ID, Name: "Product docs", Repo: "o/docs", Branch: "main",
		PathPrefix: "docs/", Exclude: []string{"INTERNAL-RESULTS.md", "spikes/*/capture/**"}, TokenEnv: &env, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	fr := &fakeRouter{files: map[string]int{}}
	svc := &knowledge.Service{Pool: pool, Get: fetch.New(fetch.Options{AllowPrivate: true, PerHostRate: 1000}), APIBase: srv.URL,
		Env: func(k string) string {
			if k == "GH_TOKEN" {
				return "tok-123"
			}
			return ""
		},
		Embedder: func(context.Context, uuid.UUID) (embed.Embedder, error) { return fakeEmbedder{}, nil },
		Router:   fr, Log: pgtest.DiscardLogger()}
	return svc, q, ws.ID, src, fr
}

func reload(t *testing.T, q *store.Queries, src store.KnowledgeSource) store.KnowledgeSource {
	t.Helper()
	s, err := q.GetKnowledgeSource(context.Background(), store.GetKnowledgeSourceParams{WorkspaceID: src.WorkspaceID, ID: src.ID})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSyncAndMemoryBank(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHub{head: "c1", blobs: map[string]string{}, hits: map[string]int{}, files: map[string]string{
		"SECURITY.md":                "# Security\n\n## Instance isolation\n\nEach customer runs in its own namespace.\n",
		"BUSINESS.md":                "# Business\n\n## Positioning\n\nContainment, not returns.\n",
		"INTERNAL-RESULTS.md":        "# Results\n\nNumbers that must never be indexed.\n",
		"spikes/x/capture/README.md": "# Capture\n",
		"diagram.png":                "binary",
	}}
	svc, q, ws, src, fr := setup(t, gh)

	res, err := svc.Sync(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 2 || res.Embedded == 0 || res.Commit != "c1" {
		t.Fatalf("first sync = %+v", res)
	}
	if !slices.Contains(gh.tokens, "Bearer tok-123") {
		t.Error("the token must be sent")
	}
	docs, _ := q.ListKnowledgeDocs(ctx, store.ListKnowledgeDocsParams{WorkspaceID: ws, SourceID: src.ID})
	var paths []string
	for _, d := range docs {
		paths = append(paths, d.Path)
	}
	if strings.Join(paths, ",") != "docs/BUSINESS.md,docs/SECURITY.md" {
		t.Fatalf("docs = %v (excluded and non-Markdown files must be skipped)", paths)
	}

	// Unchanged branch: one request, no tree or blob reads.
	src = reload(t, q, src)
	before := gh.hits["blob"] + gh.hits["tree"]
	if res, err := svc.Sync(ctx, src); err != nil || res.Changed != 0 || gh.hits["blob"]+gh.hits["tree"] != before {
		t.Fatalf("unchanged sync = %+v %v (reads %d -> %d)", res, err, before, gh.hits["blob"]+gh.hits["tree"])
	}

	// Build the memory bank.
	done, err := svc.BuildMemoryBank(ctx, ws, time.Now().Add(time.Minute))
	if err != nil || !done {
		t.Fatalf("build = %v %v", done, err)
	}
	files, _ := q.ListMemoryBank(ctx, ws)
	if len(files) != len(knowledge.CoreFiles) {
		t.Fatalf("memory bank has %d files, want %d", len(files), len(knowledge.CoreFiles))
	}
	idx := knowledge.MemoryIndex(ctx, q, ws)
	if !strings.Contains(idx, "projectbrief.md") || !strings.Contains(idx, "systemPatterns.md") {
		t.Errorf("index = %s", idx)
	}

	// A security change: only that doc is re-read, and only the core files
	// it informs (plus nothing else) are rebuilt. projectbrief is untouched,
	// so dependents keep their signature.
	gh.mu.Lock()
	gh.head = "c2"
	gh.files["SECURITY.md"] = "# Security\n\n## Instance isolation\n\nNamespaces plus network policy.\n"
	delete(gh.files, "BUSINESS.md")
	gh.files["BUSINESS.md"] = "# Business\n\n## Positioning\n\nContainment, not returns.\n" // same content, same blob
	gh.mu.Unlock()
	blobs := gh.hits["blob"]
	src = reload(t, q, src)
	res, err = svc.Sync(ctx, src)
	if err != nil || res.Changed != 1 || gh.hits["blob"]-blobs != 1 {
		t.Fatalf("changed sync = %+v %v blobs=%d", res, err, gh.hits["blob"]-blobs)
	}
	for k := range fr.files {
		fr.files[k] = 0
	}
	if done, err := svc.BuildMemoryBank(ctx, ws, time.Now().Add(time.Minute)); err != nil || !done {
		t.Fatal(err)
	}
	if fr.files["systemPatterns"] != 1 || fr.files["techContext"] != 1 || fr.files["projectbrief"] != 0 || fr.files["productContext"] != 0 {
		t.Errorf("rebuilt = %v, want only the files SECURITY.md informs", fr.files)
	}

	// Removing a doc drops it and its section embeddings.
	gh.mu.Lock()
	gh.head = "c3"
	delete(gh.files, "SECURITY.md")
	gh.mu.Unlock()
	src = reload(t, q, src)
	if res, err := svc.Sync(ctx, src); err != nil || res.Removed != 1 {
		t.Fatalf("removal sync = %+v %v", res, err)
	}
	counts, _ := q.KnowledgeCounts(ctx, store.KnowledgeCountsParams{WorkspaceID: ws, Model: "fake-embed"})
	if counts.Docs != 1 || counts.Embedded != counts.Chunks {
		t.Errorf("counts = %+v", counts)
	}
}

func TestSyncErrors(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHub{head: "c1", blobs: map[string]string{}, hits: map[string]int{}, files: map[string]string{}}
	svc, q, _, src, _ := setup(t, gh)
	src.Repo = "o/missing"
	if _, err := svc.Sync(ctx, src); !errors.Is(err, knowledge.ErrAccess) {
		t.Fatalf("err = %v, want an access error", err)
	}
	got := reload(t, q, src)
	if got.Status != "error" || got.LastError == nil || strings.Contains(*got.LastError, "tok-123") {
		t.Errorf("source = %s %v (errors are recorded, never with the token)", got.Status, got.LastError)
	}
}

func TestSyncAndBuildNeverOverlap(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHub{head: "c1", blobs: map[string]string{}, hits: map[string]int{}, files: map[string]string{"A.md": "# A\n\n## One\n\nText.\n"}}
	svc, _, ws, src, _ := setup(t, gh)
	// Hold the workspace lock the way a running build would.
	conn, err := svc.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", knowledge.LockKeyForTest(ws)).Scan(&got); err != nil || !got {
		t.Fatalf("lock: %v %v", got, err)
	}
	if _, err := svc.Sync(ctx, src); !errors.Is(err, knowledge.ErrBusy) {
		t.Errorf("sync during a build = %v, want ErrBusy", err)
	}
	if _, err := svc.BuildMemoryBank(ctx, ws, time.Now().Add(time.Minute)); !errors.Is(err, knowledge.ErrBusy) {
		t.Errorf("second build = %v, want ErrBusy", err)
	}
	if gh.hits["head"] != 0 {
		t.Error("a busy sync must not touch GitHub")
	}
	_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", knowledge.LockKeyForTest(ws))
	conn.Release()
	if res, err := svc.Sync(ctx, src); err != nil || res.Changed != 1 {
		t.Fatalf("sync after release = %+v %v", res, err)
	}
}
