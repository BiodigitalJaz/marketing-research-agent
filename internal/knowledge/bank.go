package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/agent"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/structured"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Router is the routing surface memory bank generation needs.
type Router interface {
	Resolve(ctx context.Context, ws uuid.UUID, task string) (*router.Resolved, error)
	ChatTargets(ctx context.Context, call router.Call, targets []router.Target, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error)
}

// CoreFile is one memory bank file (Cline's six core files).
type CoreFile struct {
	Name    string
	Purpose string
}

// CoreFiles in dependency order: projectbrief is the foundation the others
// build on; activeContext and progress change most often.
var CoreFiles = []CoreFile{
	{"projectbrief", "The foundation: what the project is, its core requirements and goals, scope and non-goals. Sections: Overview, Core requirements, Goals, Scope and non-goals."},
	{"productContext", "Why it exists: the problems it solves, who it is for, how it should work for the user, positioning and limits on claims. Sections: Problem, Audience, How it works for the user, Positioning."},
	{"systemPatterns", "Architecture: components and how they relate, key technical decisions and patterns, the security model and trust boundaries. Sections: Architecture, Key decisions, Security model, Patterns."},
	{"techContext", "Technologies, infrastructure and deployment, development setup, technical constraints and dependencies. Sections: Stack, Infrastructure and deployment, Development, Constraints and dependencies."},
	{"activeContext", "Current focus: recent changes, active decisions, next steps, open questions. Sections: Current focus, Recent changes, Active decisions, Next steps, Open questions."},
	{"progress", "Status: what works, what is left to build, known issues (including gaps between the docs and the implementation), how the project evolved. Sections: What works, What's left, Known issues, Evolution."},
}

// CoreFileNames lists the core file names.
func CoreFileNames() []string {
	out := make([]string, len(CoreFiles))
	for i, f := range CoreFiles {
		out[i] = f.Name
	}
	return out
}

// Word limits for generated text.
const (
	sectionWords  = 120
	notesWords    = 250
	digestWords   = 300
	coreFileWords = 600
)

var digestSchema = func() *structured.Schema {
	names := `"` + strings.Join(CoreFileNames(), `", "`) + `"`
	return structured.MustCompile("knowledge_digest", `{
  "type": "object",
  "additionalProperties": false,
  "required": ["digest", "informs"],
  "properties": {
    "digest": {"type": "string", "maxLength": 4000},
    "informs": {"type": "array", "items": {"type": "string", "enum": [`+names+`]}}
  }
}`)
}()

// ErrNoRouter means generation is not configured (sync still works).
var ErrNoRouter = errors.New("memory bank generation needs a router")

// BuildMemoryBank advances the memory bank until it is current or until
// deadline passes; it returns done=false when more work remains, so a job
// can continue in another run. Work is saved as it goes: section
// summaries, then document digests, then the core files whose source
// digests changed.
// It returns ErrBusy while a sync or another build holds the lock.
func (s *Service) BuildMemoryBank(ctx context.Context, ws uuid.UUID, deadline time.Time) (bool, error) {
	if s.Router == nil {
		return false, ErrNoRouter
	}
	var done bool
	err := s.withLock(ctx, ws, func() error {
		q := store.New(s.Pool)
		s.setStatus(ctx, q, ws, "building", nil)
		var err error
		done, err = s.buildLocked(ctx, ws, deadline)
		switch {
		case err != nil:
			msg := "Memory bank: " + err.Error()
			s.setStatus(ctx, q, ws, "error", &msg)
		case done:
			s.setStatus(ctx, q, ws, "idle", nil)
		}
		return err
	})
	return done, err
}

// setStatus records the knowledge status on every source of a workspace.
func (s *Service) setStatus(ctx context.Context, q *store.Queries, ws uuid.UUID, status string, msg *string) {
	ctx = context.WithoutCancel(ctx)
	srcs, _ := q.ListKnowledgeSources(ctx, ws)
	for _, src := range srcs {
		_ = q.SetKnowledgeSourceStatus(ctx, store.SetKnowledgeSourceStatusParams{WorkspaceID: ws, ID: src.ID, Status: status, LastError: msg})
	}
}

