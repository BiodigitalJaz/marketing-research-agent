// Package sources defines the collection connector interface, the
// connector registry, and item normalization. Connectors are read-only:
// they only issue GET requests (enforced by scripts/check_no_posting.sh).
package sources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/match"
)

// Item is one collected piece of content. Text fields are untrusted.
type Item struct {
	ExternalID   string
	URL          string
	AuthorHandle string
	Title        string
	Body         string
	Community    string
	PublishedAt  time.Time
	Metrics      map[string]any
}

// Query is what a connector needs for one run.
type Query struct {
	// Match selects items for connectors without server-side search: the
	// monitor query (or tracked keywords) minus negative keywords.
	Match *match.Matcher
	// Exclude holds only the negative keywords (for search results, which
	// the backend already matched).
	Exclude *match.Matcher
	// Terms are positive search strings for search connectors.
	Terms  []string
	Since  time.Time
	Config json.RawMessage
	State  json.RawMessage
	// Search is the workspace's search backend (search connector only).
	Search search.Backend
	// Budget is called before each paid search query; an error stops the run.
	Budget func(ctx context.Context) error
}

// Result is a connector run's output: items plus the new cursor state.
type Result struct {
	Items []Item
	State json.RawMessage
}

// Source is implemented by each connector.
type Source interface {
	Connector() string
	// ValidateConfig checks a source's stored configuration.
	ValidateConfig(raw json.RawMessage) error
	Fetch(ctx context.Context, q Query) (Result, error)
}

// Deps are shared by connectors.
type Deps struct {
	Fetch *fetch.Fetcher
	Now   func() time.Time
}

// Factory builds a connector.
type Factory func(Deps) Source

var registry = map[string]Factory{}

// Register adds a connector; called from connector packages' init.
func Register(name string, f Factory) { registry[name] = f }

// Get builds a registered connector.
func Get(name string, d Deps) (Source, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown connector %q", name)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return f(d), nil
}

// Connectors lists registered connector names.
func Connectors() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Normalization limits.
const (
	MaxBodyRunes  = 20_000
	MaxTitleRunes = 500
)

// Normalize trims and collapses whitespace, strips any HTML, and caps sizes.
func Normalize(it Item) Item {
	it.Title = truncate(fetch.HTMLToText(it.Title), MaxTitleRunes)
	it.Body = truncate(fetch.HTMLToText(it.Body), MaxBodyRunes)
	it.Community = strings.TrimSpace(it.Community)
	it.AuthorHandle = strings.TrimSpace(it.AuthorHandle)
	it.URL = strings.TrimSpace(it.URL)
	if it.Metrics == nil {
		it.Metrics = map[string]any{}
	}
	return it
}

// ContentHash is sha256 of the normalized, lowercased title and body; it
// identifies the same content seen through different sources.
func ContentHash(it Item) []byte {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(it.Title+"\n"+it.Body), " "))))
	return sum[:]
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n]))
}

// DecodeConfig unmarshals config strictly (unknown fields are errors). The
// shared "rules" key is validated here and left out of v.
func DecodeConfig(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	if r, ok := fields[rulesKey]; ok {
		if _, err := decodeRules(r); err != nil {
			return err
		}
		delete(fields, rulesKey)
		raw, _ = json.Marshal(fields)
	}
	return strictDecode(raw, v, "invalid config")
}

func strictDecode(raw json.RawMessage, v any, prefix string) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return nil
}

const rulesKey = "rules"

// Rules are a community's posting rules, stored as sources.config.rules for
// every connector. Collection ignores them; drafting enforces them.
type Rules struct {
	NoLinks             bool     `json:"no_links"`             // links are removed from drafts
	NoSelfPromotion     bool     `json:"no_self_promotion"`    // mentioning the brand is flagged
	DiscloseAffiliation bool     `json:"disclose_affiliation"` // a brand mention without disclosure blocks the draft
	Notes               []string `json:"notes"`                // free-text rules, checked by the model
}

// Limits on free-text rules.
const (
	MaxRuleNotes     = 20
	MaxRuleNoteChars = 300
)

// RulesFromConfig returns the rules in a source config (zero value if none).
func RulesFromConfig(raw json.RawMessage) (Rules, error) {
	var fields map[string]json.RawMessage
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return Rules{}, fmt.Errorf("invalid config: %w", err)
		}
	}
	r, ok := fields[rulesKey]
	if !ok {
		return Rules{}, nil
	}
	return decodeRules(r)
}

func decodeRules(raw json.RawMessage) (Rules, error) {
	var r Rules
	if string(raw) == "null" {
		return r, nil
	}
	if err := strictDecode(raw, &r, "invalid rules"); err != nil {
		return r, err
	}
	if len(r.Notes) > MaxRuleNotes {
		return r, fmt.Errorf("invalid rules: at most %d notes", MaxRuleNotes)
	}
	notes := r.Notes[:0]
	for _, n := range r.Notes {
		n = strings.TrimSpace(n)
		if utf8.RuneCountInString(n) > MaxRuleNoteChars {
			return r, fmt.Errorf("invalid rules: notes are limited to %d characters", MaxRuleNoteChars)
		}
		if n != "" {
			notes = append(notes, n)
		}
	}
	r.Notes = notes
	return r, nil
}
