// Package collect runs monitors: it resolves the workspace's search
// backend, builds keyword matchers, runs each source's connector, inserts
// new items (deduplicated), enqueues processing in the same transaction,
// and records source health, cursors, and paid-search usage.
package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/match"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"

	// Register connectors.
	_ "github.com/BiodigitalJaz/marketing-research-agent/internal/sources/hn"
	_ "github.com/BiodigitalJaz/marketing-research-agent/internal/sources/rss"
	_ "github.com/BiodigitalJaz/marketing-research-agent/internal/sources/search"
)

// Enqueue schedules processing of a new raw item inside tx.
type Enqueue func(ctx context.Context, tx pgx.Tx, ws, rawItemID uuid.UUID) error

// Service is safe for concurrent use.
type Service struct {
	Pool          *pgxpool.Pool
	Fetch         *fetch.Fetcher
	Secrets       *secrets.Box
	Env           func(string) string
	Log           *slog.Logger
	Enqueue       Enqueue
	RetentionDays int
	// FirstRunWindow bounds how far back a monitor's first run looks.
	FirstRunWindow time.Duration
	Now            func() time.Time
	// NewBackend builds a search backend from settings; tests override it.
	NewBackend func(ctx context.Context, s search.Settings) (search.Backend, error)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SourceRun summarizes one source within a monitor run.
type SourceRun struct {
	SourceID uuid.UUID `json:"source_id"`
	Name     string    `json:"name"`
	Fetched  int       `json:"fetched"`
	New      int       `json:"new"`
	Error    string    `json:"error,omitempty"`
}

// Summary is a monitor run's outcome.
type Summary struct {
	MonitorID uuid.UUID   `json:"monitor_id"`
	New       int         `json:"new"`
	Sources   []SourceRun `json:"sources"`
}

// ErrBudget means the monthly paid-search budget is used up.
var ErrBudget = errors.New("monthly search budget reached")

// Run collects one monitor.
func (s *Service) Run(ctx context.Context, ws, monitorID uuid.UUID) (*Summary, error) {
	q := store.New(s.Pool)
	mon, err := q.GetMonitor(ctx, store.GetMonitorParams{WorkspaceID: ws, ID: monitorID})
	if err != nil {
		return nil, fmt.Errorf("monitor: %w", err)
	}
	srcs, err := q.GetSourcesByIDs(ctx, store.GetSourcesByIDsParams{WorkspaceID: ws, Ids: mon.SourceIds})
	if err != nil {
		return nil, err
	}
	kw, err := s.keywords(ctx, q, ws, mon.Lanes)
	if err != nil {
		return nil, err
	}
	base := s.query(mon.Query, kw, time.Time{}) // Since is set per source

	sum := &Summary{MonitorID: mon.ID}
	var errs []string
	enabled := 0
	for _, src := range srcs {
		if src.Enabled {
			enabled++
		}
	}
	// Progress for the UI; failures here never stop collection.
	if err := q.StartMonitorRun(ctx, store.StartMonitorRunParams{WorkspaceID: ws, ID: mon.ID, RunSourcesTotal: int32(enabled)}); err != nil { // #nosec G115 -- small counts
		s.Log.WarnContext(ctx, "record monitor run start", "monitor_id", mon.ID, "err", err)
	}
	for _, src := range srcs {
		if !src.Enabled {
			continue
		}
		q := base
		q.Since = s.sinceFor(src)
		run := s.runSource(ctx, ws, &mon.ID, src, q)
		if err := store.New(s.Pool).MonitorSourceDone(ctx, store.MonitorSourceDoneParams{WorkspaceID: ws, ID: mon.ID}); err != nil {
			s.Log.WarnContext(ctx, "record monitor progress", "monitor_id", mon.ID, "err", err)
		}
		sum.New += run.New
		sum.Sources = append(sum.Sources, run)
		if run.Error != "" {
			errs = append(errs, src.Name+": "+run.Error)
		}
	}
	var lastErr *string
	if len(errs) > 0 {
		e := strings.Join(errs, "; ")
		lastErr = &e
	}
	if err := q.RecordMonitorRun(ctx, store.RecordMonitorRunParams{WorkspaceID: ws, ID: mon.ID, LastError: lastErr, LastNewItems: int32(sum.New)}); err != nil { // #nosec G115 -- small counts
		return sum, err
	}
	s.Log.InfoContext(ctx, "monitor run", "workspace_id", ws, "monitor_id", mon.ID, "new_items", sum.New, "errors", len(errs))
	return sum, nil
}

// sinceFor is the source's own cutoff: its last run (with overlap; duplicates
// are dropped on insert), or the first-run window if it has never run. It is
// per source so a source added to an existing monitor still backfills.
func (s *Service) sinceFor(src store.Source) time.Time {
	if src.LastRunAt != nil {
		return src.LastRunAt.Add(-15 * time.Minute)
	}
	return s.now().Add(-s.firstRunWindow())
}

func (s *Service) firstRunWindow() time.Duration {
	if s.FirstRunWindow > 0 {
		return s.FirstRunWindow
	}
	return 7 * 24 * time.Hour
}

type keywordSet struct{ track, negative []string }

func (s *Service) keywords(ctx context.Context, q *store.Queries, ws uuid.UUID, lanes []store.Lane) (keywordSet, error) {
	rows, err := q.ListKeywords(ctx, ws)
	if err != nil {
		return keywordSet{}, err
	}
	var k keywordSet
	for _, r := range rows {
		if r.Lane != nil && len(lanes) > 0 && !slices.Contains(lanes, *r.Lane) {
			continue
		}
		if r.Kind == "negative" {
			k.negative = append(k.negative, r.Term)
		} else {
			k.track = append(k.track, r.Term)
		}
	}
	return k, nil
}

// query builds the connector query: the monitor query if set, otherwise the
// tracked keywords; negative keywords always excluded.
func (s *Service) query(monitorQuery string, kw keywordSet, since time.Time) sources.Query {
	q := sources.Query{Since: since, Exclude: match.AnyOf(nil, kw.negative)}
	if strings.TrimSpace(monitorQuery) != "" {
		q.Match = match.Compile(monitorQuery).WithExclusions(kw.negative)
		q.Terms = match.Terms(monitorQuery)
	} else {
		q.Match = match.AnyOf(kw.track, kw.negative)
		for _, t := range kw.track {
			if strings.Contains(t, " ") {
				t = `"` + t + `"`
			}
			q.Terms = append(q.Terms, t)
		}
	}
	return q
}

func (s *Service) runSource(ctx context.Context, ws uuid.UUID, monitorID *uuid.UUID, src store.Source, base sources.Query) SourceRun {
	run := SourceRun{SourceID: src.ID, Name: src.Name}
	q := store.New(s.Pool)
	res, err := s.fetchSource(ctx, ws, src, base)
	if err != nil && len(res.Items) == 0 {
		run.Error = err.Error()
		msg := run.Error
		if rerr := q.RecordSourceRun(ctx, store.RecordSourceRunParams{WorkspaceID: ws, ID: src.ID, Health: "failing", LastError: &msg, State: orEmpty(src.State), ItemCount: 0}); rerr != nil {
			s.Log.ErrorContext(ctx, "record source run", "err", rerr)
		}
		return run
	}
	run.Fetched = len(res.Items)
	expires := s.now().AddDate(0, 0, s.RetentionDays)
	txErr := s.inTx(ctx, func(tx pgx.Tx, tq *store.Queries) error {
		for _, it := range res.Items {
			id, err := tq.InsertRawItem(ctx, rawItemParams(ws, src.ID, monitorID, it, expires))
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already collected
			}
			if err != nil {
				return fmt.Errorf("insert item: %w", err)
			}
			run.New++
			if s.Enqueue != nil {
				if err := s.Enqueue(ctx, tx, ws, id); err != nil {
					return err
				}
			}
		}
		health, lastErr := "ok", (*string)(nil)
		if err != nil { // partial result
			health = "degraded"
			msg := err.Error()
			lastErr = &msg
			run.Error = msg
		}
		state := res.State
		if len(state) == 0 {
			state = orEmpty(src.State)
		}
		return tq.RecordSourceRun(ctx, store.RecordSourceRunParams{WorkspaceID: ws, ID: src.ID, Health: health, LastError: lastErr, State: state, ItemCount: int32(run.New)}) // #nosec G115 -- small counts
	})
	if txErr != nil {
		run.Error, run.New = txErr.Error(), 0
	}
	return run
}

