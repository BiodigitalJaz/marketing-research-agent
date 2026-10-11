package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/structured"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Draft generation. Drafts are suggestions for a person to copy;
// nothing here posts anywhere.

// Drafting errors that retrying cannot fix.
var (
	ErrBelowMinScore = errors.New("opportunity is below its lane's minimum score")
	ErrUnknownKind   = errors.New("unknown draft kind")
	ErrNoOpportunity = errors.New("opportunity not found")
)

const (
	portfolioCandidates = 3
	MaxInstructionChars = 1000
)

// DraftRequest asks for a fresh batch of drafts for one opportunity.
type DraftRequest struct {
	Workspace   uuid.UUID
	Opportunity uuid.UUID
	Kind        string // empty: the lane's draft_kind
	Instruction string // optional "regenerate with instruction"
	UserID      *uuid.UUID
}

type portfolioCandidate struct {
	ID      uuid.UUID
	Kind    string
	Title   string
	URL     *string
	Summary string
}

type draftOut struct {
	Variant          string   `json:"variant"`
	Subject          *string  `json:"subject"`
	Body             string   `json:"body"`
	PortfolioItemIDs []string `json:"portfolio_item_ids"`
	IncludeLink      bool     `json:"include_link"`
}

type checkOut struct {
	Flags []struct {
		Variant  string `json:"variant"`
		Rule     string `json:"rule"`
		Quote    string `json:"quote"`
		Severity string `json:"severity"`
		Note     string `json:"note"`
	} `json:"flags"`
}

