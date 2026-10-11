// Package research runs research jobs: chat
// turns and deep research (plan, gather, verify, synthesize) on the agent
// loop, with progress streamed as research_steps.
package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/agent"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/knowledge"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/structured"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/mcp"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Default limits; routes can override them in Settings (max_steps,
// timeout_s, token_budget).
const (
	ChatMaxSteps   = 14 // thorough models search several angles before answering
	DeepMaxSteps   = 40
	ChatTimeout    = 8 * time.Minute
	DeepTimeout    = 15 * time.Minute
	MaxTimeout     = 30 * time.Minute // hard cap; the job's own timeout is a little longer
	ChatTokenLimit = 150_000
	DeepTokenLimit = 600_000
	// synthesisGrace is the time after the gather deadline for verifying
	// claims and writing the report. Gathering can use its whole limit, and a
	// local model may need several minutes to write a long report.
	synthesisGrace = 10 * time.Minute
	// verifyShare caps claim verification's share of the time left after
	// gathering, so writing the report always keeps most of it.
	verifyShare = 0.4
)

// JobTimeout bounds a research job's run, including the final synthesis.
const JobTimeout = MaxTimeout + synthesisGrace + time.Minute

// Step kinds besides the agent loop's.
const (
	StepPlan    = "plan"
	StepNote    = "note"
	StepVerify  = "verify"
	StepAnswer  = "answer"
	StepReport  = "report"
	StepWarning = "warning"
	StepError   = "error"
	StepDone    = "done"
	StepPhase   = "phase"
)

// Router is the routing surface research needs.
type Router interface {
	agent.Chatter
	Resolve(ctx context.Context, ws uuid.UUID, task string) (*router.Resolved, error)
}

// Fetcher is the polite fetcher surface research needs.
type Fetcher interface {
	Article(ctx context.Context, rawURL string) (*fetch.Page, error)
	Image(ctx context.Context, rawURL string) ([]byte, string, error)
}

// Service runs research jobs. It is safe for concurrent use.
type Service struct {
	Pool          *pgxpool.Pool
	Router        Router
	Embedder      func(ctx context.Context, ws uuid.UUID) (embed.Embedder, error)
	Search        func(ctx context.Context, ws uuid.UUID, q search.Query) ([]search.Result, error)
	Fetch         Fetcher
	MCP           *mcp.Loader // nil: no MCP tools
	Log           *slog.Logger
	RetentionDays int
	// PollInterval is how often a running job checks for cancellation.
	PollInterval time.Duration
}

// run is one job execution.
type run struct {
	s        *Service
	job      store.ResearchJob
	ws       uuid.UUID
	nb       *agent.Notebook
	tools    *agent.Registry
	resolved *router.Resolved
	params   router.Params
	budget   *agent.Budget
	deadline time.Time
	brand    pipeline.BrandContext
	stepMu   sync.Mutex
	progress map[string]any
}

// Run executes a queued job. It returns an error only for infrastructure
// failures worth logging; job failures are recorded on the job.
func (s *Service) Run(ctx context.Context, ws, jobID uuid.UUID) error {
	q := store.New(s.Pool)
	job, err := q.StartResearchJob(ctx, store.StartResearchJobParams{WorkspaceID: ws, ID: jobID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // cancelled or already handled
	}
	if err != nil {
		return err
	}
	r := &run{s: s, job: job, ws: ws, nb: agent.NewNotebook(), progress: map[string]any{}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.watchCancel(ctx, cancel)

	runErr := r.execute(ctx)
	status, msg := store.JobStatusSucceeded, (*string)(nil)
	if runErr != nil {
		status = store.JobStatusFailed
		m := runErr.Error()
		if errors.Is(runErr, agent.ErrCancelled) || errors.Is(runErr, context.Canceled) {
			m = "Cancelled."
		}
		msg = &m
	}
	// A cancelled job stays cancelled (FinishResearchJob only updates running jobs).
	bg := context.WithoutCancel(ctx)
	if err := q.FinishResearchJob(bg, store.FinishResearchJobParams{WorkspaceID: ws, ID: jobID, Status: status, Error: msg}); err != nil {
		return err
	}
	final, _ := q.ResearchJobStatus(bg, store.ResearchJobStatusParams{WorkspaceID: ws, ID: jobID})
	payload := map[string]any{"status": final}
	if msg != nil {
		payload["error"] = *msg
	}
	r.step(bg, StepDone, payload, nil)
	if runErr != nil && final == store.JobStatusFailed {
		s.Log.WarnContext(bg, "research job failed", "workspace_id", ws, "job_id", jobID, "err", runErr)
	}
	return nil
}

func (r *run) watchCancel(ctx context.Context, cancel context.CancelFunc) {
	every := r.s.PollInterval
	if every <= 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r.cancelled(ctx) {
				cancel()
				return
			}
		}
	}
}

func (r *run) cancelled(ctx context.Context) bool {
	st, err := store.New(r.s.Pool).ResearchJobStatus(context.WithoutCancel(ctx), store.ResearchJobStatusParams{WorkspaceID: r.ws, ID: r.job.ID})
	return err == nil && st == store.JobStatusCancelled
}

// step records a progress step and notifies SSE listeners.
func (r *run) step(ctx context.Context, kind string, payload any, runID *uuid.UUID) {
	b, err := json.Marshal(payload)
	if err != nil {
		b = []byte(`{}`)
	}
	r.stepMu.Lock()
	defer r.stepMu.Unlock()
	q := store.New(r.s.Pool)
	ctx = context.WithoutCancel(ctx)
	st, err := q.InsertResearchStep(ctx, store.InsertResearchStepParams{WorkspaceID: r.ws, JobID: r.job.ID, Kind: kind, Payload: b, ModelRunID: runID})
	if err != nil {
		r.s.Log.WarnContext(ctx, "record research step", "job_id", r.job.ID, "err", err)
		return
	}
	_ = q.NotifyResearch(ctx, fmt.Sprintf("%s:%d", r.job.ID, st.Seq))
}

func (r *run) setProgress(ctx context.Context, kv ...any) {
	for i := 0; i+1 < len(kv); i += 2 {
		r.progress[kv[i].(string)] = kv[i+1]
	}
	b, _ := json.Marshal(r.progress)
	_ = store.New(r.s.Pool).SetResearchProgress(context.WithoutCancel(ctx), store.SetResearchProgressParams{WorkspaceID: r.ws, ID: r.job.ID, Progress: b})
}

func (r *run) execute(ctx context.Context) error {
	res, err := r.s.Router.Resolve(ctx, r.ws, "research")
	if err != nil {
		return err
	}
	r.resolved = res
	r.params = res.Targets[0].Params
	deep := r.job.Mode == "deep"
	maxSteps, timeout, tokens := ChatMaxSteps, ChatTimeout, ChatTokenLimit
	if deep {
		maxSteps, timeout, tokens = DeepMaxSteps, DeepTimeout, DeepTokenLimit
	}
	if r.params.MaxSteps > 0 {
		maxSteps = r.params.MaxSteps
	}
	if r.params.TimeoutSec > 0 && deep {
		// The route's time limit is for deep research; chat keeps its own,
		// shorter one so a question never runs for half an hour.
		timeout = time.Duration(r.params.TimeoutSec) * time.Second
	}
	if r.params.TokenBudget > 0 {
		tokens = r.params.TokenBudget
	}
	timeout = min(timeout, MaxTimeout)
	r.params.MaxSteps = maxSteps
	r.deadline = time.Now().Add(timeout)
	r.budget = &agent.Budget{Limit: tokens}
	ctx, cancel := context.WithDeadline(ctx, r.deadline.Add(synthesisGrace))
	defer cancel()

	if r.brand, err = pipeline.LoadBrand(ctx, store.New(r.s.Pool), r.ws); err != nil {
		return err
	}
	caps := res.Targets[0].Caps
	reg := r.s.builtinTools(r.ws, r.nb, r.job.UseWeb)
	if len(res.MCPServerIDs) > 0 && r.s.MCP != nil {
		ts, err := r.s.MCP.Open(ctx, r.ws, res.MCPServerIDs)
		if err != nil {
			r.step(ctx, StepWarning, map[string]any{"text": "MCP tools are unavailable for this job: " + err.Error()}, nil)
		} else {
			defer ts.Close()
			for _, d := range ts.Defs() {
				reg.Add(agent.Tool{Def: d, Source: "mcp", Handler: func(ctx context.Context, args json.RawMessage) (agent.Output, error) {
					text, isErr, err := ts.Call(ctx, d.Name, args)
					if err != nil {
						return agent.Output{}, err
					}
					if isErr {
						return agent.Output{}, errors.New(text)
					}
					return agent.Output{Text: text, Summary: "MCP result"}, nil
				}})
			}
		}
	}
	exclude, err := r.s.unusedKnowledgeTools(ctx, r.ws)
	if err != nil {
		return err
	}
	r.tools = reg.Filter(agent.Policy{Builtins: res.Tools, UseWeb: r.job.UseWeb, Vision: caps.Vision, Exclude: exclude})
	r.setProgress(ctx, "phase", "starting", "tools", r.tools.Names())
	if deep {
		return r.deep(ctx)
	}
	return r.chat(ctx)
}

func (r *run) data(question string) prompts.Data {
	memory := ""
	if _, ok := r.tools.Get(ToolReadMemoryBank); ok {
		memory = knowledge.MemoryIndex(context.Background(), store.New(r.s.Pool), r.ws)
	}
	return prompts.Data{BrandName: r.brand.Name, Brand: r.brand.Summary, ExternalContentRule: prompts.ExternalContentRule, MemoryBank: memory,
		Question: question, Today: time.Now().UTC().Format("2006-01-02"), WebEnabled: r.job.UseWeb && r.tools.Len() > 0 && hasWebTool(r.tools),
		Template: prompts.Template(orDefault(deref(r.job.Template), "freeform"))}
}

func hasWebTool(reg *agent.Registry) bool {
	for _, n := range reg.Names() {
		if t, _ := reg.Get(n); t.Web {
			return true
		}
	}
	return false
}

func (r *run) loop(maxSteps int) *agent.Loop {
	return &agent.Loop{
		Router: r.s.Router, Call: router.Call{Workspace: r.ws, Task: "research"}, Targets: r.resolved.Targets, Tools: r.tools,
		MaxSteps: maxSteps, Deadline: r.deadline, Budget: r.budget, MaxContext: r.resolved.Targets[0].Caps.MaxContext, OutputReserve: r.outputReserve(),
		Summarize: r.summarize, Record: r.step, Cancelled: r.cancelled, RequireTool: true,
	}
}

// summarize condenses an old tool result with the summarize route; without
// one the loop truncates instead.
func (r *run) summarize(ctx context.Context, text string) (string, error) {
	q := store.New(r.s.Pool)
	system, promptID, err := prompts.Active(ctx, q, r.ws, "summarize", r.data(r.job.Question))
	if err != nil {
		return "", err
	}
	resp, _, err := r.s.chatTask(ctx, r.ws, "summarize", promptID, llm.ChatRequest{System: system,
		Messages: []llm.Message{llm.UserText("Research question: " + r.job.Question + "\n\nTool result [1]:\n" + text)}})
	if err != nil {
		return "", err
	}
	r.budget.Add(resp.Usage)
	return resp.Text(), nil
}

func (s *Service) chatTask(ctx context.Context, ws uuid.UUID, task string, promptID *uuid.UUID, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error) {
	res, err := s.Router.Resolve(ctx, ws, task)
	if err != nil {
		return nil, nil, err
	}
	return s.Router.ChatTargets(ctx, router.Call{Workspace: ws, Task: task, PromptID: promptID}, res.Targets, req)
}

// ---- chat ----

func (r *run) chat(ctx context.Context) error {
	q := store.New(r.s.Pool)
	history, err := q.ListChatMessages(ctx, store.ListChatMessagesParams{WorkspaceID: r.ws, JobID: r.job.ID})
	if err != nil {
		return err
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return errors.New("no question to answer")
	}
	msgs := make([]llm.Message, 0, len(history))
	for _, m := range history {
		if m.Role == "assistant" {
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{llm.TextPart(m.Content)}})
		} else {
			msgs = append(msgs, llm.UserText(m.Content))
		}
	}
	system, promptID, err := prompts.Active(ctx, q, r.ws, "research", r.data(history[len(history)-1].Content))
	if err != nil {
		return err
	}
	l := r.loop(r.params.MaxSteps)
	l.Call.PromptID = promptID
	r.setProgress(ctx, "phase", "answering")
	res, err := l.Run(ctx, system, msgs)
	if err != nil {
		return err
	}
	// Chat answers keep citations; charts belong in reports.
	f := agent.Finalize(res.Text, r.nb.Sources(), nil)
	sources, _ := json.Marshal(f.Cited)
	msg, err := q.InsertChatMessage(ctx, store.InsertChatMessageParams{WorkspaceID: r.ws, JobID: r.job.ID, Role: "assistant", Content: f.Text, Sources: sources})
	if err != nil {
		return err
	}
	r.step(ctx, StepAnswer, map[string]any{"message_id": msg.ID, "stopped": res.Stopped, "citations": len(f.Cited)}, res.RunID)
	r.setProgress(ctx, "phase", "done")
	return nil
}

