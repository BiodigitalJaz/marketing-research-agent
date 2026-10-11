package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestBrandPackImportExport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.workspace("Benchwright")
	e.user("ed@example.com", ws, store.MemberRoleEditor)
	e.user("viewer@example.com", ws, store.MemberRoleViewer)
	ed := e.client()
	ed.login("ed@example.com")

	raw, err := os.ReadFile("../../examples/brand-packs/benchwright.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pack, err := brand.ParsePack(raw)
	if err != nil {
		t.Fatal(err)
	}
	type summary struct {
		ProfileFields                          int `json:"profile_fields"`
		Keywords, Competitors, ICPs, Portfolio int
	}
	importPack := func(c *client, body string, mode string, dry bool) (int, summary, []byte) {
		t.Helper()
		code, resp := c.do(http.MethodPost, "/brand/import", map[string]any{"pack": body, "mode": mode, "dry_run": dry})
		var out struct{ Summary summary }
		_ = json.Unmarshal(resp, &out)
		return code, out.Summary, resp
	}
	count := func(tbl string) int {
		var n int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM "+tbl+" WHERE workspace_id = $1", ws).Scan(&n)
		return n
	}
	wantKeywords := len(pack.Keywords.Track) + len(pack.Keywords.Negative)

	viewer := e.client()
	viewer.login("viewer@example.com")
	if code, _, _ := importPack(viewer, string(raw), "merge", false); code != http.StatusForbidden {
		t.Fatalf("viewer import = %d", code)
	}
	if code, _, body := importPack(ed, "profil: {}", "merge", false); code != http.StatusBadRequest || !strings.Contains(string(body), "profil") {
		t.Fatalf("bad pack = %d %s", code, body)
	}

	// A dry run reports what would change and writes nothing.
	code, s, body := importPack(ed, string(raw), "merge", true)
	if code != http.StatusOK || s.Keywords != wantKeywords || s.ICPs != len(pack.ICPs) || s.ProfileFields == 0 {
		t.Fatalf("dry run = %d %s", code, body)
	}
	if count("keywords") != 0 || count("icps") != 0 {
		t.Fatal("dry run wrote rows")
	}

	// Merge imports everything into an empty brand; a second merge adds nothing.
	if code, s, body = importPack(ed, string(raw), "merge", false); code != http.StatusOK || s.Competitors != len(pack.Competitors) || s.Portfolio != len(pack.Portfolio) {
		t.Fatalf("merge = %d %s", code, body)
	}
	if code, s, _ = importPack(ed, string(raw), "merge", false); code != http.StatusOK || s != (summary{}) {
		t.Fatalf("second merge should change nothing: %+v", s)
	}
	var embeds int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'embed_entity'").Scan(&embeds)
	if embeds != len(pack.ICPs)+len(pack.Portfolio) {
		t.Errorf("embed jobs = %d", embeds)
	}

	// Merge keeps an edited field; replace takes the pack's.
	if code, _ := ed.do(http.MethodPut, "/brand/profile", map[string]any{"services": []string{"Edited service"}}); code != http.StatusOK {
		t.Fatalf("edit profile = %d", code)
	}
	importPack(ed, string(raw), "merge", false)
	p, _ := e.q.GetBrandProfile(ctx, ws)
	if !strings.Contains(string(p.Profile), "Edited service") {
		t.Errorf("merge overwrote an edited field: %s", p.Profile)
	}
	if code, _ := ed.do(http.MethodPost, "/keywords", map[string]any{"term": "extra keyword", "kind": "track"}); code != http.StatusCreated {
		t.Fatalf("add keyword = %d", code)
	}
	if code, _, body = importPack(ed, string(raw), "replace", false); code != http.StatusOK {
		t.Fatalf("replace = %d %s", code, body)
	}
	p, _ = e.q.GetBrandProfile(ctx, ws)
	if strings.Contains(string(p.Profile), "Edited service") || count("keywords") != wantKeywords {
		t.Errorf("replace kept old data: keywords=%d profile=%s", count("keywords"), p.Profile)
	}

	// Renaming the workspace to the pack's name is for admins.
	if code, _ := ed.do(http.MethodPost, "/brand/import", map[string]any{"pack": "name: Renamed Co\n", "mode": "merge", "rename_workspace": true}); code != http.StatusForbidden {
		t.Fatalf("editor rename = %d", code)
	}
	e.user("admin@example.com", ws, store.MemberRoleAdmin)
	admin := e.client()
	admin.login("admin@example.com")
	if code, body := admin.do(http.MethodPost, "/brand/import", map[string]any{"pack": "name: Renamed Co\n", "mode": "merge", "rename_workspace": true}); code != http.StatusOK || !strings.Contains(string(body), `"renamed":true`) {
		t.Fatalf("admin rename = %d %s", code, body)
	}
	if w, _ := e.q.GetWorkspace(ctx, ws); w.Name != "Renamed Co" {
		t.Errorf("workspace = %q", w.Name)
	}
	if _, err := e.q.RenameWorkspace(ctx, store.RenameWorkspaceParams{ID: ws, Name: "Benchwright"}); err != nil {
		t.Fatal(err)
	}

	// Export round-trips through the pack parser.
	code, out := ed.do(http.MethodGet, "/brand/export", nil)
	if code != http.StatusOK {
		t.Fatalf("export = %d %s", code, out)
	}
	back, err := brand.ParsePack(out)
	if err != nil {
		t.Fatalf("exported pack does not parse: %v\n%s", err, out)
	}
	if back.Name != "Benchwright" || len(back.Keywords.Track) != len(pack.Keywords.Track) || len(back.ICPs) != len(pack.ICPs) || back.ICPs[0].Roles == nil || back.Profile.Tone.Voice == "" {
		t.Errorf("export lost data: %+v", back)
	}
}
