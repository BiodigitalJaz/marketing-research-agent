package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// ---- sources ----

type sourceDTO struct {
	ID        uuid.UUID       `json:"id"`
	Connector string          `json:"connector"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	Enabled   bool            `json:"enabled"`
	Health    string          `json:"health"`
	LastError *string         `json:"last_error"`
	LastRunAt *time.Time      `json:"last_run_at"`
	ItemCount int32           `json:"item_count"`
}

func toSourceDTO(s store.Source) sourceDTO {
	return sourceDTO{s.ID, s.Connector, s.Name, orEmptyJSON(s.Config), s.Enabled, s.Health, s.LastError, s.LastRunAt, s.ItemCount}
}

func (s *server) handleListSources(c *gin.Context) {
	rows, err := store.New(s.pool).ListSources(c.Request.Context(), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]sourceDTO, 0, len(rows))
	for _, r := range rows {
		if r.Connector == "research_fetch" {
			continue // internal source for research citations (M6)
		}
		out = append(out, toSourceDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "connectors": sources.Connectors()})
}

type sourceInput struct {
	Connector string          `json:"connector"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	Enabled   *bool           `json:"enabled"`
}

func (in *sourceInput) validate() error {
	if err := reqString("name", &in.Name, 120); err != nil {
		return err
	}
	if !slices.Contains(sources.Connectors(), in.Connector) {
		return badRequest("connector must be one of: " + strings.Join(sources.Connectors(), ", "))
	}
	if len(in.Config) == 0 || string(in.Config) == "null" {
		in.Config = json.RawMessage(`{}`)
	}
	conn, err := sources.Get(in.Connector, sources.Deps{})
	if err != nil {
		return badRequest(err.Error())
	}
	if err := conn.ValidateConfig(in.Config); err != nil {
		return badRequest(err.Error())
	}
	return nil
}

func (s *server) handleSaveSource(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var id uuid.UUID
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in sourceInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if !creating {
		existing, err := store.New(s.pool).GetSource(ctx, store.GetSourceParams{WorkspaceID: m.WorkspaceID, ID: id})
		if err != nil {
			s.fail(c, err)
			return
		}
		in.Connector = existing.Connector // connector type is fixed after creation
	}
	if err := in.validate(); err != nil {
		s.fail(c, err)
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	var saved store.Source
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		var err error
		if creating {
			saved, err = q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: m.WorkspaceID, Connector: in.Connector, Name: in.Name, Config: in.Config, Enabled: enabled})
		} else {
			saved, err = q.UpdateSource(ctx, store.UpdateSourceParams{WorkspaceID: m.WorkspaceID, ID: id, Name: in.Name, Config: in.Config, Enabled: enabled})
		}
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "source.save", "source", saved.ID.String(), map[string]any{"connector": in.Connector, "name": in.Name})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, toSourceDTO(saved))
}

