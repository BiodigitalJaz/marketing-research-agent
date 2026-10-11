package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// BuildDailyInboxArgs features each lane's best new opportunities for
// today, up to the lane's daily quota. It runs
// hourly; before the inbox hour (local time) it does nothing. Re-running is
// safe: it only tops up to the quota.
type BuildDailyInboxArgs struct{}

func (BuildDailyInboxArgs) Kind() string { return "build_daily_inbox" }

type BuildDailyInboxWorker struct {
	river.WorkerDefaults[BuildDailyInboxArgs]
	Deps
}

// InboxHour is the local hour after which the day's inbox is built.
const InboxHour = 6

// InboxDay returns today's date in loc, and whether the inbox should be built yet.
func InboxDay(now time.Time, loc *time.Location) (pgtype.Date, bool) {
	local := now.In(loc)
	d := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	return pgtype.Date{Time: d, Valid: true}, local.Hour() >= InboxHour
}

func (w *BuildDailyInboxWorker) Work(ctx context.Context, _ *river.Job[BuildDailyInboxArgs]) error {
	loc := w.Location
	if loc == nil {
		loc = time.UTC
	}
	today, ready := InboxDay(time.Now(), loc)
	if !ready {
		return nil
	}
	q := store.New(w.Pool)
	wss, err := q.ListWorkspaceIDs(ctx)
	if err != nil {
		return err
	}
	for _, ws := range wss {
		var drafts int
		err := pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
			if err := FeatureToday(ctx, store.New(tx), ws, today); err != nil {
				return err
			}
			var err error
			drafts, err = enqueueEagerDrafts(ctx, tx, ws, today)
			return err
		})
		if err != nil {
			return err
		}
		if drafts > 0 {
			w.Log.InfoContext(ctx, "queued drafts for featured opportunities", "workspace_id", ws, "count", drafts)
		}
	}
	return nil
}

// FeatureToday tops up today's featured set in every lane of a workspace.
func FeatureToday(ctx context.Context, q *store.Queries, ws uuid.UUID, today pgtype.Date) error {
	lanes, err := q.ListLaneConfigs(ctx, ws)
	if err != nil {
		return err
	}
	for _, lc := range lanes {
		if _, err := q.FeatureTopOpportunities(ctx, store.FeatureTopOpportunitiesParams{
			WorkspaceID: ws, Lane: lc.Lane, Today: today, FreshnessHours: lc.FreshnessHours, Quota: lc.DailyQuota,
		}); err != nil {
			return fmt.Errorf("feature %s: %w", lc.Lane, err)
		}
	}
	return nil
}

// ExpireOpportunitiesArgs expires stale or past-deadline opportunities and
// returns snoozed ones whose snooze has ended (hourly).
type ExpireOpportunitiesArgs struct{}

func (ExpireOpportunitiesArgs) Kind() string { return "expire_opportunities" }

type ExpireOpportunitiesWorker struct {
	river.WorkerDefaults[ExpireOpportunitiesArgs]
	Deps
}

func (w *ExpireOpportunitiesWorker) Work(ctx context.Context, _ *river.Job[ExpireOpportunitiesArgs]) error {
	q := store.New(w.Pool)
	wss, err := q.ListWorkspaceIDs(ctx)
	if err != nil {
		return err
	}
	for _, ws := range wss {
		expired, err := q.ExpireOpportunities(ctx, ws)
		if err != nil {
			return err
		}
		woke, err := q.UnsnoozeOpportunities(ctx, ws)
		if err != nil {
			return err
		}
		if expired+woke > 0 {
			w.Log.InfoContext(ctx, "opportunity maintenance", "workspace_id", ws, "expired", expired, "unsnoozed", woke)
		}
	}
	return nil
}
