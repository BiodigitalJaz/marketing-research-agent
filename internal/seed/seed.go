// Package seed bootstraps an install: a workspace, its admin user, lane
// configs, model providers and routes from config, the shipped prompts, an
// optional brand pack, and starter sources. Every step is idempotent:
// existing rows are left untouched, so re-running never overwrites edits made
// in the UI.
package seed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// DefaultWorkspaceName is used when neither the options nor a brand pack
// name the workspace.
const DefaultWorkspaceName = "My workspace"

// Admin identifies the bootstrap admin user.
type Admin struct {
	Email    string
	Name     string
	Password string
}

// Options control what Run creates.
type Options struct {
	Admin Admin
	// WorkspaceName selects (or creates) the workspace by name. When empty,
	// the admin's first workspace is used, else one named after the brand
	// pack, else DefaultWorkspaceName.
	WorkspaceName string
	// Brand, when set, is imported into an empty brand knowledge base.
	Brand *brand.Pack
}

// jobInserter enqueues jobs in a transaction (embeddings for seeded
// entities); *river.Client satisfies it.
type jobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

func numeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(f, 'f', 3, 64))
	return n
}

// Result identifies what Apply seeded.
type Result struct {
	WorkspaceID uuid.UUID
	User        store.User
}

// Run seeds everything in one transaction.
func Run(ctx context.Context, pool *pgxpool.Pool, ins jobInserter, cfg *config.Config, opts Options, log *slog.Logger) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := Apply(ctx, tx, ins, cfg, opts, log)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	log.InfoContext(ctx, "seed complete", "workspace_id", res.WorkspaceID)
	return nil
}

// InstallPrompts inserts the shipped prompt versions that are missing. It is
// idempotent; the API runs it at startup so new versions apply on upgrade.
func InstallPrompts(ctx context.Context, q *store.Queries) error {
	defaults, err := prompts.Defaults()
	if err != nil {
		return err
	}
	for _, d := range defaults {
		if err := q.InsertDefaultPromptIfMissing(ctx, store.InsertDefaultPromptIfMissingParams{Task: d.Task, Version: d.Version, Body: d.Body}); err != nil {
			return fmt.Errorf("prompt %s v%d: %w", d.Task, d.Version, err)
		}
	}
	return nil
}