// MarkPending shows a queued memory bank build in the status.
func (s *Service) MarkPending(ctx context.Context, ws uuid.UUID) {
	s.setStatus(ctx, store.New(s.Pool), ws, "building", nil)
}

func (s *Service) buildLocked(ctx context.Context, ws uuid.UUID, deadline time.Time) (bool, error) {
	q := store.New(s.Pool)
	b := &builder{s: s, q: q, ws: ws}
	if err := b.init(ctx); err != nil {
		return false, err
	}
	// 1. Section summaries for documents whose digest is stale.
	for {
		if time.Now().After(deadline) {
			return false, nil
		}
		rows, err := q.ChunksMissingSummary(ctx, store.ChunksMissingSummaryParams{WorkspaceID: ws, Limit: 8})
		if err != nil {
			return false, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			sum, err := b.generate(ctx, "summarize", "knowledge_digest", b.data("section", sectionWords), r.Heading, r.Content)
			if err != nil {
				return false, fmt.Errorf("summarize %s: %w", r.Heading, err)
			}
			if err := q.SetKnowledgeChunkSummary(ctx, store.SetKnowledgeChunkSummaryParams{WorkspaceID: ws, ID: r.ID, Summary: &sum}); err != nil {
				return false, err
			}
		}
	}
	// 2. Document digests, and which core files each informs.
	docs, err := q.DocsNeedingDigest(ctx, ws)
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		if time.Now().After(deadline) {
			return false, nil
		}
		if err := b.digest(ctx, d); err != nil {
			return false, fmt.Errorf("digest %s: %w", d.Path, err)
		}
	}
	// 3. Core files, rebuilt only when their source digests changed.
	return b.coreFiles(ctx, deadline)
}

type builder struct {
	s       *Service
	q       *store.Queries
	ws      uuid.UUID
	brand   string
	budget  int
	targets map[string]*router.Resolved
}

func (b *builder) init(ctx context.Context) error {
	b.targets = map[string]*router.Resolved{}
	b.brand = "the project"
	if w, err := b.q.GetWorkspace(ctx, b.ws); err == nil {
		b.brand = w.Name
	}
	res, err := b.resolve(ctx, "research")
	if err != nil {
		return err
	}
	b.budget = inputBudget(res.Targets[0])
	return nil
}

func (b *builder) resolve(ctx context.Context, task string) (*router.Resolved, error) {
	if r, ok := b.targets[task]; ok {
		return r, nil
	}
	r, err := b.s.Router.Resolve(ctx, b.ws, task)
	if err != nil {
		return nil, err
	}
	b.targets[task] = r
	return r, nil
}

// inputBudget is the room for a request's input on a target: the declared
// context window minus the output reserve and a margin (12k tokens when the
// window is unknown).
func inputBudget(t router.Target) int {
	mc := t.Caps.MaxContext
	if mc <= 0 {
		return 12000
	}
	reserve := t.Params.MaxTokens
	if reserve <= 0 {
		reserve = 2048
	}
	reserve = min(reserve, mc/2)
	return max(mc-reserve-512, 600)
}

func (b *builder) data(kind string, words int) prompts.Data {
	return prompts.Data{BrandName: b.brand, Kind: kind, MaxWords: words, ExternalContentRule: prompts.ExternalContentRule, Template: coreFileList()}
}

func coreFileList() string {
	var sb strings.Builder
	for _, f := range CoreFiles {
		fmt.Fprintf(&sb, "- %s: %s\n", f.Name, f.Purpose)
	}
	return strings.TrimSpace(sb.String())
}

