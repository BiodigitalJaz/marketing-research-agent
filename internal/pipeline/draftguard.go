package pipeline

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
)

// Draft kinds (lane_configs.draft_kind) and their limits.
const (
	KindReply   = "reply"
	KindPitch   = "pitch"
	KindComment = "comment"
)

// KindSpec describes what one draft kind must look like.
type KindSpec struct {
	Variants []string
	MaxWords int
	// VariantMaxWords tightens the limit for specific variants.
	VariantMaxWords map[string]int
	Subject         bool // pitch: needs a subject line
	LinksAllowed    bool // comments carry no links by default
}

// KindSpecs are the shipped draft kinds.
var KindSpecs = map[string]KindSpec{
	KindReply:   {Variants: []string{"helpful", "story", "short"}, MaxWords: 180, VariantMaxWords: map[string]int{"short": 70}, LinksAllowed: true},
	KindPitch:   {Variants: []string{"pitch"}, MaxWords: 200, Subject: true, LinksAllowed: true},
	KindComment: {Variants: []string{"perspective", "brief"}, MaxWords: 150, VariantMaxWords: map[string]int{"brief": 70}},
}

// WordLimit is the word limit for a variant.
func (k KindSpec) WordLimit(variant string) int {
	if n, ok := k.VariantMaxWords[variant]; ok {
		return n
	}
	return k.MaxWords
}

// RuleFlag is one entry of drafts.rule_flags.
type RuleFlag struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // info | warning | block
	Note     string `json:"note"`
	Source   string `json:"source"` // community | style | brand | model
}

// Severities, mildest first.
const (
	SevInfo    = "info"
	SevWarning = "warning"
	SevBlock   = "block"
)

// WordCount counts whitespace-separated words.
func WordCount(s string) int { return len(strings.Fields(s)) }

// BannedPhrases returns the tone guide's literal banned phrases: "don't"
// entries of at most three words, or any entry wrapped in double quotes.
// Longer entries are guidance for the model, checked by the draft check.
func BannedPhrases(dont []string) []string {
	var out []string
	for _, d := range dont {
		d = strings.TrimSpace(d)
		if q := strings.Trim(d, `"“”`); q != d && q != "" {
			out = append(out, q)
			continue
		}
		if d != "" && len(strings.Fields(d)) <= 3 {
			out = append(out, d)
		}
	}
	return out
}

// GuidanceRules returns the "don't" entries that are not literal phrases.
func GuidanceRules(dont []string) []string {
	banned := BannedPhrases(dont)
	var out []string
	for _, d := range dont {
		d = strings.TrimSpace(d)
		if d != "" && !slices.Contains(banned, d) && !slices.Contains(banned, strings.Trim(d, `"“”`)) {
			out = append(out, d)
		}
	}
	return out
}

// FindBanned returns the banned phrases that appear in text as whole words,
// case-insensitively.
func FindBanned(text string, phrases []string) []string {
	var hits []string
	for _, p := range phrases {
		re, err := regexp.Compile(`(?i)(^|\W)` + strings.Join(strings.Fields(regexp.QuoteMeta(p)), `\s+`) + `($|\W)`)
		if err == nil && re.MatchString(text) {
			hits = append(hits, p)
		}
	}
	return hits
}