// Apply seeds inside tx (the caller commits): the admin user, the workspace
// and membership, lanes, providers and routes from cfg, workspace settings,
// prompts, the brand pack, and starter sources.
func Apply(ctx context.Context, tx pgx.Tx, ins jobInserter, cfg *config.Config, opts Options, log *slog.Logger) (Result, error) {
	admin := opts.Admin
	admin.Email = strings.ToLower(strings.TrimSpace(admin.Email))
	if admin.Email == "" {
		return Result{}, errors.New("admin email is required")
	}
	q := store.New(tx)

	u, err := q.GetUserByEmail(ctx, admin.Email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if len(admin.Password) < auth.MinPasswordLen {
			return Result{}, fmt.Errorf("admin password must be at least %d characters", auth.MinPasswordLen)
		}
		hash, herr := auth.HashPassword(admin.Password, auth.DefaultArgon2)
		if herr != nil {
			return Result{}, herr
		}
		name := admin.Name
		if name == "" {
			name = "Admin"
		}
		if u, err = q.CreateUser(ctx, store.CreateUserParams{Email: admin.Email, Name: name, PasswordHash: hash}); err != nil {
			return Result{}, fmt.Errorf("create admin: %w", err)
		}
		log.InfoContext(ctx, "created admin user", "email", admin.Email)
	case err != nil:
		return Result{}, fmt.Errorf("lookup admin: %w", err)
	default:
		log.InfoContext(ctx, "admin user exists; password unchanged", "email", admin.Email)
	}
	ws, err := workspace(ctx, q, u.ID, opts, log)
	if err != nil {
		return Result{}, err
	}
	if err := q.AddMembership(ctx, store.AddMembershipParams{WorkspaceID: ws, UserID: u.ID, Role: store.MemberRoleAdmin}); err != nil {
		return Result{}, fmt.Errorf("membership: %w", err)
	}

	for _, lc := range defaultLaneConfigs {
		w, _ := json.Marshal(lc.weights)
		if err := q.InsertLaneConfigIfMissing(ctx, store.InsertLaneConfigIfMissingParams{
			WorkspaceID: ws, Lane: lc.lane, Weights: w, DailyQuota: lc.quota,
			FreshnessHours: lc.freshness, MinScore: numeric(lc.minScore), DraftKind: lc.draftKind,
		}); err != nil {
			return Result{}, fmt.Errorf("lane config %s: %w", lc.lane, err)
		}
	}

	providerKinds := map[string]config.Provider{}
	for _, p := range cfg.Providers {
		providerKinds[p.ID] = p
		var base, keyEnv *string
		if p.BaseURL != "" {
			b := p.BaseURL
			base = &b
		}
		if p.APIKeyEnv != "" {
			k := p.APIKeyEnv
			keyEnv = &k
		}
		opts := []byte("{}")
		if p.Kind == config.KindOpenAICompat && p.Local {
			opts = []byte(`{"reasoning_style":"reasoning_effort","max_concurrency":1}`)
		}
		name := p.ID
		if err := q.InsertProviderIfMissing(ctx, store.InsertProviderIfMissingParams{
			ID: p.ID, Kind: p.Kind, BaseUrl: base, IsLocal: p.Local, Name: &name, ApiKeyEnv: keyEnv, Options: opts,
		}); err != nil {
			return Result{}, fmt.Errorf("provider %s: %w", p.ID, err)
		}
	}
	declare := func(providerID, model string) error {
		p := providerKinds[providerID]
		km := defaultModel(p.Kind, model)
		caps, _ := json.Marshal(km.caps)
		if err := q.InsertProviderModelIfMissing(ctx, store.InsertProviderModelIfMissingParams{ProviderID: providerID, Model: model, Capabilities: caps}); err != nil {
			return fmt.Errorf("model %s/%s: %w", providerID, model, err)
		}
		price, _ := json.Marshal(km.price)
		return q.SetProviderModelPriceIfEmpty(ctx, store.SetProviderModelPriceIfEmptyParams{ProviderID: providerID, Model: model, Price: price})
	}
	for task, r := range cfg.Routes {
		params := store.InsertTaskRouteIfMissingParams{WorkspaceID: ws, Task: task, ProviderID: r.Provider, Model: r.Model}
		if r.Fallback != nil {
			fp, fm := r.Fallback.Provider, r.Fallback.Model
			params.FallbackProviderID, params.FallbackModel = &fp, &fm
		}
		if err := q.InsertTaskRouteIfMissing(ctx, params); err != nil {
			return Result{}, fmt.Errorf("route %s: %w", task, err)
		}
		if err := declare(r.Provider, r.Model); err != nil {
			return Result{}, err
		}
		if r.Fallback != nil {
			if err := declare(r.Fallback.Provider, r.Fallback.Model); err != nil {
				return Result{}, err
			}
		}
	}

	emb, _ := json.Marshal(map[string]any{"provider_id": cfg.Embeddings.Provider, "model": cfg.Embeddings.Model, "dimensions": cfg.Embeddings.Dimensions})
	search, _ := json.Marshal(map[string]any{"backend": cfg.Search.Backend, "searxng_url": cfg.Search.SearXNGURL, "api_key_env": cfg.Search.Brave.APIKeyEnv})
	if err := q.InsertWorkspaceSettingsIfMissing(ctx, store.InsertWorkspaceSettingsIfMissingParams{
		WorkspaceID: ws, LocalOnly: cfg.Mode.Offline, Embeddings: emb, Search: search,
	}); err != nil {
		return Result{}, fmt.Errorf("workspace settings: %w", err)
	}

	if err := InstallPrompts(ctx, q); err != nil {
		return Result{}, err
	}

	if err := seedBrand(ctx, tx, q, ins, ws, opts.Brand, log); err != nil {
		return Result{}, err
	}
	if err := seedCollection(ctx, q, ws, log); err != nil {
		return Result{}, err
	}
	return Result{WorkspaceID: ws, User: u}, nil
}

// workspace picks the workspace to seed: the one named in opts, else the
// admin's first workspace (so re-running the seed never creates a second
// one), else a new one named after the brand pack or DefaultWorkspaceName.
func workspace(ctx context.Context, q *store.Queries, user uuid.UUID, opts Options, log *slog.Logger) (uuid.UUID, error) {
	name := strings.TrimSpace(opts.WorkspaceName)
	if name == "" {
		mine, err := q.ListWorkspacesForUser(ctx, user)
		if err != nil {
			return uuid.Nil, fmt.Errorf("workspaces: %w", err)
		}
		if len(mine) > 0 {
			return mine[0].ID, nil
		}
		name = DefaultWorkspaceName
		if opts.Brand != nil && strings.TrimSpace(opts.Brand.Name) != "" {
			name = strings.TrimSpace(opts.Brand.Name)
		}
	}
	ws, err := q.GetWorkspaceByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		ws, err = q.CreateWorkspace(ctx, name)
		log.InfoContext(ctx, "created workspace", "name", name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("workspace: %w", err)
	}
	return ws.ID, nil
}

