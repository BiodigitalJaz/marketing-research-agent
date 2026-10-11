package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

func TestPingJobRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	log := pgtest.DiscardLogger()

	client, err := jobs.NewWorkerClient(jobs.Deps{Log: log, Pool: pool}, 2)
	if err != nil {
		t.Fatal(err)
	}
	completed, cancel := client.Subscribe(river.EventKindJobCompleted)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = client.Stop(stopCtx)
	})

	// Insert transactionally from a separate insert-only client, as the API will.
	inserter, err := jobs.NewInsertClient(pool, log)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := inserter.InsertTx(ctx, tx, jobs.PingArgs{Note: "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-completed:
		if ev.Job.ID != res.Job.ID {
			t.Fatalf("completed job %d, want %d", ev.Job.ID, res.Job.ID)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ping job did not complete")
	}
}