// generate renders a prompt and returns the model's text for one input.
func (b *builder) generate(ctx context.Context, task, promptTask string, data prompts.Data, label, text string) (string, error) {
	resp, err := b.call(ctx, task, promptTask, data, prompts.ExternalContent(label, text), nil)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(resp.Text())
	if out == "" {
		return "", errors.New("empty response")
	}
	return out, nil
}

func (b *builder) call(ctx context.Context, task, promptTask string, data prompts.Data, user string, schema *structured.Schema) (*llm.ChatResponse, error) {
	res, err := b.resolve(ctx, task)
	if err != nil {
		return nil, err
	}
	system, promptID, err := prompts.Active(ctx, b.q, b.ws, promptTask, data)
	if err != nil {
		return nil, err
	}
	req := llm.ChatRequest{System: system, Messages: []llm.Message{llm.UserText(user)}}
	if schema != nil {
		req.JSONSchema, req.SchemaName = schema.Raw, promptTask
	}
	resp, _, err := b.s.Router.ChatTargets(ctx, router.Call{Workspace: b.ws, Task: task, PromptID: promptID}, res.Targets, req)
	return resp, err
}

// fit condenses texts until they fit the input budget, in batches.
func (b *builder) fit(ctx context.Context, label string, texts []string) (string, error) {
	budget := b.budget - 400 // the prompt itself
	for round := 0; ; round++ {
		joined := strings.Join(texts, "\n\n")
		if agent.EstimateText(joined) <= budget || len(texts) <= 1 || round > 4 {
			return clipTokens(joined, budget), nil
		}
		var next []string
		var batch []string
		size := 0
		for _, t := range texts {
			n := agent.EstimateText(t)
			if size+n > budget && len(batch) > 0 {
				c, err := b.generate(ctx, "summarize", "knowledge_digest", b.data("notes", notesWords), label, strings.Join(batch, "\n\n"))
				if err != nil {
					return "", err
				}
				next = append(next, c)
				batch, size = nil, 0
			}
			batch = append(batch, t)
			size += n
		}
		if len(batch) > 0 {
			c, err := b.generate(ctx, "summarize", "knowledge_digest", b.data("notes", notesWords), label, strings.Join(batch, "\n\n"))
			if err != nil {
				return "", err
			}
			next = append(next, c)
		}
		texts = next
	}
}

func clipTokens(s string, tokens int) string {
	if agent.EstimateText(s) <= tokens {
		return s
	}
	return clip(s, tokens*3)
}

func (b *builder) digest(ctx context.Context, d store.DocsNeedingDigestRow) error {
	chunks, err := b.q.DocChunks(ctx, store.DocChunksParams{WorkspaceID: b.ws, DocID: d.ID})
	if err != nil {
		return err
	}
	var parts []string
	for _, c := range chunks {
		if c.Summary == nil {
			return fmt.Errorf("section %q has no summary yet", c.Heading)
		}
		parts = append(parts, c.Heading+": "+*c.Summary)
	}
	if len(parts) == 0 {
		parts = []string{d.Title + ": (empty document)"}
	}
	text, err := b.fit(ctx, d.Path, parts)
	if err != nil {
		return err
	}
	resp, err := b.call(ctx, "research", "knowledge_digest", b.data("document", digestWords),
		"Document: "+d.Path+" ("+d.Title+")\n\n"+prompts.ExternalContent(d.Path, text), digestSchema)
	if err != nil {
		return err
	}
	var out struct {
		Digest  string   `json:"digest"`
		Informs []string `json:"informs"`
	}
	if err := digestSchema.Decode(resp.Text(), &out); err != nil {
		return err
	}
	informs := []string{}
	for _, n := range out.Informs {
		if slices.Contains(CoreFileNames(), n) && !slices.Contains(informs, n) {
			informs = append(informs, n)
		}
	}
	digest := strings.TrimSpace(out.Digest)
	return b.q.SetKnowledgeDocDigest(ctx, store.SetKnowledgeDocDigestParams{WorkspaceID: b.ws, ID: d.ID, Digest: &digest, DigestSha: &d.BlobSha, Informs: informs})
}

