package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/examples"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/seed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// setupLockKey serializes setup claims (pg_advisory_xact_lock).
const setupLockKey = 7_301_120_811

// PrepareSetup readies first-run setup when no user exists: it stores the
// hash of a one-time setup token and logs where to use it. The token is
// envToken when set (it is not logged), else a new random one that is
// logged; each API start replaces the previous token, so with several API
// replicas the most recently started one's log has the valid token.
func PrepareSetup(ctx context.Context, pool *pgxpool.Pool, envToken, baseURL string, log *slog.Logger) error {
	q := store.New(pool)
	n, err := q.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	tok := envToken
	if tok == "" {
		if tok, err = auth.NewToken(); err != nil {
			return err
		}
	}
	if err := q.SetSetupToken(ctx, auth.SessionID(tok)); err != nil {
		return err
	}
	url := strings.TrimRight(baseURL, "/") + "/setup"
	if envToken != "" {
		log.Warn("first-run setup is open: no users exist yet. Open the setup page and enter the token from "+config.EnvPrefix+"SETUP_TOKEN", "url", url)
	} else {
		log.Warn("first-run setup is open: no users exist yet. Open the setup page and enter this one-time token", "url", url, "setup_token", tok)
	}
	return nil
}

type setupStatusDTO struct {
	NeedsSetup        bool     `json:"needs_setup"`
	EncryptionEnabled bool     `json:"encryption_enabled"`
	ExampleBrands     []string `json:"example_brands"`
}

func (s *server) handleSetupStatus(c *gin.Context) {
	n, err := store.New(s.pool).CountUsers(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, setupStatusDTO{NeedsSetup: n == 0, EncryptionEnabled: s.box.Enabled(), ExampleBrands: examples.BrandPacks()})
}