// seedBrand makes sure the workspace has a brand profile and imports the
// pack, if any. Each part of the pack is imported only where the workspace
// has nothing yet, so a re-run never duplicates or overrides UI edits.
func seedBrand(ctx context.Context, tx pgx.Tx, q *store.Queries, ins jobInserter, ws uuid.UUID, pack *brand.Pack, log *slog.Logger) error {
	if pack == nil {
		pack = &brand.Pack{}
	}
	pb, _ := json.Marshal(pack.Profile)
	if err := q.InsertBrandProfileIfMissing(ctx, store.InsertBrandProfileIfMissingParams{WorkspaceID: ws, Profile: pb}); err != nil {
		return fmt.Errorf("brand profile: %w", err)
	}
	for kind, terms := range map[string][]string{"track": pack.Keywords.Track, "negative": pack.Keywords.Negative} {
		for _, t := range terms {
			if err := q.InsertKeywordIfMissing(ctx, store.InsertKeywordIfMissingParams{WorkspaceID: ws, Term: t, Kind: kind}); err != nil {
				return fmt.Errorf("keyword %q: %w", t, err)
			}
		}
	}
	for _, c := range pack.Competitors {
		if _, err := q.GetCompetitorByName(ctx, store.GetCompetitorByNameParams{WorkspaceID: ws, Name: c.Name}); err == nil {
			continue
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		notes := c.Notes
		if _, err := q.CreateCompetitor(ctx, store.CreateCompetitorParams{WorkspaceID: ws, Name: c.Name, Domains: c.Domains, Notes: &notes}); err != nil {
			return fmt.Errorf("competitor %s: %w", c.Name, err)
		}
	}
	if n, err := q.CountICPs(ctx, ws); err != nil {
		return err
	} else if n == 0 {
		for _, i := range pack.ICPs {
			d, _ := json.Marshal(i.ICPDefinition)
			row, err := q.CreateICP(ctx, store.CreateICPParams{WorkspaceID: ws, Name: i.Name, Definition: d})
			if err != nil {
				return fmt.Errorf("icp %s: %w", i.Name, err)
			}
			if _, err := ins.InsertTx(ctx, tx, jobs.EmbedEntityArgs{WorkspaceID: ws, EntityType: jobs.EntityICP, EntityID: row.ID}, nil); err != nil {
				return err
			}
		}
	}
	if n, err := q.CountPortfolioItems(ctx, ws); err != nil {
		return err
	} else if n == 0 {
		for _, p := range pack.Portfolio {
			tags, _ := json.Marshal(p.Tags)
			var url *string
			if p.URL != "" {
				u := p.URL
				url = &u
			}
			row, err := q.CreatePortfolioItem(ctx, store.CreatePortfolioItemParams{WorkspaceID: ws, Kind: p.Kind, Title: p.Title, Url: url, Summary: p.Summary, Tags: tags})
			if err != nil {
				return fmt.Errorf("portfolio %q: %w", p.Title, err)
			}
			if _, err := ins.InsertTx(ctx, tx, jobs.EmbedEntityArgs{WorkspaceID: ws, EntityType: jobs.EntityPortfolioItem, EntityID: row.ID}, nil); err != nil {
				return err
			}
		}
	}
	if pack.Name != "" || len(pack.Keywords.Track) > 0 {
		log.InfoContext(ctx, "imported brand pack", "name", pack.Name)
	}
	return nil
}

// starterSources are inserted only into a workspace with no sources. They
// work for any brand and cost nothing: the Hacker News API and the
// workspace's search backend (SearXNG by default), both driven by the
// tracked keywords.
var starterSources = []struct {
	connector, name, config string
}{
	{"hn", "Hacker News", `{"lists":["newstories","askstories"],"rules":{"no_self_promotion":true,"notes":["Hacker News guidelines: comment on the substance of the story; do not use HN primarily for promotion"]}}`},
	{"search", "Web search", `{"max_queries":5,"max_results":10,"full_text":true,"rules":{"disclose_affiliation":true}}`},
}

// seedCollection adds starter sources and a disabled starter monitor so the
// admin can review them, add feeds, and enable it from Monitors & sources.
func seedCollection(ctx context.Context, q *store.Queries, ws uuid.UUID, log *slog.Logger) error {
	existing, err := q.ListSources(ctx, ws)
	if err != nil || len(existing) > 0 {
		return err
	}
	ids := make([]uuid.UUID, 0, len(starterSources))
	for _, src := range starterSources {
		row, err := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws, Connector: src.connector, Name: src.name, Config: []byte(src.config), Enabled: true})
		if err != nil {
			return fmt.Errorf("source %s: %w", src.name, err)
		}
		ids = append(ids, row.ID)
	}
	if _, err := q.CreateMonitor(ctx, store.CreateMonitorParams{
		WorkspaceID: ws, Name: "Starter monitor", SourceIds: ids, Query: "",
		Lanes: []store.Lane{store.LaneInbound, store.LaneThoughtLeadership}, ScheduleCron: "*/30 * * * *", Enabled: false,
	}); err != nil {
		return fmt.Errorf("starter monitor: %w", err)
	}
	log.InfoContext(ctx, "seeded starter sources and a disabled monitor", "sources", len(ids))
	return nil
}
