// Package pipeline turns a collected raw item into scored opportunities:
// pre-filter, dedupe, classify, score, insert.
package pipeline

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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/scoring"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/match"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Filter reasons recorded on raw_items.filter_reason.
const (
	ReasonNegative      = "negative_keyword"
	ReasonStale         = "stale"
	ReasonTooShort      = "too_short"
	ReasonDuplicate     = "duplicate"
	ReasonNearDuplicate = "near_duplicate"
	ReasonNotRelevant   = "not_relevant"
	ReasonBelowMin      = "below_min_score"
)

const (
	minBodyChars       = 40
	nearDupThreshold   = 0.92
	maxItemPromptRunes = 6000
	maxEmbedRunes      = 4000
)

// Chatter is the router surface the pipeline needs (tests use fakes).
type Chatter interface {
	Chat(ctx context.Context, call router.Call, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error)
}

// Service processes items. It is safe for concurrent use.
type Service struct {
	Pool     *pgxpool.Pool
	Router   Chatter
	Embedder func(ctx context.Context, ws uuid.UUID) (embed.Embedder, error)
	Log      *slog.Logger
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Result summarizes one item's processing.
type Result struct {
	Opportunities int
	Reason        string // filter reason when no opportunity was created
}

type laneConfig struct {
	weights        map[string]float64
	quota          int32
	freshnessHours int32
	minScore       float64
}

// Process runs the pipeline for one raw item. It is idempotent: a processed
// item is skipped. LLM errors are returned so the job retries.
func (s *Service) Process(ctx context.Context, ws, rawItemID uuid.UUID) (*Result, error) {
	q := store.New(s.Pool)
	item, err := q.GetRawItemForProcessing(ctx, store.GetRawItemForProcessingParams{WorkspaceID: ws, ID: rawItemID})
	if errors.Is(err, pgx.ErrNoRows) {
		return &Result{Reason: "deleted"}, nil
	}
	if err != nil {
		return nil, err
	}
	if item.ProcessedAt != nil {
		return &Result{Reason: "already_processed"}, nil
	}
	lanes, err := s.laneConfigs(ctx, q, ws)
	if err != nil {
		return nil, err
	}
	title := deref(item.Title)
	text := strings.TrimSpace(title + "\n" + item.Body)
	published := item.FetchedAt
	if item.PublishedAt != nil {
		published = *item.PublishedAt
	}
	age := s.now().Sub(published)

	// 9.1 Pre-filter (no LLM).
	negatives, err := s.negativeKeywords(ctx, q, ws)
	if err != nil {
		return nil, err
	}
	var maxFresh int32
	for _, lc := range lanes {
		maxFresh = max(maxFresh, lc.freshnessHours)
	}
	switch {
	case len(negatives) > 0 && !match.AnyOf(nil, negatives).Match(negativeScope(title, item.Body)):
		return s.drop(ctx, q, ws, rawItemID, ReasonNegative, nil)
	case maxFresh > 0 && age > time.Duration(maxFresh)*time.Hour:
		return s.drop(ctx, q, ws, rawItemID, ReasonStale, nil)
	case len([]rune(text)) < minBodyChars:
		return s.drop(ctx, q, ws, rawItemID, ReasonTooShort, nil)
	}

	// 9.2 Dedupe: exact content first, then embedding similarity.
	if cluster, err := q.FindClusterByContentHash(ctx, store.FindClusterByContentHashParams{WorkspaceID: ws, ContentHash: item.ContentHash, ID: rawItemID}); err == nil {
		return s.drop(ctx, q, ws, rawItemID, ReasonDuplicate, &cluster)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	vec, model, dims := s.embedItem(ctx, q, ws, rawItemID, text)
	if vec != nil {
		near, err := q.NearestRawItem(ctx, store.NearestRawItemParams{Vec: *vec, WorkspaceID: ws, Model: model, Dims: dims, ExcludeID: rawItemID})
		if err == nil && near.Similarity >= nearDupThreshold {
			cluster := near.ClusterID
			return s.drop(ctx, q, ws, rawItemID, ReasonNearDuplicate, &cluster)
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	cluster := uuid.New()

	bc, err := LoadBrand(ctx, q, ws)
	if err != nil {
		return nil, err
	}
	excluded := excludedVenue(bc.Targeting, deref(item.Community), item.SourceName, item.Connector)
	allowed := allowedLanes(item.Lanes, excluded)
	if len(allowed) == 0 {
		return s.drop(ctx, q, ws, rawItemID, ReasonNotRelevant, &cluster)
	}
	userMsg := itemMessage(item, excluded)

	// 9.3 Classify.
	cls, _, err := s.classify(ctx, q, ws, bc, allowed, userMsg)
	if err != nil {
		return nil, err
	}

	// 9.4 Score each candidate lane.
	var maxCos *float64
	if vec != nil {
		if sim, err := q.MaxBrandSimilarity(ctx, store.MaxBrandSimilarityParams{Vec: *vec, WorkspaceID: ws, Model: model, Dims: dims}); err == nil && sim >= 0 {
			maxCos = &sim
		}
	}
	reach, err := s.reach(ctx, q, ws, item)
	if err != nil {
		return nil, err
	}
	created := 0
	seen := map[string]bool{}
	for _, c := range cls.Candidates {
		if seen[c.Lane] || !slices.Contains(allowed, c.Lane) {
			continue
		}
		seen[c.Lane] = true
		lc, ok := lanes[c.Lane]
		if !ok {
			continue
		}
		sc, runID, err := s.score(ctx, q, ws, bc, c.Lane, c.OppType, c.Rationale, userMsg)
		if err != nil {
			return nil, err
		}
		deadline := parseDeadline(c.Deadline)
		f := scoring.Factors{
			scoring.Relevance: scoring.RelevanceScore(maxCos, sc.Relevance),
			scoring.Intent:    sc.Intent,
			scoring.Fit:       sc.Fit,
			scoring.Recency:   scoring.RecencyScore(age, int(lc.freshnessHours)),
			scoring.Reach:     reach,
		}
		if c.Lane == "media" {
			f[scoring.Deadline] = scoring.DeadlineScore(deadline, s.now())
		}
		final := scoring.Final(lc.weights, f)
		if final < lc.minScore {
			continue
		}
		breakdown, _ := json.Marshal(map[string]any{"factors": f, "flags": scoreFlags(sc.Flags, excluded), "llm_relevance": sc.Relevance, "embedding_similarity": maxCos})
		var score pgtype.Numeric
		_ = score.Scan(fmt.Sprintf("%.3f", final))
		if _, err := q.InsertOpportunity(ctx, store.InsertOpportunityParams{
			WorkspaceID: ws, RawItemID: rawItemID, Lane: store.Lane(c.Lane), OppType: c.OppType, Score: score,
			ScoreBreakdown: breakdown, Explanation: strings.TrimSpace(sc.Explanation), Deadline: deadline, ClusterID: &cluster, ModelRunID: runID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("insert opportunity: %w", err)
		}
		created++
	}
	reason := ""
	if created == 0 {
		reason = ReasonBelowMin
		if len(cls.Candidates) == 0 {
			reason = ReasonNotRelevant
		}
	}
	var r *string
	if reason != "" {
		r = &reason
	}
	if err := q.MarkRawItemProcessed(ctx, store.MarkRawItemProcessedParams{WorkspaceID: ws, ID: rawItemID, FilterReason: r, ClusterID: &cluster}); err != nil {
		return nil, err
	}
	return &Result{Opportunities: created, Reason: reason}, nil
}

func (s *Service) drop(ctx context.Context, q *store.Queries, ws, id uuid.UUID, reason string, cluster *uuid.UUID) (*Result, error) {
	if err := q.MarkRawItemProcessed(ctx, store.MarkRawItemProcessedParams{WorkspaceID: ws, ID: id, FilterReason: &reason, ClusterID: cluster}); err != nil {
		return nil, err
	}
	return &Result{Reason: reason}, nil
}

func (s *Service) laneConfigs(ctx context.Context, q *store.Queries, ws uuid.UUID) (map[string]laneConfig, error) {
	rows, err := q.ListLaneConfigs(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := map[string]laneConfig{}
	for _, r := range rows {
		lc := laneConfig{quota: r.DailyQuota, freshnessHours: r.FreshnessHours}
		if err := json.Unmarshal(r.Weights, &lc.weights); err != nil {
			return nil, fmt.Errorf("lane %s weights: %w", r.Lane, err)
		}
		if f, err := r.MinScore.Float64Value(); err == nil {
			lc.minScore = f.Float64
		}
		out[string(r.Lane)] = lc
	}
	if len(out) == 0 {
		return nil, errors.New("no lane configs: run the seed")
	}
	return out, nil
}

func (s *Service) negativeKeywords(ctx context.Context, q *store.Queries, ws uuid.UUID) ([]string, error) {
	rows, err := q.ListKeywords(ctx, ws)
	if err != nil {
		return nil, err
	}
	var neg []string
	for _, r := range rows {
		if r.Kind == "negative" {
			neg = append(neg, r.Term)
		}
	}
	return neg, nil
}

// embedItem stores the item's embedding. Author handles are never embedded.
// Embedding is best-effort: without an embedder the pipeline still scores
// (relevance falls back to the LLM's judgment, near-dupes are not merged).
func (s *Service) embedItem(ctx context.Context, q *store.Queries, ws, id uuid.UUID, text string) (*pgvector.Vector, string, int32) {
	if s.Embedder == nil {
		return nil, "", 0
	}
	e, err := s.Embedder(ctx, ws)
	if err != nil {
		if !errors.Is(err, embed.ErrNotConfigured) {
			s.Log.WarnContext(ctx, "embedder unavailable; skipping similarity", "workspace_id", ws, "err", err)
		}
		return nil, "", 0
	}
	vecs, err := e.Embed(ctx, []string{truncate(text, maxEmbedRunes)})
	if err != nil {
		s.Log.WarnContext(ctx, "embedding failed; skipping similarity", "workspace_id", ws, "err", err)
		return nil, "", 0
	}
	v := pgvector.NewVector(vecs[0])
	dims := int32(e.Dims()) // #nosec G115 -- small
	if err := q.UpsertEmbedding(ctx, store.UpsertEmbeddingParams{EntityType: "raw_item", EntityID: id, Model: e.Model(), Dims: dims, Embedding: v, WorkspaceID: ws}); err != nil {
		s.Log.WarnContext(ctx, "store item embedding", "err", err)
	}
	return &v, e.Model(), dims
}

func (s *Service) reach(ctx context.Context, q *store.Queries, ws uuid.UUID, item store.GetRawItemForProcessingRow) (float64, error) {
	var m map[string]any
	_ = json.Unmarshal(item.Metrics, &m)
	v, ok := m["score"].(float64)
	if !ok || item.Community == nil {
		return scoring.ReachScore(nil, 0), nil
	}
	p90, err := q.CommunityScoreP90(ctx, store.CommunityScoreP90Params{WorkspaceID: ws, Community: item.Community})
	if err != nil {
		return 0, err
	}
	return scoring.ReachScore(&v, p90), nil
}

// promptFor loads and renders the active prompt for a task.
func promptFor(ctx context.Context, q *store.Queries, ws uuid.UUID, task string, data PromptData) (string, *uuid.UUID, error) {
	return prompts.Active(ctx, q, ws, task, data)
}

// PromptData is the data every pipeline prompt renders against.
type PromptData = prompts.Data

func (s *Service) classify(ctx context.Context, q *store.Queries, ws uuid.UUID, bc BrandContext, allowed []string, userMsg string) (*classifyOut, *uuid.UUID, error) {
	system, promptID, err := promptFor(ctx, q, ws, "classify", PromptData{BrandName: bc.Name, Brand: bc.Summary, ExternalContentRule: prompts.ExternalContentRule})
	if err != nil {
		return nil, nil, err
	}
	msg := fmt.Sprintf("Allowed lanes for this item: %s\n\n%s", strings.Join(allowed, ", "), userMsg)
	resp, attempts, err := s.Router.Chat(ctx, router.Call{Workspace: ws, Task: "classify", PromptID: promptID}, llm.ChatRequest{
		System: system, Messages: []llm.Message{llm.UserText(msg)}, JSONSchema: classifySchema.Raw, SchemaName: "classify",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("classify: %w", err)
	}
	var out classifyOut
	if err := classifySchema.Decode(resp.Text(), &out); err != nil {
		return nil, nil, err
	}
	return &out, lastRun(attempts), nil
}

func (s *Service) score(ctx context.Context, q *store.Queries, ws uuid.UUID, bc BrandContext, lane, oppType, rationale, userMsg string) (*scoreOut, *uuid.UUID, error) {
	system, promptID, err := promptFor(ctx, q, ws, "score", PromptData{BrandName: bc.Name, Brand: bc.Summary, Lane: lane, OppType: oppType, ExternalContentRule: prompts.ExternalContentRule})
	if err != nil {
		return nil, nil, err
	}
	msg := fmt.Sprintf("Classifier rationale: %s\n\n%s", rationale, userMsg)
	resp, attempts, err := s.Router.Chat(ctx, router.Call{Workspace: ws, Task: "score", PromptID: promptID}, llm.ChatRequest{
		System: system, Messages: []llm.Message{llm.UserText(msg)}, JSONSchema: scoreSchema.Raw, SchemaName: "score",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("score: %w", err)
	}
	var out scoreOut
	if err := scoreSchema.Decode(resp.Text(), &out); err != nil {
		return nil, nil, err
	}
	return &out, lastRun(attempts), nil
}

func lastRun(attempts []router.Attempt) *uuid.UUID {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Err == nil && attempts[i].RunID != uuid.Nil {
			id := attempts[i].RunID
			return &id
		}
	}
	return nil
}

// itemMessage renders the item for the model. Author handles are omitted:
// they are not needed for classification or scoring.
func itemMessage(item store.GetRawItemForProcessingRow, excluded bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Source: %s", item.SourceName)
	if c := deref(item.Community); c != "" {
		fmt.Fprintf(&b, " (%s)", c)
	}
	if excluded {
		b.WriteString(" [a venue where the brand does not post]")
	}
	if item.PublishedAt != nil {
		fmt.Fprintf(&b, "\nPublished: %s", item.PublishedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "\nURL: %s\n\n", item.Url)
	content := deref(item.Title) + "\n\n" + item.Body
	b.WriteString(prompts.ExternalContent(item.SourceName, truncate(strings.TrimSpace(content), maxItemPromptRunes)))
	return b.String()
}

func allowedLanes(lanes []store.Lane, excluded bool) []string {
	var out []string
	for _, l := range lanes {
		if excluded && l == store.LaneInbound {
			continue
		}
		out = append(out, string(l))
	}
	return out
}

func parseDeadline(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, *s); err == nil {
			return &t
		}
	}
	return nil
}

// negativeChars is how much of an item's body negative keywords are
// checked against, after the title.
const negativeChars = 600

// negativeScope is the text negative keywords apply to: the title and the
// opening of the body. A fetched article that mentions an excluded topic in
// passing (e.g. a negative keyword mentioned once in an unrelated story) is still scored; one
// that is about it says so up front.
func negativeScope(title, body string) string {
	return title + "\n" + truncate(strings.TrimSpace(body), negativeChars)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// scoreFlags returns the scorer's flags with excluded_venue decided by the
// brand's venue list rather than the model, which cannot know it reliably.
// Legacy flag names are renamed.
func scoreFlags(llm []string, excluded bool) []string {
	out := []string{}
	for _, f := range llm {
		if f == legacyFlagRestrictedClaim {
			f = FlagRestrictedClaim
		}
		if f != FlagExcludedVenue && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if excluded {
		out = append(out, FlagExcludedVenue)
	}
	return out
}
