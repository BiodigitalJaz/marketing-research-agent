package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/agent"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

const maxQuestionChars = 4000

var chartRefRe = regexp.MustCompile(`\{\{chart:([A-Za-z0-9_-]+)\}\}`)

type researchJobDTO struct {
	ID         uuid.UUID       `json:"id"`
	Mode       string          `json:"mode"`
	Template   *string         `json:"template"`
	Question   string          `json:"question"`
	Status     string          `json:"status"`
	Progress   json.RawMessage `json:"progress"`
	UseWeb     bool            `json:"use_web"`
	Error      *string         `json:"error"`
	ReportID   *uuid.UUID      `json:"report_id"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	Archived   bool            `json:"archived"`
	CreatedBy  uuid.UUID       `json:"created_by"`
}

func toJobDTO(j store.ResearchJob, report *uuid.UUID) researchJobDTO {
	return researchJobDTO{j.ID, j.Mode, j.Template, j.Question, string(j.Status), orEmptyJSON(j.Progress), j.UseWeb, j.Error, report, j.CreatedAt, j.StartedAt, j.FinishedAt,
		j.ArchivedAt != nil, j.CreatedBy}
}

type stepDTO struct {
	Seq       int32           `json:"seq"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type chatMessageDTO struct {
	ID        int64           `json:"id"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Sources   json.RawMessage `json:"sources"`
	CreatedAt time.Time       `json:"created_at"`
}

type researchInput struct {
	Mode     string  `json:"mode"`
	Question string  `json:"question"`
	Template *string `json:"template"`
	UseWeb   *bool   `json:"use_web"`
}

func (s *server) handleCreateResearch(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var in researchInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := oneOf("mode", in.Mode, "chat", "deep"); err != nil {
		s.fail(c, err)
		return
	}
	if err := reqString("question", &in.Question, maxQuestionChars); err != nil {
		s.fail(c, err)
		return
	}
	if in.Template != nil {
		if *in.Template == "" {
			in.Template = nil
		} else if err := oneOf("template", *in.Template, prompts.Templates...); err != nil {
			s.fail(c, err)
			return
		}
	}
	useWeb := in.UseWeb == nil || *in.UseWeb
	ctx := c.Request.Context()
	var job store.ResearchJob
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		var err error
		job, err = q.CreateResearchJob(ctx, store.CreateResearchJobParams{WorkspaceID: m.WorkspaceID, CreatedBy: u.ID, Mode: in.Mode, Template: in.Template, Question: in.Question, UseWeb: useWeb})
		if err != nil {
			return err
		}
		if in.Mode == "chat" {
			if _, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{WorkspaceID: m.WorkspaceID, JobID: job.ID, Role: "user", Content: in.Question, Sources: []byte(`[]`)}); err != nil {
				return err
			}
		}
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.ResearchArgs{WorkspaceID: m.WorkspaceID, JobID: job.ID}, jobs.ResearchJobOpts(m.WorkspaceID, job.ID)); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "research.create", "research_job", job.ID.String(), map[string]any{"mode": in.Mode, "use_web": useWeb})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, toJobDTO(job, nil))
}

func (s *server) handleListResearch(c *gin.Context) {
	ws := memberOf(c).WorkspaceID
	q := store.New(s.pool)
	rows, err := q.ListResearchJobs(c.Request.Context(), store.ListResearchJobsParams{WorkspaceID: ws, Limit: 50, Archived: c.Query("archived") == "true"})
	if err != nil {
		s.fail(c, err)
		return
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	reports := map[uuid.UUID]uuid.UUID{}
	if len(ids) > 0 {
		rs, err := q.ReportsForJobs(c.Request.Context(), store.ReportsForJobsParams{WorkspaceID: ws, JobIds: ids})
		if err != nil {
			s.fail(c, err)
			return
		}
		for _, r := range rs {
			reports[r.JobID] = r.ID
		}
	}
	out := make([]researchJobDTO, 0, len(rows))
	for _, r := range rows {
		var rep *uuid.UUID
		if id, ok := reports[r.ID]; ok {
			rep = &id
		}
		out = append(out, toJobDTO(r, rep))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "templates": prompts.Templates})
}

func (s *server) handleGetResearch(c *gin.Context) {
	ws := memberOf(c).WorkspaceID
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	job, err := q.GetResearchJob(ctx, store.GetResearchJobParams{WorkspaceID: ws, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	steps, err := q.ListResearchSteps(ctx, store.ListResearchStepsParams{WorkspaceID: ws, JobID: id, AfterSeq: 0})
	if err != nil {
		s.fail(c, err)
		return
	}
	msgs, err := q.ListChatMessages(ctx, store.ListChatMessagesParams{WorkspaceID: ws, JobID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	var report *uuid.UUID
	if rs, err := q.ReportsForJobs(ctx, store.ReportsForJobsParams{WorkspaceID: ws, JobIds: []uuid.UUID{id}}); err == nil && len(rs) > 0 {
		report = &rs[0].ID
	}
	sd := make([]stepDTO, 0, len(steps))
	for _, st := range steps {
		sd = append(sd, stepDTO{st.Seq, st.Kind, st.Payload, st.CreatedAt})
	}
	md := make([]chatMessageDTO, 0, len(msgs))
	for _, m := range msgs {
		md = append(md, chatMessageDTO{m.ID, m.Role, m.Content, orEmptyArray(m.Sources), m.CreatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"job": toJobDTO(job, report), "steps": sd, "messages": md})
}

func (s *server) handleResearchMessage(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := reqString("content", &in.Content, maxQuestionChars); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	var job store.ResearchJob
	err = s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		var err error
		job, err = q.RequeueResearchJob(ctx, store.RequeueResearchJobParams{WorkspaceID: m.WorkspaceID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return &apiError{http.StatusConflict, "busy", "follow-ups work on finished chat conversations only"}
		}
		if err != nil {
			return err
		}
		if _, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{WorkspaceID: m.WorkspaceID, JobID: id, Role: "user", Content: in.Content, Sources: []byte(`[]`)}); err != nil {
			return err
		}
		if _, err := s.jobs.InsertTx(ctx, tx, jobs.ResearchArgs{WorkspaceID: m.WorkspaceID, JobID: id}, jobs.ResearchJobOpts(m.WorkspaceID, id)); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "research.message", "research_job", id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toJobDTO(job, nil))
}

func (s *server) handleCancelResearch(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	job, err := q.CancelResearchJob(ctx, store.CancelResearchJobParams{WorkspaceID: m.WorkspaceID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		s.fail(c, &apiError{http.StatusConflict, "not_running", "only queued or running jobs can be cancelled"})
		return
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	// The worker sees the status within seconds; cancelling the River job
	// also stops one that has not started.
	if res, err := s.jobs.JobList(ctx, river.NewJobListParams().Kinds(jobs.ResearchArgs{}.Kind()).Metadata(string(jobs.ResearchJobMetadata(m.WorkspaceID, id))).States(allJobStates...).First(5)); err == nil {
		for _, j := range res.Jobs {
			if _, err := s.jobs.JobCancel(ctx, j.ID); err != nil {
				s.log.WarnContext(ctx, "cancel river job", "job_id", j.ID, "err", err)
			}
		}
	}
	s.audit(ctx, q, m.WorkspaceID, &u.ID, "research.cancel", "research_job", id.String(), nil)
	c.JSON(http.StatusOK, toJobDTO(job, nil))
}

// handleResearchStream streams steps as server-sent events. Clients resume
// with Last-Event-ID (the last seq they saw); missed steps are replayed
// from the database.
func (s *server) handleResearchStream(c *gin.Context) {
	ws := memberOf(c).WorkspaceID
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	if _, err := q.GetResearchJob(ctx, store.GetResearchJobParams{WorkspaceID: ws, ID: id}); err != nil {
		s.fail(c, err)
		return
	}
	after := 0
	for _, v := range []string{c.GetHeader("Last-Event-ID"), c.Query("after")} {
		if n, err := strconv.Atoi(v); err == nil && n > after {
			after = n
		}
	}
	notify, unsubscribe := s.hub.subscribe(id)
	defer unsubscribe()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform") // no-transform: proxies must not compress (and so buffer) the stream
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = fmt.Fprint(c.Writer, "retry: 3000\n\n")
	c.Writer.Flush()

	send := func() (done bool) {
		steps, err := q.ListResearchSteps(ctx, store.ListResearchStepsParams{WorkspaceID: ws, JobID: id, AfterSeq: int32(after)}) // #nosec G115 -- small
		if err != nil {
			return false
		}
		for _, st := range steps {
			b, _ := json.Marshal(stepDTO{st.Seq, st.Kind, st.Payload, st.CreatedAt})
			_, _ = fmt.Fprintf(c.Writer, "id: %d\nevent: step\ndata: %s\n\n", st.Seq, b)
			after = int(st.Seq)
			if st.Kind == "done" {
				done = true
			}
		}
		c.Writer.Flush()
		return done
	}
	if send() {
		return
	}
	poll := time.NewTicker(5 * time.Second) // in case a notification is missed
	defer poll.Stop()
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-notify:
			if send() {
				return
			}
		case <-poll.C:
			if send() {
				return
			}
		case <-beat.C:
			_, _ = fmt.Fprint(c.Writer, ": ping\n\n")
			c.Writer.Flush()
		}
	}
}

// ---- reports ----

type citationDTO struct {
	Marker    string     `json:"marker"`
	RawItemID *uuid.UUID `json:"raw_item_id"`
	URL       string     `json:"url"`
	Title     *string    `json:"title"`
}

func (s *server) handleListReports(c *gin.Context) {
	rows, err := store.New(s.pool).ListReports(c.Request.Context(), store.ListReportsParams{WorkspaceID: memberOf(c).WorkspaceID, Limit: 100})
	if err != nil {
		s.fail(c, err)
		return
	}
	type row struct {
		ID            uuid.UUID  `json:"id"`
		JobID         *uuid.UUID `json:"job_id"`
		Title         string     `json:"title"`
		Version       int32      `json:"version"`
		Template      *string    `json:"template"`
		CitationCount int32      `json:"citation_count"`
		CreatedAt     time.Time  `json:"created_at"`
	}
	out := make([]row, 0, len(rows))
	for _, r := range rows {
		out = append(out, row{r.ID, r.JobID, r.Title, r.Version, r.Template, r.CitationCount, r.CreatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *server) loadReport(c *gin.Context) (store.Report, []citationDTO, bool) {
	ws := memberOf(c).WorkspaceID
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return store.Report{}, nil, false
	}
	q := store.New(s.pool)
	rep, err := q.GetReport(c.Request.Context(), store.GetReportParams{WorkspaceID: ws, ID: id})
	if err != nil {
		s.fail(c, err)
		return rep, nil, false
	}
	cites, err := q.ListCitations(c.Request.Context(), store.ListCitationsParams{WorkspaceID: ws, ReportID: id})
	if err != nil {
		s.fail(c, err)
		return rep, nil, false
	}
	out := make([]citationDTO, 0, len(cites))
	for _, ct := range cites {
		out = append(out, citationDTO{ct.Marker, ct.RawItemID, ct.Url, ct.Title})
	}
	return rep, out, true
}

func (s *server) handleGetReport(c *gin.Context) {
	rep, cites, ok := s.loadReport(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": rep.ID, "job_id": rep.JobID, "title": rep.Title, "version": rep.Version, "template": rep.Template,
		"markdown": rep.Markdown, "charts": orEmptyArray(rep.Charts), "citations": cites, "created_at": rep.CreatedAt})
}

// handleExportReport downloads the report as Markdown with a sources list.
// Charts are exported as tables of their (tool-produced) data.
func (s *server) handleExportReport(c *gin.Context) {
	if f := c.DefaultQuery("format", "md"); f != "md" {
		s.fail(c, badRequest("format must be md"))
		return
	}
	rep, cites, ok := s.loadReport(c)
	if !ok {
		return
	}
	var charts []agent.Chart
	_ = json.Unmarshal(rep.Charts, &charts)
	md := chartRefRe.ReplaceAllStringFunc(rep.Markdown, func(m string) string {
		id := chartRefRe.FindStringSubmatch(m)[1]
		for _, ch := range charts {
			if ch.ID == id {
				return chartTable(ch)
			}
		}
		return ""
	})
	cited := make([]agent.Cited, 0, len(cites))
	for _, ct := range cites {
		cited = append(cited, agent.Cited{Marker: ct.Marker, Source: agent.Source{URL: ct.URL, Title: derefStr(ct.Title)}})
	}
	body := strings.TrimSpace(md) + "\n\n" + agent.SourceList(cited) + "\n---\nAI-generated research report — review before sharing.\n"
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, rep.Title)
	name = strings.Trim(strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r == '-' }), "-"), "-")
	if name == "" {
		name = "report"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.md"`, clipName(name, 80)))
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(body))
}

