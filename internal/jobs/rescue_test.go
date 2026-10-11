package jobs_test

import (
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
)

// River rescues a still-running job after RescueStuckJobsAfter, so it must
// outlast every worker's own timeout or live jobs get run twice.
func TestRescueOutlastsWorkerTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{"collect_monitor", (&jobs.CollectMonitorWorker{}).Timeout(&river.Job[jobs.CollectMonitorArgs]{})},
		{"process_item", (&jobs.ProcessItemWorker{}).Timeout(&river.Job[jobs.ProcessItemArgs]{})},
		{"generate_drafts", (&jobs.GenerateDraftsWorker{}).Timeout(&river.Job[jobs.GenerateDraftsArgs]{})},
		{"research", (&jobs.ResearchWorker{}).Timeout(&river.Job[jobs.ResearchArgs]{})},
		{"sync_knowledge", (&jobs.SyncKnowledgeWorker{}).Timeout(&river.Job[jobs.SyncKnowledgeArgs]{})},
		{"build_memory_bank", (&jobs.BuildMemoryBankWorker{}).Timeout(&river.Job[jobs.BuildMemoryBankArgs]{})},
	}
	for _, tt := range tests {
		if tt.timeout >= jobs.RescueStuckJobsAfter {
			t.Errorf("%s timeout %v >= RescueStuckJobsAfter %v", tt.name, tt.timeout, jobs.RescueStuckJobsAfter)
		}
	}
}