func (s *Service) fetchSource(ctx context.Context, ws uuid.UUID, src store.Source, base sources.Query) (sources.Result, error) {
	conn, err := sources.Get(src.Connector, sources.Deps{Fetch: s.Fetch, Now: s.now})
	if err != nil {
		return sources.Result{}, err
	}
	q := base
	q.Config = src.Config
	q.State = orEmpty(src.State)
	if src.Connector == "search" {
		backend, settings, err := s.Backend(ctx, ws)
		if err != nil {
			return sources.Result{}, err
		}
		q.Search = backend
		q.Budget = s.budget(ws, backend.Name(), settings.MonthlyBudget)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return conn.Fetch(ctx, q)
}

// Test runs a source once without writing anything: no items, no cursor,
// no health update. Paid search queries still count against the budget.
func (s *Service) Test(ctx context.Context, ws uuid.UUID, src store.Source) ([]sources.Item, error) {
	q := store.New(s.Pool)
	kw, err := s.keywords(ctx, q, ws, nil)
	if err != nil {
		return nil, err
	}
	base := s.query("", kw, s.now().Add(-s.firstRunWindow()))
	src.State = nil // never advance a cursor from a test
	res, err := s.fetchSource(ctx, ws, src, base)
	if len(res.Items) > 10 {
		res.Items = res.Items[:10]
	}
	return res.Items, err
}

// Backend resolves the workspace's search backend from Settings.
func (s *Service) Backend(ctx context.Context, ws uuid.UUID) (search.Backend, search.Settings, error) {
	var st search.Settings
	row, err := store.New(s.Pool).GetWorkspaceSettings(ctx, ws)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, st, err
	}
	if err == nil {
		_ = json.Unmarshal(orEmpty(row.Search), &st)
	}
	if s.NewBackend != nil {
		b, err := s.NewBackend(ctx, st)
		return b, st, err
	}
	b, err := s.buildBackend(ctx, ws, st)
	return b, st, err
}