func chartTable(ch agent.Chart) string {
	cols := append([]string{}, ch.X)
	cols = append(cols, ch.Y...)
	if ch.Type == "table" || ch.X == "" {
		cols = nil
		if len(ch.Data) > 0 {
			for k := range ch.Data[0] {
				cols = append(cols, k)
			}
			sort.Strings(cols)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n| %s |\n|%s\n", ch.Title, strings.Join(cols, " | "), strings.Repeat(" --- |", len(cols)))
	for _, row := range ch.Data {
		vals := make([]string, len(cols))
		for i, col := range cols {
			vals[i] = strings.ReplaceAll(fmt.Sprint(row[col]), "|", "/")
		}
		fmt.Fprintf(&b, "| %s |\n", strings.Join(vals, " | "))
	}
	return b.String()
}

func clipName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- SSE hub ----

// researchHub fans out research_events notifications (payload "job:seq")
// to stream handlers over one listening connection.
type researchHub struct {
	pool *pgxpool.Pool
	log  interface{ Warn(string, ...any) }
	once sync.Once
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan struct{}]struct{}
}

func newResearchHub(pool *pgxpool.Pool, log interface{ Warn(string, ...any) }) *researchHub {
	return &researchHub{pool: pool, log: log, subs: map[uuid.UUID]map[chan struct{}]struct{}{}}
}

func (h *researchHub) subscribe(job uuid.UUID) (<-chan struct{}, func()) {
	h.once.Do(func() { go h.listen(context.Background()) })
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[job] == nil {
		h.subs[job] = map[chan struct{}]struct{}{}
	}
	h.subs[job][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[job], ch)
		if len(h.subs[job]) == 0 {
			delete(h.subs, job)
		}
		h.mu.Unlock()
	}
}

