package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestKnowledgeAPI(t *testing.T) {
	se := newSettingsEnv(t)
	ctx := context.Background()
	admin := se.admin
	editor := se.client()
	editor.login("editor@a.com")

	if code, _ := editor.do(http.MethodPost, "/settings/knowledge", map[string]any{"repo": "o/r"}); code != http.StatusForbidden {
		t.Fatalf("editor create = %d", code)
	}
	for _, bad := range []map[string]any{{"repo": "not-a-repo"}, {"repo": "o/r", "path_prefix": "../etc"}, {"repo": "o/r", "token_env": "lower"}, {"repo": "o/r", "branch": "a b"}} {
		if code, _ := admin.do(http.MethodPost, "/settings/knowledge", bad); code != http.StatusBadRequest {
			t.Fatalf("create %v = %d", bad, code)
		}
	}
	const token = "github_pat_secretvalue1234"
	code, body := admin.do(http.MethodPost, "/settings/knowledge", map[string]any{"name": "Product docs", "repo": "example-org/docs", "path_prefix": "docs", "token": token})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	if strings.Contains(string(body), token) {
		t.Fatal("the token must never be returned")
	}
	var src struct {
		ID         string   `json:"id"`
		Branch     string   `json:"branch"`
		PathPrefix string   `json:"path_prefix"`
		Exclude    []string `json:"exclude"`
		Token      struct {
			Source string `json:"source"`
			Last4  string `json:"last4"`
		} `json:"token"`
	}
	_ = json.Unmarshal(body, &src)
	if src.Branch != "main" || src.PathPrefix != "docs/" || src.Token.Source != "secret" || src.Token.Last4 != "1234" || src.Exclude == nil {
		t.Fatalf("source = %+v (defaults: main, docs/, an empty exclude list)", src)
	}
	var syncs int
	_ = se.pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'sync_knowledge'").Scan(&syncs)
	if syncs != 1 {
		t.Errorf("saving should queue a sync; jobs = %d", syncs)
	}
	// The sync queued on save is still pending: another must be refused.
	if code, b := admin.do(http.MethodPost, "/settings/knowledge/"+src.ID+"/sync", nil); code != http.StatusConflict || errCode(b) != "busy" {
		t.Fatalf("sync while one is queued = %d %s", code, b)
	}
	if code, _ := admin.do(http.MethodPost, "/settings/knowledge/rebuild", nil); code != http.StatusConflict {
		t.Fatalf("rebuild while a sync is queued = %d", code)
	}
	if _, err := se.pool.Exec(ctx, "UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'sync_knowledge'"); err != nil {
		t.Fatal(err)
	}
	if code, b := admin.do(http.MethodPost, "/settings/knowledge/"+src.ID+"/sync?force=true", nil); code != http.StatusAccepted {
		t.Fatalf("sync when idle = %d %s", code, b)
	}
	if _, err := se.pool.Exec(ctx, "UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'sync_knowledge'"); err != nil {
		t.Fatal(err)
	}
	code, body = admin.do(http.MethodGet, "/settings/knowledge", nil)
	if code != http.StatusOK || strings.Contains(string(body), token) || !strings.Contains(string(body), `"core_files":["projectbrief"`) {
		t.Fatalf("get = %d %s", code, body)
	}
	if code, body := admin.do(http.MethodGet, "/settings/knowledge/"+src.ID+"/docs", nil); code != http.StatusOK || !strings.Contains(string(body), `"items":[]`) {
		t.Fatalf("docs = %d %s", code, body)
	}

	// Members can read the memory bank.
	ws, _ := se.q.GetWorkspaceByName(ctx, "Benchwright")
	if err := se.q.UpsertMemoryBankFile(ctx, store.UpsertMemoryBankFileParams{WorkspaceID: ws.ID, Name: "progress", Content: "# progress", Sources: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := se.q.UpsertMemoryBankFile(ctx, store.UpsertMemoryBankFileParams{WorkspaceID: ws.ID, Name: "projectbrief", Content: "# projectbrief", Sources: []string{}}); err != nil {
		t.Fatal(err)
	}
	code, body = editor.do(http.MethodGet, "/knowledge/memory-bank", nil)
	if code != http.StatusOK || strings.Index(string(body), "projectbrief") > strings.Index(string(body), `"progress"`) {
		t.Fatalf("memory bank = %d %s (core file order)", code, body)
	}
	if code, b := admin.do(http.MethodPost, "/settings/knowledge/rebuild", nil); code != http.StatusAccepted {
		t.Fatalf("rebuild = %d %s", code, b)
	}

	// Updating keeps the token unless cleared; deleting removes the source.
	code, body = admin.do(http.MethodPut, "/settings/knowledge/"+src.ID, map[string]any{"repo": "example-org/docs", "path_prefix": "docs/"})
	if code != http.StatusOK || !strings.Contains(string(body), `"last4":"1234"`) {
		t.Fatalf("update = %d %s", code, body)
	}
	if code, _ := admin.do(http.MethodDelete, "/settings/knowledge/"+src.ID, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
}