type setupRequest struct {
	Token        string `json:"token"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Password     string `json:"password"`
	Workspace    string `json:"workspace"`
	ExampleBrand string `json:"example_brand"`
}

// handleSetup claims a fresh install: it creates the admin, the workspace,
// and its defaults in one transaction, then signs the admin in. It works
// only while no user exists and only with the setup token.
func (s *server) handleSetup(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLoginBody)
	var req setupRequest
	if err := bindJSON(c, &req); err != nil {
		s.fail(c, err)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Name, req.Workspace = strings.TrimSpace(req.Name), strings.TrimSpace(req.Workspace)
	switch {
	case req.Token == "":
		s.fail(c, badRequest("the setup token is required"))
		return
	case req.Email == "" || !strings.Contains(req.Email, "@"):
		s.fail(c, badRequest("a valid email is required"))
		return
	case len(req.Password) < auth.MinPasswordLen:
		s.fail(c, badRequest("the password must be at least 12 characters"))
		return
	case req.Workspace == "":
		s.fail(c, badRequest("a workspace name is required"))
		return
	}
	var pack *brand.Pack
	if req.ExampleBrand != "" {
		p, err := examples.BrandPack(req.ExampleBrand)
		if err != nil {
			s.fail(c, badRequest(err.Error()))
			return
		}
		pack = p
	}
	if !s.limiter.allow("setup") {
		writeError(c, http.StatusTooManyRequests, "rate_limited", "too many setup attempts; try again shortly")
		return
	}
	if s.cfg == nil {
		s.fail(c, errors.New("setup is unavailable: the server has no config"))
		return
	}

	ctx := c.Request.Context()
	var res seed.Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(setupLockKey)); err != nil {
			return err
		}
		q := store.New(tx)
		if n, err := q.CountUsers(ctx); err != nil {
			return err
		} else if n > 0 {
			return &apiError{http.StatusConflict, "already_set_up", "this install is already set up; sign in instead"}
		}
		hash, err := q.GetSetupTokenHash(ctx)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !auth.TokensEqual(hash, auth.SessionID(req.Token))) {
			return &apiError{http.StatusUnauthorized, "invalid_setup_token", "the setup token is not valid; copy it from the most recent api log"}
		}
		if err != nil {
			return err
		}
		res, err = seed.Apply(ctx, tx, s.jobs, s.cfg, seed.Options{
			Admin:         seed.Admin{Email: req.Email, Name: req.Name, Password: req.Password},
			WorkspaceName: req.Workspace,
			Brand:         pack,
		}, s.log)
		if err != nil {
			return err
		}
		s.audit(ctx, q, res.WorkspaceID, &res.User.ID, "setup", "workspace", res.WorkspaceID.String(), map[string]any{"example_brand": req.ExampleBrand})
		return q.DeleteSetupToken(ctx)
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	s.log.InfoContext(ctx, "first-run setup complete", "workspace_id", res.WorkspaceID, "user_id", res.User.ID)
	s.startSession(c, store.New(s.pool), res.User)
}

// startSession creates a session for u, sets the cookies, and writes the
// session response (the same shape as login).
func (s *server) startSession(c *gin.Context, q *store.Queries, u store.User) {
	ctx := c.Request.Context()
	tok, err := auth.NewToken()
	if err != nil {
		s.fail(c, err)
		return
	}
	csrf, err := auth.NewToken()
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := q.CreateSession(ctx, store.CreateSessionParams{
		ID: auth.SessionID(tok), UserID: u.ID, ExpiresAt: time.Now().Add(auth.SessionTTL),
	}); err != nil {
		s.fail(c, err)
		return
	}
	ws, err := q.ListWorkspacesForUser(ctx, u.ID)
	if err != nil {
		s.fail(c, err)
		return
	}
	for _, w := range ws {
		s.audit(ctx, q, w.ID, &u.ID, "login", "user", u.ID.String(), nil)
	}
	maxAge := int(auth.SessionTTL.Seconds())
	s.setCookie(c, s.sessionCookie(), tok, maxAge, true)
	s.setCookie(c, s.csrfCookie(), csrf, maxAge, false)
	c.JSON(http.StatusOK, sessionDTO{User: userDTO{u.ID, u.Email, u.Name}, Workspaces: toWorkspaceDTOs(ws), CSRFToken: csrf})
}

// checklistItem is one "Getting started" step. Admin marks steps only an
// admin can complete (Settings, sources).
type checklistItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Done   bool   `json:"done"`
	Href   string `json:"href"`
	Admin  bool   `json:"admin"`
}

// handleChecklist reports how far the workspace is from a working setup.
func (s *server) handleChecklist(c *gin.Context) {
	ctx := c.Request.Context()
	ws := memberOf(c).WorkspaceID
	q := store.New(s.pool)
	items, err := s.checklist(ctx, q, ws)
	if err != nil {
		s.fail(c, err)
		return
	}
	done := 0
	for _, it := range items {
		if it.Done {
			done++
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "done": done, "total": len(items)})
}

func (s *server) checklist(ctx context.Context, q *store.Queries, ws uuid.UUID) ([]checklistItem, error) {
	// Each task's route must resolve, and one of its models (primary or
	// fallback) must have answered at least once, in any task or the
	// playground. A route can look usable (a keyless local provider, say)
	// while nothing serves its models, so a call to another model doesn't
	// count.
	var unusable, unproven []string
	for _, task := range config.KnownTasks {
		res, err := s.router.Resolve(ctx, ws, task)
		if err != nil || len(res.Targets) == 0 {
			unusable = append(unusable, task)
			continue
		}
		answered := false
		var models []string
		for _, t := range res.Targets {
			models = append(models, t.ProviderID+"/"+t.Model)
			ok, err := q.HasSuccessfulRunFor(ctx, store.HasSuccessfulRunForParams{WorkspaceID: ws, ProviderID: t.ProviderID, Model: t.Model})
			if err != nil {
				return nil, err
			}
			answered = answered || ok
		}
		if !answered {
			unproven = append(unproven, task+" ("+strings.Join(models, " or ")+")")
		}
	}
	llm := checklistItem{ID: "llm", Title: "Connect a language model", Done: len(unusable) == 0 && len(unproven) == 0, Href: "/settings?tab=routes", Admin: true,
		Detail: "Every task (classify, score, draft, research, summarize) routes to a model that has answered."}
	switch {
	case len(unusable) > 0:
		llm.Detail = "Add a provider and its key (or a local server's URL), then pick models for: " + strings.Join(unusable, ", ") + "."
		llm.Href = "/settings?tab=providers"
	case len(unproven) > 0:
		llm.Detail = "No model on these routes has answered yet: " + strings.Join(unproven, "; ") + ". Point the routes at models your server offers (Settings → Routes), then send a Playground message to each."
	}

	embeddings := checklistItem{ID: "embeddings", Title: "Set up embeddings", Href: "/settings?tab=workspace", Admin: true,
		Detail: "Powers duplicate detection, brand matching, and semantic search. Optional, but recommended."}
	if _, err := s.router.Embedder(ctx, ws); err == nil {
		embedded, err := q.HasEmbeddings(ctx, ws)
		if err != nil {
			return nil, err
		}
		embeddings.Done = embedded
		if !embedded {
			embeddings.Detail = "An embeddings model is configured, but nothing has been embedded yet. Brand entries and collected items are embedded by the worker; if this stays open, check the model's URL and the worker's log."
		}
	}
	wsSettings, err := s.loadWorkspaceSettings(ctx, q, ws)
	if err != nil {
		return nil, err
	}

	var profile brand.Profile
	if row, err := q.GetBrandProfile(ctx, ws); err == nil {
		_ = json.Unmarshal(row.Profile, &profile)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	keywords, err := q.CountTrackedKeywords(ctx, ws)
	if err != nil {
		return nil, err
	}
	monitors, err := q.CountEnabledMonitors(ctx, ws)
	if err != nil {
		return nil, err
	}

	return []checklistItem{
		llm,
		embeddings,
		{ID: "search", Title: "Choose a web search backend", Detail: "The bundled SearXNG is free; Brave and Google Programmable Search are optional.", Done: wsSettings.SearchKeySet, Href: "/settings?tab=workspace", Admin: true},
		{ID: "brand", Title: "Describe your brand", Detail: "What you offer, your tone, who you're for, and the claims you never make. Scoring and drafts depend on it.", Done: len(profile.Services) > 0, Href: "/brand"},
		{ID: "keywords", Title: "Add keywords to track", Detail: "Monitors and web search look for these. Add negative keywords to drop noise.", Done: keywords > 0, Href: "/brand?tab=keywords"},
		{ID: "monitor", Title: "Turn on a monitor", Detail: "Attach sources (Google Alerts, RSS, Hacker News, web search) and enable the monitor to start collecting.", Done: monitors > 0, Href: "/sources", Admin: true},
	}, nil
}