// ---- deep research ----

var planSchema = structured.MustCompile("research_plan", `{
  "type": "object",
  "additionalProperties": false,
  "required": ["title", "sub_questions"],
  "properties": {
    "title": {"type": "string", "maxLength": 160},
    "sub_questions": {"type": "array", "maxItems": 8, "items": {"type": "string", "maxLength": 400}}
  }
}`)

var verifySchema = structured.MustCompile("research_verify", `{
  "type": "object",
  "additionalProperties": false,
  "required": ["claims", "unsupported"],
  "properties": {
    "claims": {
      "type": "array",
      "maxItems": 12,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["claim", "sources"],
        "properties": {
          "claim": {"type": "string", "maxLength": 400},
          "sources": {"type": "array", "items": {"type": "integer"}}
        }
      }
    },
    "unsupported": {
      "type": "array",
      "maxItems": 12,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["claim", "sources", "note"],
        "properties": {
          "claim": {"type": "string", "maxLength": 400},
          "sources": {"type": "array", "items": {"type": "integer"}},
          "note": {"type": "string", "maxLength": 300}
        }
      }
    }
  }
}`)

type claim struct {
	Claim   string `json:"claim"`
	Sources []int  `json:"sources"`
	Single  bool   `json:"single_source"`
	Note    string `json:"note,omitempty"` // why an unsupported claim isn't backed
}

