package seed_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/examples"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/seed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

func counts(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"workspaces", "users", "memberships", "lane_configs", "model_providers", "task_routes", "keywords", "competitors", "icps", "portfolio_items", "brand_profiles", "sources", "monitors", "prompts"} {
		var n int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tbl] = n
	}
	var j int
	_ = pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'embed_entity'").Scan(&j)
	out["embed_jobs"] = j
	return out
}

func setup(t *testing.T) (*pgxpool.Pool, *config.Config, func(seed.Options) error) {
	t.Helper()
	pool := pgtest.NewMigratedDB(t)
	log := pgtest.DiscardLogger()
	ins, err := jobs.NewInsertClient(pool, log)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load("../../config.example.yaml", func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return pool, cfg, func(o seed.Options) error { return seed.Run(context.Background(), pool, ins, cfg, o, log) }
}

var admin = seed.Admin{Email: "Admin@Example.com", Password: "a-long-enough-password"}

func TestSeedWithoutBrandPack(t *testing.T) {
	pool, cfg, run := setup(t)
	if err := run(seed.Options{Admin: seed.Admin{Email: "x@example.com", Password: "short"}}); err == nil {
		t.Fatal("short admin password must be rejected")
	}
	if err := run(seed.Options{Admin: admin}); err != nil {
		t.Fatal(err)
	}
	c := counts(t, pool)
	want := map[string]int{"workspaces": 1, "users": 1, "memberships": 1, "lane_configs": 3, "task_routes": len(cfg.Routes),
		"brand_profiles": 1, "keywords": 0, "competitors": 0, "icps": 0, "portfolio_items": 0, "sources": 2, "monitors": 1, "embed_jobs": 0}
	for k, v := range want {
		if c[k] != v {
			t.Errorf("%s = %d, want %d", k, c[k], v)
		}
	}
	if c["prompts"] == 0 {
		t.Error("shipped prompts not installed")
	}
	q := store.New(pool)
	ws, err := q.GetWorkspaceByName(context.Background(), seed.DefaultWorkspaceName)
	if err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	m, _ := q.ListMonitors(context.Background(), ws.ID)
	if len(m) != 1 || m[0].Enabled {
		t.Errorf("starter monitor should exist and be disabled: %+v", m)
	}
}

func TestSeedBrandPackIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, _, run := setup(t)
	pack, err := examples.BrandPack("benchwright")
	if err != nil {
		t.Fatal(err)
	}
	if err := run(seed.Options{Admin: admin, Brand: pack}); err != nil {
		t.Fatal(err)
	}
	first := counts(t, pool)
	if first["keywords"] != len(pack.Keywords.Track)+len(pack.Keywords.Negative) || first["competitors"] != len(pack.Competitors) ||
		first["icps"] != len(pack.ICPs) || first["portfolio_items"] != len(pack.Portfolio) || first["embed_jobs"] != len(pack.ICPs)+len(pack.Portfolio) {
		t.Fatalf("pack not imported: %v", first)
	}
	q := store.New(pool)
	ws, err := q.GetWorkspaceByName(ctx, pack.Name)
	if err != nil {
		t.Fatalf("workspace should be named after the pack: %v", err)
	}
	p, _ := q.GetBrandProfile(ctx, ws.ID)
	if !strings.Contains(string(p.Profile), "evaluation harness") {
		t.Errorf("profile not imported: %s", p.Profile)
	}

	// UI edits survive a re-seed, and re-seeding without a workspace name
	// reuses the admin's workspace instead of creating another.
	if _, err := q.UpsertBrandProfile(ctx, store.UpsertBrandProfileParams{WorkspaceID: ws.ID, Profile: []byte(`{"services":["edited"]}`)}); err != nil {
		t.Fatal(err)
	}
	if err := run(seed.Options{Admin: admin, Brand: pack}); err != nil {
		t.Fatal(err)
	}
	if err := run(seed.Options{Admin: admin}); err != nil {
		t.Fatal(err)
	}
	second := counts(t, pool)
	for k, v := range first {
		if second[k] != v {
			t.Errorf("%s changed on re-seed: %d -> %d", k, v, second[k])
		}
	}
	p, _ = q.GetBrandProfile(ctx, ws.ID)
	if string(p.Profile) != `{"services": ["edited"]}` {
		t.Errorf("re-seed overwrote an edited brand profile: %s", p.Profile)
	}

	// An explicit workspace name creates that workspace.
	if err := run(seed.Options{Admin: admin, WorkspaceName: "Second"}); err != nil {
		t.Fatal(err)
	}
	if n := counts(t, pool)["workspaces"]; n != 2 {
		t.Errorf("workspaces = %d, want 2", n)
	}
}
