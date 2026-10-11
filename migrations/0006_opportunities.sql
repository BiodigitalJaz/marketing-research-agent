-- +goose Up
CREATE TABLE lane_configs (
  workspace_id     uuid REFERENCES workspaces(id) ON DELETE CASCADE,
  lane             lane NOT NULL,
  weights          jsonb NOT NULL,   -- {relevance, intent, recency, reach, fit, deadline}
  daily_quota      int NOT NULL,
  freshness_hours  int NOT NULL,     -- older items are not surfaced
  min_score        numeric(4,3) NOT NULL DEFAULT 0.5,
  draft_kind       text NOT NULL CHECK (draft_kind IN ('reply','pitch','comment')),
  PRIMARY KEY (workspace_id, lane)
);
CREATE TYPE opp_status AS ENUM ('new','saved','acted','dismissed','snoozed','expired');
CREATE TABLE opportunities (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  raw_item_id      uuid NOT NULL REFERENCES raw_items(id) ON DELETE CASCADE,
  lane             lane NOT NULL,
  opp_type         text NOT NULL,   -- recommendation_request | pain_point | journalist_query | podcast_guest | speaking_cfp | guest_post | rfp | competitor_complaint | trending_discussion
  score            numeric(4,3) NOT NULL,
  score_breakdown  jsonb NOT NULL,  -- {relevance:0.8, intent:0.9, ...}
  explanation      text NOT NULL,
  deadline         timestamptz,
  status           opp_status NOT NULL DEFAULT 'new',
  snoozed_until    timestamptz,
  featured_on      date,            -- set by BuildDailyInbox
  assignee_id      uuid REFERENCES users(id),
  cluster_id       uuid,            -- near-duplicate grouping; canonical item = lowest created_at
  model_run_id     uuid REFERENCES model_runs(id),
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (raw_item_id, lane)
);
CREATE INDEX opp_inbox ON opportunities (workspace_id, lane, status, score DESC);
CREATE INDEX opp_deadline ON opportunities (workspace_id, deadline) WHERE deadline IS NOT NULL;
CREATE INDEX opp_cluster ON opportunities (workspace_id, cluster_id) WHERE cluster_id IS NOT NULL;
CREATE TABLE drafts (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  opportunity_id      uuid NOT NULL REFERENCES opportunities(id) ON DELETE CASCADE,
  kind                text NOT NULL CHECK (kind IN ('reply','pitch','comment')),
  variant             text NOT NULL,   -- helpful | story | short | custom
  body                text NOT NULL,
  portfolio_item_ids  uuid[] NOT NULL DEFAULT '{}',
  include_link        boolean NOT NULL DEFAULT false,
  rule_flags          jsonb NOT NULL DEFAULT '[]',  -- [{rule, severity, note}]
  model_run_id        uuid REFERENCES model_runs(id),
  created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX drafts_opp ON drafts (opportunity_id);
CREATE TABLE opportunity_events (
  id              bigserial PRIMARY KEY,
  opportunity_id  uuid NOT NULL REFERENCES opportunities(id) ON DELETE CASCADE,
  user_id         uuid REFERENCES users(id),
  event           text NOT NULL,  -- viewed | saved | dismissed | snoozed | copied_draft | opened_link | acted | outcome
  reason          text,           -- dismissal reason code
  outcome         text,           -- no_response | reply | dm | meeting | lead | client
  meta            jsonb NOT NULL DEFAULT '{}',
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX opp_events_opp ON opportunity_events (opportunity_id, created_at);

-- +goose Down
DROP TABLE opportunity_events;
DROP TABLE drafts;
DROP TABLE opportunities;
DROP TYPE opp_status;
DROP TABLE lane_configs;