type note struct {
	Question string `json:"sub_question"`
	Text     string `json:"text"`
}

func (r *run) structured(ctx context.Context, task string, data prompts.Data, user string, schema *structured.Schema, out any) (*uuid.UUID, error) {
	q := store.New(r.s.Pool)
	system, promptID, err := prompts.Active(ctx, q, r.ws, task, data)
	if err != nil {
		return nil, err
	}
	resp, attempts, err := r.s.Router.ChatTargets(ctx, router.Call{Workspace: r.ws, Task: "research", PromptID: promptID}, r.resolved.Targets,
		llm.ChatRequest{System: system, Messages: []llm.Message{llm.UserText(user)}, JSONSchema: schema.Raw, SchemaName: task})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", task, err)
	}
	r.budget.Add(resp.Usage)
	if err := schema.Decode(resp.Text(), out); err != nil {
		return nil, fmt.Errorf("%s: %w", task, err)
	}
	return runOf(attempts), nil
}

func (r *run) deep(ctx context.Context) error {
	data := r.data(r.job.Question)

	// 1. Plan.
	r.setProgress(ctx, "phase", "plan")
	var plan struct {
		Title        string   `json:"title"`
		SubQuestions []string `json:"sub_questions"`
	}
	runID, err := r.structured(ctx, "research_plan", data, "Research question: "+r.job.Question, planSchema, &plan)
	if err != nil {
		return err
	}
	var subqs []string
	for _, sq := range plan.SubQuestions {
		if sq = strings.TrimSpace(sq); sq != "" {
			subqs = append(subqs, sq)
		}
	}
	if len(subqs) == 0 {
		subqs = []string{r.job.Question}
	}
	title := strings.TrimSpace(plan.Title)
	if title == "" {
		title = clip(r.job.Question, 120)
	}
	r.step(ctx, StepPlan, map[string]any{"title": title, "sub_questions": subqs}, runID)
	r.setProgress(ctx, "phase", "gather", "steps_planned", len(subqs), "steps_done", 0)

	// 2. Gather: each sub-question gets a fresh history and a share of the
	// remaining steps; the notebook (and so source numbers) is shared.
	q := store.New(r.s.Pool)
	system, promptID, err := prompts.Active(ctx, q, r.ws, "research", data)
	if err != nil {
		return err
	}
	var notes []note
	stepsLeft := r.params.MaxSteps
	stopped := ""
	for i, sq := range subqs {
		if r.cancelled(ctx) {
			return agent.ErrCancelled
		}
		if stopped != "" || stepsLeft <= 0 || time.Now().After(r.deadline) || r.budget.Exhausted() {
			r.step(ctx, StepWarning, map[string]any{"text": fmt.Sprintf("Limits reached; skipped %d of %d sub-questions.", len(subqs)-i, len(subqs))}, nil)
			break
		}
		share := max(2, stepsLeft/(len(subqs)-i))
		l := r.loop(min(share, stepsLeft))
		l.Call.PromptID = promptID
		r.step(ctx, StepPhase, map[string]any{"phase": "gather", "index": i + 1, "of": len(subqs), "sub_question": sq}, nil)
		res, err := l.Run(ctx, system, []llm.Message{llm.UserText(fmt.Sprintf(
			"Overall question: %s\n\nSub-question %d of %d: %s\n\nFirst search for evidence with the tools (search_corpus, and the web if available). Then write concise notes: the findings, each followed by the source numbers the tools showed, like [3], and what remains unclear. Cite only numbers that appear in tool results. If the tools find nothing relevant, write \"No evidence found.\" Notes only; no introduction.",
			r.job.Question, i+1, len(subqs), sq))})
		if err != nil {
			return err
		}
		stepsLeft -= res.Steps
		if res.Stopped == agent.StopDeadline || res.Stopped == agent.StopBudget {
			stopped = res.Stopped
		}
		// Citations to sources the tools never showed are removed here, so
		// they cannot reach verification or the report.
		text, invented := agent.DropUnknown(res.Text, r.nb.Sources())
		if invented > 0 {
			r.step(ctx, StepWarning, map[string]any{"text": fmt.Sprintf("Removed %d citation(s) to sources the tools never returned.", invented)}, nil)
		}
		res.Text = text
		notes = append(notes, note{Question: sq, Text: res.Text})
		r.step(ctx, StepNote, map[string]any{"index": i + 1, "sub_question": sq, "text": clip(res.Text, 3000)}, res.RunID)
		r.setProgress(ctx, "steps_done", i+1)
	}
	if len(notes) == 0 {
		return errors.New("no research notes were gathered before the limits were reached")
	}

	// 3. Verify: key claims, checked against excerpts of the sources they
	// cite. Excerpts get about half the verifier's input budget.
	r.setProgress(ctx, "phase", "verify")
	verifyBudget := r.inputBudget(ctx, "research_verify", data)
	evidence := renderEvidence(renderNotes(notes), r.evidenceSources(), verifyBudget*4/2)
	notesText := renderNotes(r.fitNotes(ctx, notes, verifyBudget-agent.EstimateText(evidence)))
	var verified struct {
		Claims      []claim `json:"claims"`
		Unsupported []claim `json:"unsupported"`
	}
	vctx, vcancel := context.WithTimeout(ctx, verifyTimeout(ctx))
	runID, err = r.structured(vctx, "research_verify", data, notesText+"\n\nSources and excerpts:\n"+evidence, verifySchema, &verified)
	vcancel()
	if err != nil {
		r.step(ctx, StepWarning, map[string]any{"text": "Claim verification failed: " + err.Error()}, nil)
	}
	claims := r.checkClaims(verified.Claims)
	unsupported := r.checkUnsupported(verified.Unsupported)
	r.step(ctx, StepVerify, map[string]any{"claims": claims, "unsupported": unsupported}, runID)

	// 4. Synthesize the report (no tools).
	r.setProgress(ctx, "phase", "synthesize")
	sysSynth, synthPrompt, err := prompts.Active(ctx, q, r.ws, "research_synthesize", data)
	if err != nil {
		return err
	}
	// Everything the report may cite must fit the model's window with room
	// for the reply: keep only cited sources, and condense notes to fit.
	refs := citedIn(notesText)
	for _, c := range claims {
		for _, n := range c.Sources {
			refs[n] = true
		}
	}
	fixed := fmt.Sprintf("Report title: %s\n\nVerified claims:\n%s\nNot supported by their sources:\n%s\nSources:\n%s\nCharts you may place:\n%s", title, renderClaims(claims), renderUnsupported(unsupported), r.sourceList(refs), r.chartList())
	synthNotes := renderNotes(r.fitNotes(ctx, notes, r.inputBudget(ctx, "research_synthesize", data)-agent.EstimateText(fixed)))
	var user strings.Builder
	fmt.Fprintf(&user, "Report title: %s\n\n%s\n\nVerified claims:\n%s\nNot supported by their sources:\n%s\nSources:\n%s\nCharts you may place:\n%s", title, synthNotes, renderClaims(claims), renderUnsupported(unsupported), r.sourceList(refs), r.chartList())
	md, runID, err := r.generateLong(ctx, router.Call{Workspace: r.ws, Task: "research", PromptID: synthPrompt}, sysSynth, user.String())
	if err != nil {
		return fmt.Errorf("synthesize: %w", err)
	}
	md = strings.TrimSpace(md)
	if md == "" {
		return errors.New("synthesize: the model returned an empty report")
	}
	md = ensureTitle(md, title) + keyClaimsSection(claims) + unsupportedSection(unsupported)
	return r.saveReport(ctx, title, md, runID)
}

