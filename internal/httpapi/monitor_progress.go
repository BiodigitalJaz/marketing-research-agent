package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Monitor run phases, in order.
const (
	phaseIdle       = "idle"       // never run
	phaseQueued     = "queued"     // a collect job is waiting
	phaseCollecting = "collecting" // fetching sources
	phaseScoring    = "scoring"    // collected items still being processed
	phaseDone       = "done"       // every collected item processed
)

type monitorProgressDTO struct {
	Phase         string     `json:"phase"`
	SourcesDone   int        `json:"sources_done"`
	SourcesTotal  int        `json:"sources_total"`
	Collected     int        `json:"collected"`
	Processed     int        `json:"processed"`
	Opportunities int        `json:"opportunities"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	NextRunAt     *time.Time `json:"next_run_at"`
	LastError     *string    `json:"last_error"`
}

// handleMonitorProgress reports the current or latest run of a monitor:
// collecting (sources done of total), then scoring (items processed of
// collected, opportunities so far), then done.
func (s *server) handleMonitorProgress(c *gin.Context) {
	ctx := c.Request.Context()
	ws := memberOf(c).WorkspaceID
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	q := store.New(s.pool)
	mon, err := q.GetMonitor(ctx, store.GetMonitorParams{WorkspaceID: ws, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	d := monitorProgressDTO{Phase: phaseIdle, SourcesDone: int(mon.RunSourcesDone), SourcesTotal: int(mon.RunSourcesTotal),
		StartedAt: mon.RunStartedAt, FinishedAt: mon.LastRunAt, NextRunAt: mon.NextRunAt, LastError: mon.LastError}

	running := mon.RunStartedAt != nil && (mon.LastRunAt == nil || mon.RunStartedAt.After(*mon.LastRunAt))
	if mon.RunStartedAt != nil {
		until := time.Now()
		if !running && mon.LastRunAt != nil {
			until = *mon.LastRunAt
		}
		items, err := q.MonitorRunItems(ctx, store.MonitorRunItemsParams{WorkspaceID: ws, SourceIds: mon.SourceIds, Since: *mon.RunStartedAt, Until: until})
		if err != nil {
			s.fail(c, err)
			return
		}
		d.Collected, d.Processed, d.Opportunities = int(items.Collected), int(items.Processed), int(items.Opportunities)
		switch {
		case running:
			d.Phase = phaseCollecting
		case d.Processed < d.Collected:
			d.Phase = phaseScoring
		default:
			d.Phase = phaseDone
		}
	}
	if !running && s.jobs != nil {
		res, err := s.jobs.JobList(ctx, river.NewJobListParams().Kinds(jobs.CollectMonitorArgs{}.Kind()).
			States(rivertype.JobStateAvailable, rivertype.JobStateScheduled, rivertype.JobStateRetryable, rivertype.JobStateRunning).
			Where("args->>'monitor_id' = @id", river.NamedArgs{"id": id.String()}).First(1))
		if err != nil {
			s.fail(c, err)
			return
		}
		if len(res.Jobs) > 0 {
			d.Phase = phaseQueued
		}
	}
	c.JSON(http.StatusOK, d)
}