var draftSchemas = func() map[string]*structured.Schema {
	out := map[string]*structured.Schema{}
	for kind, spec := range KindSpecs {
		variants, _ := json.Marshal(spec.Variants)
		subject := `{"type": ["string", "null"]}`
		if spec.Subject {
			subject = `{"type": "string", "minLength": 1, "maxLength": 200}`
		}
		out[kind] = structured.MustCompile("drafts_"+kind, fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["drafts"],
  "properties": {
    "drafts": {
      "type": "array",
      "maxItems": %d,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["variant", "subject", "body", "portfolio_item_ids", "include_link"],
        "properties": {
          "variant": {"type": "string", "enum": %s},
          "subject": %s,
          "body": {"type": "string", "maxLength": 4000},
          "portfolio_item_ids": {"type": "array", "maxItems": %d, "items": {"type": "string"}},
          "include_link": {"type": "boolean"}
        }
      }
    }
  }
}`, len(spec.Variants), variants, subject, portfolioCandidates))
	}
	return out
}()

var checkSchema = structured.MustCompile("draft_check", `{
  "type": "object",
  "additionalProperties": false,
  "required": ["flags"],
  "properties": {
    "flags": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["variant", "rule", "quote", "severity", "note"],
        "properties": {
          "variant": {"type": "string"},
          "rule": {"type": "string", "maxLength": 120},
          "quote": {"type": "string", "maxLength": 300},
          "severity": {"type": "string", "enum": ["warning", "block"]},
          "note": {"type": "string", "maxLength": 400}
        }
      }
    }
  }
}`)

// Draft generates, checks, and stores one batch of drafts.
func (s *Service) Draft(ctx context.Context, req DraftRequest) ([]store.Draft, error) {
	q := store.New(s.Pool)
	ws := req.Workspace
	o, err := q.GetOpportunityForDraft(ctx, store.GetOpportunityForDraftParams{WorkspaceID: ws, ID: req.Opportunity})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoOpportunity
	}
	if err != nil {
		return nil, err
	}
	if score, minimum := numericF(o.Score), numericF(o.MinScore); score < minimum {
		return nil, ErrBelowMinScore
	}
	kind := req.Kind
	if kind == "" {
		kind = o.DraftKind
	}
	spec, ok := KindSpecs[kind]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKind, kind)
	}
	rules, err := sources.RulesFromConfig(o.SourceConfig)
	if err != nil {
		return nil, fmt.Errorf("source rules: %w", err)
	}
	bc, err := LoadBrand(ctx, q, ws)
	if err != nil {
		return nil, err
	}
	candidates, err := s.portfolioFor(ctx, q, ws, o.RawItemID)
	if err != nil {
		return nil, err
	}
	community := deref(o.Community)
	if community == "" {
		community = o.SourceName
	}
	guard := GuardInput{Spec: spec, Rules: rules, BrandName: bc.Name, Banned: BannedPhrases(bc.Tone.Dont)}
	for _, c := range candidates {
		if c.URL != nil && *c.URL != "" {
			guard.AllowedLinks = append(guard.AllowedLinks, *c.URL)
		}
	}

	data := PromptData{BrandName: bc.Name, Brand: bc.Summary, Lane: string(o.Lane), OppType: o.OppType, ExternalContentRule: prompts.ExternalContentRule,
		Kind: kind, MaxWords: spec.MaxWords, LinksAllowed: guard.LinksAllowed(), Community: community, Tone: toneText(bc.Tone)}
	system, promptID, err := promptFor(ctx, q, ws, "draft", data)
	if err != nil {
		return nil, err
	}
	instruction := truncate(strings.TrimSpace(req.Instruction), MaxInstructionChars)
	messages := []llm.Message{llm.UserText(draftMessage(o, community, candidates, instruction))}
	call := router.Call{Workspace: ws, Task: "draft", PromptID: promptID}
	schema := draftSchemas[kind]

	drafts, resp, runID, err := s.generate(ctx, call, system, messages, schema, spec)
	if err != nil {
		return nil, err
	}
	checked := s.enforceAll(guard, drafts, candidates)
	// Style guard: one regeneration for banned phrases or word limits.
	if problems := revisionNotes(checked); len(problems) > 0 {
		retry := append(slices.Clone(messages), resp.AssistantMessage(), llm.UserText("Revise the drafts and return every variant again. Fix these problems:\n- "+strings.Join(problems, "\n- ")))
		if d2, _, run2, err := s.generate(ctx, call, system, retry, schema, spec); err == nil {
			drafts, runID, checked = d2, run2, s.enforceAll(guard, d2, candidates)
		} else {
			s.Log.WarnContext(ctx, "draft revision failed; keeping first drafts", "opportunity_id", o.ID, "err", err)
		}
	}
	s.modelCheck(ctx, q, ws, bc, kind, community, rules, drafts, checked)

	batch := uuid.New()
	var saved []store.Draft
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tq := store.New(tx)
		for i, d := range drafts {
			c := checked[i]
			var subject *string
			if spec.Subject && d.Subject != nil && strings.TrimSpace(*d.Subject) != "" {
				sub := truncate(strings.TrimSpace(*d.Subject), 200)
				subject = &sub
			} else if spec.Subject {
				c.Flags = append(c.Flags, RuleFlag{Rule: "missing_subject", Severity: SevWarning, Note: "The pitch has no subject line; write one before sending.", Source: "style"})
			}
			flags, _ := json.Marshal(nonNilFlags(c.Flags))
			var instr *string
			if instruction != "" {
				instr = &instruction
			}
			row, err := tq.InsertDraft(ctx, store.InsertDraftParams{
				WorkspaceID: ws, OpportunityID: o.ID, BatchID: batch, Kind: kind, Variant: d.Variant, Subject: subject, Body: c.Body,
				PortfolioItemIds: chosenPortfolio(d.PortfolioItemIDs, candidates), IncludeLink: c.IncludeLink, RuleFlags: flags,
				WordCount: int32(WordCount(c.Body)), Instruction: instr, ModelRunID: runID, CreatedBy: req.UserID, // #nosec G115 -- bounded by maxLength
			})
			if err != nil {
				return fmt.Errorf("insert draft: %w", err)
			}
			saved = append(saved, row)
		}
		return nil
	})
	return saved, err
}

// generate calls the model and returns the drafts in the kind's variant
// order (one per variant; unknown or duplicate variants dropped).
func (s *Service) generate(ctx context.Context, call router.Call, system string, messages []llm.Message, schema *structured.Schema, spec KindSpec) ([]draftOut, *llm.ChatResponse, *uuid.UUID, error) {
	resp, attempts, err := s.Router.Chat(ctx, call, llm.ChatRequest{System: system, Messages: messages, JSONSchema: schema.Raw, SchemaName: "drafts"})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("draft: %w", err)
	}
	var out struct {
		Drafts []draftOut `json:"drafts"`
	}
	if err := schema.Decode(resp.Text(), &out); err != nil {
		return nil, nil, nil, err
	}
	var drafts []draftOut
	for _, v := range spec.Variants {
		for _, d := range out.Drafts {
			if d.Variant == v && strings.TrimSpace(d.Body) != "" {
				drafts = append(drafts, d)
				break
			}
		}
	}
	if len(drafts) == 0 {
		return nil, nil, nil, errors.New("draft: the model returned no usable drafts")
	}
	return drafts, resp, lastRun(attempts), nil
}

func (s *Service) enforceAll(g GuardInput, drafts []draftOut, candidates []portfolioCandidate) []Checked {
	out := make([]Checked, len(drafts))
	for i, d := range drafts {
		var urls []string
		for _, id := range chosenPortfolio(d.PortfolioItemIDs, candidates) {
			for _, c := range candidates {
				if c.ID == id && c.URL != nil && *c.URL != "" {
					urls = append(urls, *c.URL)
				}
			}
		}
		out[i] = Enforce(g, d.Variant, d.Body, d.IncludeLink, urls)
	}
	return out
}

func revisionNotes(checked []Checked) []string {
	var out []string
	for _, c := range checked {
		out = append(out, NeedsRevision(c.Flags)...)
	}
	return out
}

// modelCheck asks the model to check the drafts against the rules code
// cannot verify (community notes, the tone guide's guidance) and adds its
// findings to the flags. A failed check is itself flagged, not fatal: the
// hard rules were already enforced.
func (s *Service) modelCheck(ctx context.Context, q *store.Queries, ws uuid.UUID, bc BrandContext, kind, community string, rules sources.Rules, drafts []draftOut, checked []Checked) {
	var list []string
	for _, n := range rules.Notes {
		list = append(list, "Community rule: "+n)
	}
	for _, g := range GuidanceRules(bc.Tone.Dont) {
		list = append(list, "Never: "+g)
	}
	if len(list) == 0 {
		return
	}
	system, promptID, err := promptFor(ctx, q, ws, "draft_check", PromptData{BrandName: bc.Name, ExternalContentRule: prompts.ExternalContentRule,
		Kind: kind, Community: community, Rules: "- " + strings.Join(list, "\n- ")})
	failed := func(err error) {
		s.Log.WarnContext(ctx, "draft rules check failed", "workspace_id", ws, "err", err)
		for i := range checked {
			checked[i].Flags = append(checked[i].Flags, RuleFlag{Rule: "rules_check", Severity: SevWarning, Source: "model",
				Note: "The automated rules check could not run; review this draft against the community rules yourself."})
		}
	}
	if err != nil {
		failed(err)
		return
	}
	var b strings.Builder
	for i, d := range drafts {
		fmt.Fprintf(&b, "Variant %q:\n%s\n\n", d.Variant, checked[i].Body)
	}
	resp, _, err := s.Router.Chat(ctx, router.Call{Workspace: ws, Task: "draft", PromptID: promptID}, llm.ChatRequest{
		System: system, Messages: []llm.Message{llm.UserText(prompts.ExternalContent("drafts", strings.TrimSpace(b.String())))},
		JSONSchema: checkSchema.Raw, SchemaName: "draft_check",
	})
	if err != nil {
		failed(err)
		return
	}
	var out checkOut
	if err := checkSchema.Decode(resp.Text(), &out); err != nil {
		failed(err)
		return
	}
	for _, f := range out.Flags {
		for i, d := range drafts {
			// A finding must quote words that are really in that draft;
			// unverifiable findings (or ones copied across variants) are dropped.
			if d.Variant == f.Variant && QuoteIn(checked[i].Body, f.Quote) {
				checked[i].Flags = append(checked[i].Flags, RuleFlag{Rule: truncate(f.Rule, 120), Severity: f.Severity, Note: truncate(f.Note, 400), Source: "model"})
			}
		}
	}
}

func (s *Service) portfolioFor(ctx context.Context, q *store.Queries, ws, rawItemID uuid.UUID) ([]portfolioCandidate, error) {
	matched, err := q.MatchPortfolioItems(ctx, store.MatchPortfolioItemsParams{WorkspaceID: ws, RawItemID: rawItemID, Lim: portfolioCandidates})
	if err != nil {
		return nil, err
	}
	var out []portfolioCandidate
	for _, m := range matched {
		out = append(out, portfolioCandidate{m.ID, m.Kind, m.Title, m.Url, m.Summary})
	}
	if len(out) > 0 {
		return out, nil
	}
	recent, err := q.RecentPortfolioItems(ctx, store.RecentPortfolioItemsParams{WorkspaceID: ws, Limit: portfolioCandidates})
	if err != nil {
		return nil, err
	}
	for _, r := range recent {
		out = append(out, portfolioCandidate{r.ID, r.Kind, r.Title, r.Url, r.Summary})
	}
	return out, nil
}

// chosenPortfolio keeps the model's picks that are real candidates.
func chosenPortfolio(ids []string, candidates []portfolioCandidate) []uuid.UUID {
	out := []uuid.UUID{}
	for _, s := range ids {
		id, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil || slices.Contains(out, id) {
			continue
		}
		for _, c := range candidates {
			if c.ID == id {
				out = append(out, id)
			}
		}
	}
	return out
}

// draftMessage renders the opportunity for the drafting model. The author
// handle is included so the reply can address the person.
func draftMessage(o store.GetOpportunityForDraftRow, community string, candidates []portfolioCandidate, instruction string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Opportunity: %s / %s\nWhy it was surfaced: %s\n", o.Lane, o.OppType, o.Explanation)
	if o.Deadline != nil {
		fmt.Fprintf(&b, "Deadline: %s\n", o.Deadline.UTC().Format(time.RFC1123))
	}
	fmt.Fprintf(&b, "Community: %s\nSource: %s\nURL: %s\n", community, o.SourceName, o.Url)
	if h := deref(o.AuthorHandle); h != "" {
		fmt.Fprintf(&b, "Author: %s\n", h)
	}
	if o.PublishedAt != nil {
		fmt.Fprintf(&b, "Published: %s\n", o.PublishedAt.UTC().Format(time.RFC3339))
	}
	b.WriteString("\n")
	content := deref(o.Title) + "\n\n" + o.Body
	b.WriteString(prompts.ExternalContent(o.SourceName, truncate(strings.TrimSpace(content), maxItemPromptRunes)))
	b.WriteString("\n\nCandidate portfolio pieces:\n")
	if len(candidates) == 0 {
		b.WriteString("(none)\n")
	}
	for _, c := range candidates {
		url := "no public URL"
		if c.URL != nil && *c.URL != "" {
			url = *c.URL
		}
		fmt.Fprintf(&b, "- id: %s\n  %s: %s (%s)\n  %s\n", c.ID, c.Kind, c.Title, url, truncate(c.Summary, 400))
	}
	if instruction != "" {
		fmt.Fprintf(&b, "\nInstruction from the reviewer: %s\n", instruction)
	}
	return b.String()
}

func toneText(t brand.Tone) string {
	var b strings.Builder
	if t.Voice != "" {
		fmt.Fprintf(&b, "Voice: %s\n", t.Voice)
	}
	for _, d := range t.Do {
		fmt.Fprintf(&b, "Do: %s\n", d)
	}
	for _, d := range t.Dont {
		fmt.Fprintf(&b, "Don't: %s\n", d)
	}
	for _, e := range t.Examples {
		fmt.Fprintf(&b, "Example of the voice: %s\n", e)
	}
	if b.Len() == 0 {
		return "Plain, specific, and honest."
	}
	return strings.TrimSpace(b.String())
}

func nonNilFlags(f []RuleFlag) []RuleFlag {
	if f == nil {
		return []RuleFlag{}
	}
	return f
}

func numericF(n pgtype.Numeric) float64 {
	f, _ := n.Float64Value()
	return f.Float64
}