// coreFiles regenerates each core file whose inputs changed. Dependent
// files include projectbrief's signature, so a new foundation ripples.
func (b *builder) coreFiles(ctx context.Context, deadline time.Time) (bool, error) {
	docs, err := b.q.DigestedDocs(ctx, b.ws)
	if err != nil {
		return false, err
	}
	if len(docs) == 0 {
		return true, nil
	}
	existing := map[string]store.MemoryBank{}
	files, err := b.q.ListMemoryBank(ctx, b.ws)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		existing[f.Name] = f
	}
	commit, _ := b.q.KnowledgeLatestCommit(ctx, b.ws)
	brief := ""
	briefSig := ""
	for _, cf := range CoreFiles {
		var contrib []store.DigestedDocsRow
		for _, d := range docs {
			if slices.Contains(d.Informs, cf.Name) {
				contrib = append(contrib, d)
			}
		}
		if len(contrib) == 0 && cf.Name == "projectbrief" {
			contrib = docs // the foundation draws on everything if nothing claims it
		}
		sig := signature(contrib)
		if cf.Name != "projectbrief" {
			sig = append(sig, "projectbrief@"+briefSig)
		}
		cur, ok := existing[cf.Name]
		if ok && slices.Equal(cur.Sources, sig) {
			if cf.Name == "projectbrief" {
				brief, briefSig = cur.Content, hashOf(cur.Content)
			}
			continue
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		content := "Not documented: no document in the knowledge base covers this yet.\n"
		var runID *uuid.UUID
		if len(contrib) > 0 {
			var parts []string
			for _, d := range contrib {
				parts = append(parts, fmt.Sprintf("Digest of %s (%s):\n%s", d.Path, d.Title, *d.Digest))
			}
			text, err := b.fit(ctx, cf.Name, parts)
			if err != nil {
				return false, err
			}
			user := ""
			if cf.Name != "projectbrief" && brief != "" {
				user = "projectbrief.md (the foundation, for context):\n" + clipTokens(brief, 600) + "\n\n"
			}
			data := b.data(cf.Name, coreFileWords)
			data.Template = cf.Purpose
			resp, err := b.call(ctx, "research", "memory_bank", data, user+prompts.ExternalContent("document digests", text), nil)
			if err != nil {
				return false, fmt.Errorf("memory bank %s: %w", cf.Name, err)
			}
			content = strings.TrimSpace(resp.Text())
		}
		var c *string
		if commit != "" {
			c = &commit
		}
		if err := b.q.UpsertMemoryBankFile(ctx, store.UpsertMemoryBankFileParams{WorkspaceID: b.ws, Name: cf.Name, Content: content, Sources: sig, CommitSha: c, ModelRunID: runID}); err != nil {
			return false, err
		}
		if cf.Name == "projectbrief" {
			brief, briefSig = content, hashOf(content)
		}
	}
	return true, nil
}

func signature(docs []store.DigestedDocsRow) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Path+"@"+d.BlobSha)
	}
	sort.Strings(out)
	return out
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// MemoryIndex is the short memory bank overview research puts in its
// system prompt ("" when there is no memory bank yet).
func MemoryIndex(ctx context.Context, q *store.Queries, ws uuid.UUID) string {
	files, err := q.ListMemoryBank(ctx, ws)
	if err != nil || len(files) == 0 {
		return ""
	}
	have := map[string]store.MemoryBank{}
	for _, f := range files {
		have[f.Name] = f
	}
	var sb strings.Builder
	for _, cf := range CoreFiles {
		f, ok := have[cf.Name]
		if !ok {
			continue
		}
		purpose, _, _ := strings.Cut(cf.Purpose, ". Sections:")
		fmt.Fprintf(&sb, "- %s.md: %s (updated %s)\n", cf.Name, purpose, f.UpdatedAt.UTC().Format("2006-01-02"))
	}
	return strings.TrimSpace(sb.String())
}
