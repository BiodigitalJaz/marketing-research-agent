package prompts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Data is available to every prompt template. Fields a task does not use
// are empty; unknown fields are template errors.
type Data struct {
	BrandName           string
	Brand               string
	Lane                string
	OppType             string
	ExternalContentRule string
	// Drafting.
	Kind         string // reply | pitch | comment
	MaxWords     int
	LinksAllowed bool
	Community    string
	Tone         string
	Rules        string // draft_check: the rules to check, one per line
	// Research.
	Question   string
	Template   string // report skeleton (Markdown)
	Today      string // YYYY-MM-DD
	WebEnabled bool
	// MemoryBank is the memory bank index (research), "" when there is none.
	MemoryBank string
}

// Fields lists the template fields, for validation messages.
const Fields = ".BrandName .Brand .Lane .OppType .ExternalContentRule .Kind .MaxWords .LinksAllowed .Community .Tone .Rules .Question .Template .Today .WebEnabled .MemoryBank"

// SampleData fills every field, to check that an edited prompt renders.
func SampleData() Data {
	return Data{BrandName: "Brand", Brand: "summary", Lane: "inbound", OppType: "pain_point", ExternalContentRule: ExternalContentRule,
		Kind: "reply", MaxWords: 180, LinksAllowed: true, Community: "a forum", Tone: "voice", Rules: "- a rule",
		Question: "a question", Template: "# Title", Today: "2026-01-01", WebEnabled: true, MemoryBank: "- projectbrief.md: the foundation"}
}

// Active loads a task's active prompt (the workspace's version, else the
// shipped default) and renders it.
func Active(ctx context.Context, q *store.Queries, ws uuid.UUID, task string, data Data) (string, *uuid.UUID, error) {
	p, err := q.GetActivePrompt(ctx, store.GetActivePromptParams{WorkspaceID: &ws, Task: task})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, fmt.Errorf("no active %s prompt (run the seed): %w", task, err)
	}
	if err != nil {
		return "", nil, fmt.Errorf("load %s prompt: %w", task, err)
	}
	body, err := Render(task, p.Body, data)
	if err != nil {
		return "", nil, err
	}
	return body, &p.ID, nil
}