// verifyTimeout is verification's share of the time left on ctx.
func verifyTimeout(ctx context.Context) time.Duration {
	d, ok := ctx.Deadline()
	if !ok {
		return synthesisGrace
	}
	return time.Duration(float64(time.Until(d)) * verifyShare)
}

// maxContinuations bounds how often a long answer cut off by the model's
// output limit is continued.
const maxContinuations = 3

// generateLong asks for a long text with no tools; when the model stops at
// its output limit it is asked to continue where it stopped.
func (r *run) generateLong(ctx context.Context, call router.Call, system, user string) (string, *uuid.UUID, error) {
	msgs := []llm.Message{llm.UserText(user)}
	var out strings.Builder
	var runID *uuid.UUID
	for i := 0; i <= maxContinuations; i++ {
		resp, attempts, err := r.s.Router.ChatTargets(ctx, call, r.resolved.Targets, llm.ChatRequest{System: system, Messages: msgs})
		if err != nil {
			return "", nil, err
		}
		r.budget.Add(resp.Usage)
		runID = runOf(attempts)
		part := resp.Text()
		out.WriteString(part)
		if resp.StopReason != llm.StopMaxTokens {
			break
		}
		if i == maxContinuations {
			r.step(ctx, StepWarning, map[string]any{"text": "The report reached the model's output limit and may be cut short."}, nil)
			break
		}
		// Send only the tail of what was written, so continuing never grows
		// the request past the context window.
		msgs = []llm.Message{llm.UserText(user), {Role: llm.RoleAssistant, Parts: []llm.Part{llm.TextPart("…" + tail(out.String(), 1500))}},
			llm.UserText("Continue exactly where you stopped, without repeating anything.")}
	}
	return out.String(), runID, nil
}

