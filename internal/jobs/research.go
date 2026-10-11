package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/research"
)

// QueueResearch runs research jobs apart from the scoring pipeline, so a
// long report never holds up scoring.
const QueueResearch = "research"

// ResearchArgs runs one research job (a chat turn or a deep report).
type ResearchArgs struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	JobID       uuid.UUID `json:"job_id"`
}

func (ResearchArgs) Kind() string { return "research" }

// ResearchJobOpts: research is expensive, so a failed run is reported to
// the user rather than retried automatically. Metadata lets the API find
// the job to cancel it.
func ResearchJobOpts(ws, job uuid.UUID) *river.InsertOpts {
	return &river.InsertOpts{Queue: QueueResearch, MaxAttempts: 1, Metadata: ResearchJobMetadata(ws, job)}
}

// ResearchJobMetadata identifies a research job's River jobs.
func ResearchJobMetadata(ws, job uuid.UUID) []byte {
	return []byte(`{"workspace_id":"` + ws.String() + `","research_job_id":"` + job.String() + `"}`)
}

type ResearchWorker struct {
	river.WorkerDefaults[ResearchArgs]
	Deps
}

func (w *ResearchWorker) Timeout(*river.Job[ResearchArgs]) time.Duration { return research.JobTimeout }

func (w *ResearchWorker) Work(ctx context.Context, job *river.Job[ResearchArgs]) error {
	if w.Research == nil {
		return river.JobCancel(errors.New("research is not configured"))
	}
	return w.Research.Run(ctx, job.Args.WorkspaceID, job.Args.JobID)
}