// Search runs one query on the workspace's search backend, counting paid
// queries against the monthly budget (research's web_search tool).
func (s *Service) Search(ctx context.Context, ws uuid.UUID, q search.Query) ([]search.Result, error) {
	backend, settings, err := s.Backend(ctx, ws)
	if err != nil {
		return nil, err
	}
	if backend.Paid() {
		if err := s.budget(ws, backend.Name(), settings.MonthlyBudget)(ctx); err != nil {
			return nil, err
		}
	}
	if q.Lang == "" {
		q.Lang = settings.SearchLang
	}
	if q.Country == "" {
		q.Country = settings.Country
	}
	if q.Safesearch == "" {
		q.Safesearch = settings.Safesearch
	}
	return backend.Search(ctx, q)
}

func (s *Service) buildBackend(ctx context.Context, ws uuid.UUID, st search.Settings) (search.Backend, error) {
	switch st.Backend {
	case search.SearXNG:
		if st.SearXNGURL == "" {
			return nil, errors.New("SearXNG URL is not set (Settings → Mode, embeddings & search)")
		}
		return search.NewSearXNG(st.SearXNGURL, st.SearXNGCategories, s.Fetch), nil
	case search.GooglePSE:
		key, err := s.secret(ctx, ws, st.GooglePSEKeySecretID, st.GooglePSEKeyEnv)
		if err != nil {
			return nil, err
		}
		return search.NewGooglePSE(key, st.GooglePSECX, s.Fetch), nil
	case search.Brave:
		key, err := s.secret(ctx, ws, st.APIKeySecretID, st.APIKeyEnv)
		if err != nil {
			return nil, err
		}
		return search.NewBrave(key, s.Fetch), nil
	case "":
		return nil, errors.New("no search backend selected (Settings → Mode, embeddings & search)")
	}
	return nil, fmt.Errorf("unknown search backend %q", st.Backend)
}

func (s *Service) secret(ctx context.Context, ws uuid.UUID, id *uuid.UUID, env string) (string, error) {
	if id != nil {
		sec, err := store.New(s.Pool).GetSecretAnyScope(ctx, store.GetSecretAnyScopeParams{ID: *id, WorkspaceID: ws})
		if err != nil {
			return "", fmt.Errorf("search key: %w", err)
		}
		return s.Secrets.Open(secrets.Sealed{Ciphertext: sec.Ciphertext, Nonce: sec.Nonce, KeyID: sec.KeyID}, sec.ID, sec.WorkspaceID)
	}
	if env != "" && s.Env != nil {
		return s.Env(env), nil
	}
	return "", nil
}

func (s *Service) budget(ws uuid.UUID, backend string, limit int) func(context.Context) error {
	return func(ctx context.Context) error {
		q := store.New(s.Pool)
		if limit > 0 {
			used, err := q.GetSearchUsage(ctx, store.GetSearchUsageParams{WorkspaceID: ws, Backend: backend})
			if err != nil {
				return err
			}
			if int(used) >= limit {
				return fmt.Errorf("%w (%d/%d queries on %s)", ErrBudget, used, limit, backend)
			}
		}
		_, err := q.IncrementSearchUsage(ctx, store.IncrementSearchUsageParams{WorkspaceID: ws, Backend: backend})
		return err
	}
}

func (s *Service) inTx(ctx context.Context, fn func(pgx.Tx, *store.Queries) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx, store.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func rawItemParams(ws, source uuid.UUID, monitor *uuid.UUID, it sources.Item, expires time.Time) store.InsertRawItemParams {
	metrics, _ := json.Marshal(it.Metrics)
	p := store.InsertRawItemParams{
		WorkspaceID: ws, SourceID: source, MonitorID: monitor, ExternalID: it.ExternalID, Url: it.URL,
		Body: it.Body, Metrics: metrics, ContentHash: sources.ContentHash(it), ExpiresAt: &expires,
	}
	if it.AuthorHandle != "" {
		p.AuthorHandle = &it.AuthorHandle
	}
	if it.Title != "" {
		p.Title = &it.Title
	}
	if it.Community != "" {
		p.Community = &it.Community
	}
	if !it.PublishedAt.IsZero() {
		t := it.PublishedAt
		p.PublishedAt = &t
	}
	return p
}

func orEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}