var (
	mdLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\((?:https?://|www\.)[^)\s]*\)`)
	urlRe      = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>()\[\]]*[^\s<>()\[\].,;:!?'"]`)
	bareHostRe = regexp.MustCompile(`(?i)\b[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*\.(?:com|io|ai|net|org|app|co|dev|xyz|me|us|so|sh|ly)\b(?:/[^\s<>()]*[^\s<>().,;:!?'"])?`)
	spacesRe   = regexp.MustCompile(`[ \t]{2,}`)
	spacePunct = regexp.MustCompile(`[ \t]+([.,;:!?])`)
)

// StripLinks removes links from text. URLs in keep (exact match) survive;
// with bareHosts, domain names such as example.com count as links too.
// It reports whether anything was removed.
func StripLinks(text string, keep []string, bareHosts bool) (string, bool) {
	removed := false
	out := mdLinkRe.ReplaceAllStringFunc(text, func(m string) string {
		url := m[strings.Index(m, "](")+2 : len(m)-1]
		if slices.Contains(keep, url) {
			return m
		}
		removed = true
		return mdLinkRe.FindStringSubmatch(m)[1]
	})
	out = urlRe.ReplaceAllStringFunc(out, func(m string) string {
		if slices.Contains(keep, m) {
			return m
		}
		removed = true
		return ""
	})
	if bareHosts {
		out = bareHostRe.ReplaceAllStringFunc(out, func(m string) string {
			removed = true
			return ""
		})
	}
	if !removed {
		return text, false
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(spacePunct.ReplaceAllString(spacesRe.ReplaceAllString(l, " "), "$1"), " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), true
}

// MentionsBrand reports whether text names the brand.
func MentionsBrand(text, brand string) bool {
	if strings.TrimSpace(brand) == "" {
		return false
	}
	return len(FindBanned(text, []string{brand})) > 0
}

var disclosureRe = regexp.MustCompile(`(?i)\b(disclosure|disclaimer|i work (on|at|for)|i'm (one of )?the (founder|builder|team)|i am (one of )?the (founder|builder|team)|i (build|built|made|run|wrote|co-?founded|founded|work on)|i'm building|i am building|we (build|built|make|run)|my (company|startup|team|product)|our (company|startup|team|product)|affiliated|full disclosure|i'm on the team|i'm with)\b`)

// HasDisclosure reports whether text discloses an affiliation.
func HasDisclosure(text string) bool { return disclosureRe.MatchString(text) }

// GuardInput is what the deterministic checks need about a draft's context.
type GuardInput struct {
	Spec         KindSpec
	Rules        sources.Rules
	BrandName    string
	Banned       []string
	AllowedLinks []string // URLs of the portfolio items the draft may link
}

// Checked is a draft after deterministic enforcement.
type Checked struct {
	Body        string
	IncludeLink bool
	Flags       []RuleFlag
}

// LinksAllowed reports whether a draft in this context may carry a link.
func (g GuardInput) LinksAllowed() bool {
	return g.Spec.LinksAllowed && !g.Rules.NoLinks
}

// Enforce applies the hard rules to one variant: links the community or
// kind forbids are removed (never just flagged), and limits, banned
// phrases, and affiliation rules become flags.
func Enforce(g GuardInput, variant, body string, includeLink bool, portfolioURLs []string) Checked {
	c := Checked{Body: strings.TrimSpace(body)}
	allowed := g.LinksAllowed()
	var keep []string
	if allowed {
		for _, u := range portfolioURLs {
			if slices.Contains(g.AllowedLinks, u) {
				keep = append(keep, u)
			}
		}
	}
	var stripped bool
	c.Body, stripped = StripLinks(c.Body, keep, !allowed)
	c.IncludeLink = allowed && includeLink && len(keep) > 0
	if stripped {
		note := "Removed a link that is not one of the brand's portfolio pages."
		switch {
		case g.Rules.NoLinks:
			note = "Removed a link: this community does not allow links."
		case !g.Spec.LinksAllowed:
			note = "Removed a link: comments carry no links."
		}
		c.Flags = append(c.Flags, RuleFlag{Rule: "no_links", Severity: SevInfo, Note: note, Source: "community"})
	}
	if n, limit := WordCount(c.Body), g.Spec.WordLimit(variant); n > limit {
		c.Flags = append(c.Flags, RuleFlag{Rule: "word_limit", Severity: SevWarning, Note: fmt.Sprintf("%d words; the limit is %d.", n, limit), Source: "style"})
	}
	for _, p := range FindBanned(c.Body, g.Banned) {
		c.Flags = append(c.Flags, RuleFlag{Rule: "banned_phrase", Severity: SevBlock, Note: fmt.Sprintf("Contains %q, which the tone guide bans.", p), Source: "style"})
	}
	if MentionsBrand(c.Body, g.BrandName) {
		if g.Rules.NoSelfPromotion {
			c.Flags = append(c.Flags, RuleFlag{Rule: "no_self_promotion", Severity: SevWarning, Note: "Mentions " + g.BrandName + " in a community that discourages self-promotion.", Source: "community"})
		}
		if !HasDisclosure(c.Body) {
			sev := SevWarning
			if g.Rules.DiscloseAffiliation {
				sev = SevBlock
			}
			c.Flags = append(c.Flags, RuleFlag{Rule: "disclose_affiliation", Severity: sev, Note: "Mentions " + g.BrandName + " without disclosing your affiliation.", Source: "brand"})
		}
	}
	return c
}

// NeedsRevision lists problems worth one regeneration: banned phrases and
// word limits.
func NeedsRevision(flags []RuleFlag) []string {
	var out []string
	for _, f := range flags {
		if f.Rule == "banned_phrase" || f.Rule == "word_limit" {
			out = append(out, f.Note)
		}
	}
	return out
}

// QuoteIn reports whether quote occurs in text, ignoring case, whitespace
// differences, and typographic quotes. Very short quotes do not count.
func QuoteIn(text, quote string) bool {
	norm := func(s string) string {
		s = strings.NewReplacer("“", `"`, "”", `"`, "‘", "'", "’", "'").Replace(strings.ToLower(s))
		return strings.Join(strings.Fields(s), " ")
	}
	q := strings.Trim(norm(quote), ` "'.`)
	return len(q) >= 3 && strings.Contains(norm(text), q)
}