func (h *researchHub) listen(ctx context.Context) {
	for {
		if err := h.listenOnce(ctx); err != nil && ctx.Err() == nil {
			// Stream handlers fall back to polling while the listener is down.
			if err := h.pool.Ping(ctx); err != nil && strings.Contains(err.Error(), "closed pool") {
				return // shutting down
			}
			h.log.Warn("research event listener stopped; reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// listenOnce uses its own connection, not one borrowed from the pool, so a
// held LISTEN never blocks the pool from closing.
func (h *researchHub) listenOnce(ctx context.Context) error {
	conn, err := pgx.ConnectConfig(ctx, h.pool.Config().ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN research_events"); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		jobStr, _, _ := strings.Cut(n.Payload, ":")
		job, err := uuid.Parse(jobStr)
		if err != nil {
			continue
		}
		h.mu.Lock()
		for ch := range h.subs[job] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		h.mu.Unlock()
	}
}

// handleArchiveResearch hides a research job from the list, or restores it.
func (s *server) handleArchiveResearch(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in struct {
		Archived bool `json:"archived"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	n, err := q.SetResearchArchived(ctx, store.SetResearchArchivedParams{WorkspaceID: m.WorkspaceID, ID: id, Archived: in.Archived})
	if err != nil {
		s.fail(c, err)
		return
	}
	if n == 0 {
		s.fail(c, errNotFound)
		return
	}
	s.audit(ctx, q, m.WorkspaceID, &u.ID, "research.archive", "research_job", id.String(), map[string]any{"archived": in.Archived})
	c.Status(http.StatusNoContent)
}

// handleDeleteResearch deletes a finished research job with its steps and
// chat. Reports it produced are kept. Editors delete their own jobs; admins
// any job.
func (s *server) handleDeleteResearch(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	q := store.New(s.pool)
	job, err := q.GetResearchJob(ctx, store.GetResearchJobParams{WorkspaceID: m.WorkspaceID, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	if job.CreatedBy != u.ID && m.Role != auth.RoleAdmin {
		s.fail(c, &apiError{http.StatusForbidden, "forbidden", "only the person who started this research, or an admin, can delete it"})
		return
	}
	n, err := q.DeleteResearchJob(ctx, store.DeleteResearchJobParams{WorkspaceID: m.WorkspaceID, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	if n == 0 {
		s.fail(c, &apiError{http.StatusConflict, "research_running", "this research is still running; cancel it first"})
		return
	}
	s.audit(ctx, q, m.WorkspaceID, &u.ID, "research.delete", "research_job", id.String(), map[string]any{"question": job.Question})
	c.Status(http.StatusNoContent)
}