func (r *run) saveReport(ctx context.Context, title, md string, runID *uuid.UUID) error {
	f := agent.Finalize(md, r.nb.Sources(), r.nb.Charts())
	charts, _ := json.Marshal(f.Charts)
	var reportID uuid.UUID
	err := pgx.BeginFunc(ctx, r.s.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		rep, err := q.InsertReport(ctx, store.InsertReportParams{WorkspaceID: r.ws, JobID: &r.job.ID, Title: clip(title, 200), Markdown: f.Text, Charts: charts, Template: r.job.Template})
		if err != nil {
			return err
		}
		reportID = rep.ID
		for _, c := range f.Cited {
			t := c.Source.Title
			var tp *string
			if t != "" {
				tp = &t
			}
			if err := q.InsertCitation(ctx, store.InsertCitationParams{WorkspaceID: r.ws, ReportID: rep.ID, Marker: c.Marker, RawItemID: c.Source.RawItemID, Url: c.Source.URL, Title: tp}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("save report: %w", err)
	}
	r.step(ctx, StepReport, map[string]any{"report_id": reportID, "title": title, "citations": len(f.Cited), "charts": len(f.Charts), "dropped": f.Dropped}, runID)
	r.setProgress(ctx, "phase", "done", "report_id", reportID)
	return nil
}

// checkClaims keeps sources that exist and marks single-source claims.
func (r *run) checkClaims(in []claim) []claim {
	out := []claim{}
	for _, c := range in {
		c.Claim = strings.TrimSpace(c.Claim)
		var ok []int
		for _, n := range c.Sources {
			if _, exists := r.nb.Source(n); exists && !containsInt(ok, n) {
				ok = append(ok, n)
			}
		}
		if c.Claim == "" || len(ok) == 0 {
			continue
		}
		c.Sources, c.Single = ok, len(ok) == 1
		out = append(out, c)
	}
	return out
}

// sourceList lists the given sources (all when refs is nil), compactly.
// checkUnsupported keeps unsupported claims with text, dropping citation
// numbers that don't exist; a claim citing nothing real is still listed.
func (r *run) checkUnsupported(in []claim) []claim {
	out := []claim{}
	for _, c := range in {
		c.Claim, c.Note = strings.TrimSpace(c.Claim), strings.TrimSpace(c.Note)
		if c.Claim == "" {
			continue
		}
		var ok []int
		for _, n := range c.Sources {
			if _, exists := r.nb.Source(n); exists && !containsInt(ok, n) {
				ok = append(ok, n)
			}
		}
		c.Sources, c.Single = ok, false
		out = append(out, c)
	}
	return out
}

// evidenceSources pairs every source with the text the tools showed for it.
func (r *run) evidenceSources() []evidenceSource {
	srcs := r.nb.Sources()
	out := make([]evidenceSource, 0, len(srcs))
	for _, s := range srcs {
		out = append(out, evidenceSource{N: s.N, Title: s.Title, URL: s.URL, Text: r.nb.Evidence(s.N)})
	}
	return out
}

func (r *run) sourceList(refs map[int]bool) string {
	var b strings.Builder
	for _, s := range r.nb.Sources() {
		if refs != nil && !refs[s.N] {
			continue
		}
		fmt.Fprintf(&b, "[%d] %s — %s\n", s.N, clip(orURL(s.Title, s.URL), 90), clip(s.URL, 120))
	}
	if b.Len() == 0 {
		return "(none)\n"
	}
	return b.String()
}

func (r *run) chartList() string {
	var b strings.Builder
	for _, c := range r.nb.Charts() {
		fmt.Fprintf(&b, "{{chart:%s}} — %s (%s, %d rows)\n", c.ID, c.Title, c.Type, len(c.Data))
	}
	if b.Len() == 0 {
		return "(none)\n"
	}
	return b.String()
}

// outputReserve is the room kept for a reply: the route's max tokens
// (default 2048), at most half the window.
func (r *run) outputReserve() int {
	reserve := r.params.MaxTokens
	if reserve <= 0 {
		reserve = 2048
	}
	if mc := r.resolved.Targets[0].Caps.MaxContext; mc > 0 {
		reserve = min(reserve, mc/2)
	}
	return reserve
}

// inputBudget is how many tokens a no-tool request for task may carry
// besides its system prompt; 0 means the window is unknown (no limit).
func (r *run) inputBudget(ctx context.Context, task string, data prompts.Data) int {
	mc := r.resolved.Targets[0].Caps.MaxContext
	if mc <= 0 {
		return 0
	}
	system, _, err := prompts.Active(ctx, store.New(r.s.Pool), r.ws, task, data)
	if err != nil {
		system = ""
	}
	return max(mc-r.outputReserve()-256-agent.EstimateText(system), 300)
}

// fitNotes condenses notes so they fit budget tokens (0 = no limit): each
// over-long note is summarized with the summarize route, keeping its source
// numbers, or trimmed at a line boundary if that fails.
func (r *run) fitNotes(ctx context.Context, notes []note, budget int) []note {
	if budget <= 0 || agent.EstimateText(renderNotes(notes)) <= budget {
		return notes
	}
	per := max(budget/max(len(notes), 1)-20, 60)
	out := make([]note, len(notes))
	for i, n := range notes {
		out[i] = n
		if agent.EstimateText(n.Text) <= per {
			continue
		}
		words := max(per*2/3, 40)
		if s, err := r.condense(ctx, n.Text, words); err == nil && agent.EstimateText(s) <= per {
			out[i].Text = s
			continue
		}
		out[i].Text = trimLines(n.Text, per*3)
	}
	r.step(ctx, StepWarning, map[string]any{"text": "Research notes were condensed to fit the model's context window."}, nil)
	return out
}

func (r *run) condense(ctx context.Context, text string, words int) (string, error) {
	resp, _, err := r.s.chatTask(ctx, r.ws, "summarize", nil, llm.ChatRequest{
		System:   "You condense research notes. Keep every finding that has a source number like [3], with its number attached. Never add facts.",
		Messages: []llm.Message{llm.UserText(fmt.Sprintf("Condense these notes to at most %d words:\n\n%s", words, text))}})
	if err != nil {
		return "", err
	}
	r.budget.Add(resp.Usage)
	return strings.TrimSpace(resp.Text()), nil
}

// trimLines keeps whole lines up to n characters.
func trimLines(s string, n int) string {
	if len(s) <= n {
		return s
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if b.Len()+len(line)+1 > n {
			break
		}
		b.WriteString(line + "\n")
	}
	if b.Len() == 0 {
		return clip(s, n/2)
	}
	return strings.TrimSpace(b.String()) + "\n…"
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

var citeNumRe = regexp.MustCompile(`\[(\d{1,3})\]`)

// citedIn returns the source numbers a text cites.
func citedIn(text string) map[int]bool {
	out := map[int]bool{}
	for _, m := range citeNumRe.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		out[n] = true
	}
	return out
}

func renderNotes(notes []note) string {
	var b strings.Builder
	b.WriteString("Research notes:\n")
	for i, n := range notes {
		fmt.Fprintf(&b, "\n### %d. %s\n%s\n", i+1, n.Question, strings.TrimSpace(n.Text))
	}
	return b.String()
}

func renderClaims(claims []claim) string {
	if len(claims) == 0 {
		return "(none)\n"
	}
	var b strings.Builder
	for _, c := range claims {
		fmt.Fprintf(&b, "- %s %s%s\n", c.Claim, markers(c.Sources), map[bool]string{true: " (single source)", false: ""}[c.Single])
	}
	return b.String()
}

// keyClaimsSection lists verified claims with their sources, marking
// single-source claims, so the report always shows how well each is backed.
func keyClaimsSection(claims []claim) string {
	if len(claims) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Key claims and evidence\n\n")
	for _, c := range claims {
		fmt.Fprintf(&b, "- %s %s", c.Claim, markers(c.Sources))
		if c.Single {
			b.WriteString(" — *single source*")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderUnsupported(claims []claim) string {
	if len(claims) == 0 {
		return "(none)\n"
	}
	var b strings.Builder
	for _, c := range claims {
		fmt.Fprintf(&b, "- %s (cited %s; %s)\n", c.Claim, orDefault(markers(c.Sources), "nothing"), orDefault(c.Note, "not in the source"))
	}
	return b.String()
}

// unsupportedSection lists claims the cited sources don't back, so a reader
// sees what the check rejected instead of it vanishing from the report.
func unsupportedSection(claims []claim) string {
	if len(claims) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Not supported by the cited sources\n\nThe research notes made these claims, but the sources they cite don't back them. Treat them as unverified.\n\n")
	for _, c := range claims {
		fmt.Fprintf(&b, "- %s", c.Claim)
		if m := markers(c.Sources); m != "" {
			fmt.Fprintf(&b, " (cited %s)", m)
		}
		if c.Note != "" {
			fmt.Fprintf(&b, " — %s", c.Note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func markers(ns []int) string {
	var b strings.Builder
	for _, n := range ns {
		fmt.Fprintf(&b, "[%d]", n)
	}
	return b.String()
}

func ensureTitle(md, title string) string {
	if strings.HasPrefix(md, "# ") {
		return md
	}
	return "# " + title + "\n\n" + md
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func runOf(attempts []router.Attempt) *uuid.UUID {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Err == nil && attempts[i].RunID != uuid.Nil {
			id := attempts[i].RunID
			return &id
		}
	}
	return nil
}