func (s *server) handleDeleteSource(c *gin.Context) {
	s.deleteSimple(c, "source", func(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (int64, error) {
		if err := q.RemoveSourceFromMonitors(ctx, store.RemoveSourceFromMonitorsParams{WorkspaceID: ws, SourceID: id}); err != nil {
			return 0, err
		}
		return q.DeleteSource(ctx, store.DeleteSourceParams{WorkspaceID: ws, ID: id})
	})
}

type itemPreview struct {
	ExternalID  string         `json:"external_id"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Snippet     string         `json:"snippet"`
	Community   string         `json:"community"`
	PublishedAt *time.Time     `json:"published_at"`
	Metrics     map[string]any `json:"metrics"`
}

// handleTestSource fetches once and returns sample items. Nothing is saved
// and the source's cursor is not advanced.
func (s *server) handleTestSource(c *gin.Context) {
	m := memberOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	src, err := store.New(s.pool).GetSource(ctx, store.GetSourceParams{WorkspaceID: m.WorkspaceID, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	items, terr := s.collect.Test(ctx, m.WorkspaceID, src)
	out := make([]itemPreview, 0, len(items))
	for _, it := range items {
		p := itemPreview{ExternalID: it.ExternalID, URL: it.URL, Title: it.Title, Snippet: truncateRunes(it.Body, 300), Community: it.Community, Metrics: it.Metrics}
		if !it.PublishedAt.IsZero() {
			t := it.PublishedAt
			p.PublishedAt = &t
		}
		out = append(out, p)
	}
	res := gin.H{"ok": terr == nil, "items": out}
	if terr != nil {
		res["error"] = terr.Error()
	}
	c.JSON(http.StatusOK, res)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---- monitors ----

type monitorDTO struct {
	ID           uuid.UUID   `json:"id"`
	Name         string      `json:"name"`
	SourceIDs    []uuid.UUID `json:"source_ids"`
	Query        string      `json:"query"`
	Lanes        []string    `json:"lanes"`
	ScheduleCron string      `json:"schedule_cron"`
	Enabled      bool        `json:"enabled"`
	LastRunAt    *time.Time  `json:"last_run_at"`
	NextRunAt    *time.Time  `json:"next_run_at"`
	LastError    *string     `json:"last_error"`
	LastNewItems int32       `json:"last_new_items"`
}

func toMonitorDTO(m store.Monitor) monitorDTO {
	lanes := make([]string, 0, len(m.Lanes))
	for _, l := range m.Lanes {
		lanes = append(lanes, string(l))
	}
	ids := m.SourceIds
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return monitorDTO{m.ID, m.Name, ids, m.Query, lanes, m.ScheduleCron, m.Enabled, m.LastRunAt, m.NextRunAt, m.LastError, m.LastNewItems}
}

func (s *server) handleListMonitors(c *gin.Context) {
	rows, err := store.New(s.pool).ListMonitors(c.Request.Context(), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]monitorDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toMonitorDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

type monitorInput struct {
	Name         string      `json:"name"`
	SourceIDs    []uuid.UUID `json:"source_ids"`
	Query        string      `json:"query"`
	Lanes        []string    `json:"lanes"`
	ScheduleCron string      `json:"schedule_cron"`
	Enabled      *bool       `json:"enabled"`
}

func (s *server) handleSaveMonitor(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var id uuid.UUID
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in monitorInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := reqString("name", &in.Name, 120); err != nil {
		s.fail(c, err)
		return
	}
	in.Query = strings.TrimSpace(in.Query)
	if len(in.Query) > 500 {
		s.fail(c, badRequest("query is too long"))
		return
	}
	if len(in.SourceIDs) == 0 || len(in.SourceIDs) > 50 {
		s.fail(c, badRequest("choose 1-50 sources"))
		return
	}
	if in.ScheduleCron == "" {
		in.ScheduleCron = "*/30 * * * *"
	}
	if _, err := jobs.NextRun(in.ScheduleCron, time.Now()); err != nil {
		s.fail(c, badRequest(err.Error()))
		return
	}
	if len(in.Lanes) == 0 {
		in.Lanes = lanes
	}
	laneVals := make([]store.Lane, 0, len(in.Lanes))
	for _, l := range in.Lanes {
		if err := oneOf("lanes", l, lanes...); err != nil {
			s.fail(c, err)
			return
		}
		laneVals = append(laneVals, store.Lane(l))
	}
	enabled := in.Enabled == nil || *in.Enabled
	// An enabled monitor is due immediately; the scheduler then follows its cron.
	var next *time.Time
	if enabled {
		now := time.Now()
		next = &now
	}
	ctx := c.Request.Context()
	var saved store.Monitor
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		srcs, err := q.GetSourcesByIDs(ctx, store.GetSourcesByIDsParams{WorkspaceID: m.WorkspaceID, Ids: in.SourceIDs})
		if err != nil {
			return err
		}
		// Keep only sources that exist in this workspace: ids of deleted
		// sources (or another workspace's) are dropped, not saved.
		in.SourceIDs = in.SourceIDs[:0]
		for _, src := range srcs {
			in.SourceIDs = append(in.SourceIDs, src.ID)
		}
		if len(in.SourceIDs) == 0 {
			return badRequest("choose at least one existing source")
		}
		if creating {
			saved, err = q.CreateMonitor(ctx, store.CreateMonitorParams{WorkspaceID: m.WorkspaceID, Name: in.Name, SourceIds: in.SourceIDs,
				Query: in.Query, Lanes: laneVals, ScheduleCron: in.ScheduleCron, Enabled: enabled, NextRunAt: next})
		} else {
			saved, err = q.UpdateMonitor(ctx, store.UpdateMonitorParams{WorkspaceID: m.WorkspaceID, ID: id, Name: in.Name, SourceIds: in.SourceIDs,
				Query: in.Query, Lanes: laneVals, ScheduleCron: in.ScheduleCron, Enabled: enabled, NextRunAt: next})
		}
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "monitor.save", "monitor", saved.ID.String(), map[string]any{"name": in.Name, "enabled": enabled})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, toMonitorDTO(saved))
}

func (s *server) handleDeleteMonitor(c *gin.Context) {
	s.deleteSimple(c, "monitor", func(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (int64, error) {
		return q.DeleteMonitor(ctx, store.DeleteMonitorParams{WorkspaceID: ws, ID: id})
	})
}

// handleRunMonitor enqueues a collection run now.
func (s *server) handleRunMonitor(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	var jobID int64
	err = s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		mon, err := q.GetMonitor(ctx, store.GetMonitorParams{WorkspaceID: m.WorkspaceID, ID: id})
		if err != nil {
			return err
		}
		res, err := s.jobs.InsertTx(ctx, tx, jobs.CollectMonitorArgs{WorkspaceID: m.WorkspaceID, MonitorID: id}, nil)
		if err != nil {
			return err
		}
		// This run counts as the scheduled one: an enabled monitor that was
		// due now (just turned on) would otherwise start a second run moments
		// later, and its progress would hide this one's.
		if mon.Enabled {
			if next, err := jobs.NextRun(mon.ScheduleCron, time.Now()); err == nil {
				if err := q.SetMonitorNextRun(ctx, store.SetMonitorNextRunParams{WorkspaceID: m.WorkspaceID, ID: id, NextRunAt: &next}); err != nil {
					return err
				}
			}
		}
		jobID = res.Job.ID
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "monitor.run", "monitor", id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID})
}

// ---- collected items ----

type rawItemDTO struct {
	ID           uuid.UUID       `json:"id"`
	SourceID     uuid.UUID       `json:"source_id"`
	SourceName   string          `json:"source_name"`
	Connector    string          `json:"connector"`
	MonitorID    *uuid.UUID      `json:"monitor_id"`
	URL          string          `json:"url"`
	Title        *string         `json:"title"`
	Snippet      string          `json:"snippet"`
	Community    *string         `json:"community"`
	AuthorHandle *string         `json:"author_handle"`
	PublishedAt  *time.Time      `json:"published_at"`
	FetchedAt    time.Time       `json:"fetched_at"`
	Metrics      json.RawMessage `json:"metrics"`
}

func (s *server) handleListItems(c *gin.Context) {
	m := memberOf(c)
	p := store.ListRawItemsParams{WorkspaceID: m.WorkspaceID, Limit: 100}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 && n <= 500 {
			p.Limit = int32(n)
		}
	}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{{"source_id", &p.SourceID}, {"monitor_id", &p.MonitorID}} {
		if v := c.Query(f.key); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				s.fail(c, badRequest(f.key+" must be a UUID"))
				return
			}
			*f.dst = &id
		}
	}
	q := store.New(s.pool)
	rows, err := q.ListRawItems(c.Request.Context(), p)
	if err != nil {
		s.fail(c, err)
		return
	}
	total, _ := q.CountRawItems(c.Request.Context(), m.WorkspaceID)
	out := make([]rawItemDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, rawItemDTO{r.ID, r.SourceID, r.SourceName, r.Connector, r.MonitorID, r.Url, r.Title, r.Snippet, r.Community,
			r.AuthorHandle, r.PublishedAt, r.FetchedAt, orEmptyJSON(r.Metrics)})
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": total})
}
