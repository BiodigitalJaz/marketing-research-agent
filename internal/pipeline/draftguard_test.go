package pipeline

import (
	"slices"
	"strings"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
)

var brandDont = []string{"Claim or imply returns, profits, or performance", "guaranteed", "passive income", "get rich", `"to the moon"`, "Urgency or scarcity language"}

func TestBannedPhrasesAndGuidance(t *testing.T) {
	if got, want := BannedPhrases(brandDont), []string{"guaranteed", "passive income", "get rich", "to the moon"}; !slices.Equal(got, want) {
		t.Errorf("BannedPhrases = %q, want %q", got, want)
	}
	if got, want := GuidanceRules(brandDont), []string{"Claim or imply returns, profits, or performance", "Urgency or scarcity language"}; !slices.Equal(got, want) {
		t.Errorf("GuidanceRules = %q, want %q", got, want)
	}
}

func TestFindBanned(t *testing.T) {
	phrases := BannedPhrases(brandDont)
	tests := []struct {
		text string
		want []string
	}{
		{"Nothing is Guaranteed here.", []string{"guaranteed"}},
		{"This is not passive\nincome.", []string{"passive income"}},
		{"unguaranteed results", nil}, // whole words only
		{"Get rich slowly", []string{"get rich"}},
		{"Plain helpful answer.", nil},
	}
	for _, tt := range tests {
		if got := FindBanned(tt.text, phrases); !slices.Equal(got, tt.want) {
			t.Errorf("FindBanned(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestStripLinks(t *testing.T) {
	keep := []string{"https://benchwright.dev/blog/containment"}
	tests := []struct {
		name      string
		in        string
		bare      bool
		want      string
		removed   bool
		keepLinks []string
	}{
		{"no links", "Use a limit order.", false, "Use a limit order.", false, nil},
		{"url removed", "See https://evil.example/x?a=1. Then retry.", false, "See. Then retry.", true, nil},
		{"markdown link keeps text", "Read [the write-up](https://evil.example/x) first.", false, "Read the write-up first.", true, nil},
		{"kept portfolio url", "Details: https://benchwright.dev/blog/containment.", false, "Details: https://benchwright.dev/blog/containment.", false, keep},
		{"bare host only when forbidden", "Try benchwright.dev today.", true, "Try today.", true, nil},
		{"bare host allowed otherwise", "Try benchwright.dev today.", false, "Try benchwright.dev today.", false, nil},
		{"www", "Go to www.example.org now", false, "Go to now", true, nil},
		{"code names survive", "Node.js and e.g. U.S. markets", true, "Node.js and e.g. U.S. markets", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed := StripLinks(tt.in, tt.keepLinks, tt.bare)
			if got != tt.want || removed != tt.removed {
				t.Errorf("StripLinks = %q, %v; want %q, %v", got, removed, tt.want, tt.removed)
			}
		})
	}
}

func TestEnforce(t *testing.T) {
	const url = "https://benchwright.dev/blog/containment"
	base := GuardInput{Spec: KindSpecs[KindReply], BrandName: "Benchwright", Banned: BannedPhrases(brandDont), AllowedLinks: []string{url}}
	rules := func(g GuardInput, r sources.Rules) GuardInput { g.Rules = r; return g }
	spec := func(g GuardInput, k string) GuardInput { g.Spec = KindSpecs[k]; return g }
	tests := []struct {
		name      string
		g         GuardInput
		variant   string
		body      string
		link      bool
		portfolio []string
		wantLink  bool
		wantBody  string // substring that must survive ("" = skip)
		noBody    string // substring that must be gone
		wantRules []string
	}{
		{"clean reply keeps portfolio link", base, "helpful", "Answer first. I wrote this up: " + url, true, []string{url}, true, url, "", nil},
		{"linking the brand's own page is a mention", base, "helpful", "Answer first. Write-up: " + url, true, []string{url}, true, url, "", []string{"disclose_affiliation"}},
		{"community forbids links: forced off and removed", rules(base, sources.Rules{NoLinks: true}), "helpful", "Answer. " + url, true, []string{url}, false, "Answer.", url, []string{"no_links"}},
		{"comment never links", spec(base, KindComment), "perspective", "Point of view. See benchwright.dev", true, []string{url}, false, "Point of view.", "benchwright.dev", []string{"no_links"}},
		{"invented link removed even when links allowed", base, "helpful", "See https://made.up/page", false, nil, false, "See", "made.up", []string{"no_links"}},
		{"include_link without a linkable portfolio item", base, "helpful", "Answer.", true, nil, false, "", "", nil},
		{"over the short limit", base, "short", strings.Repeat("word ", 71), false, nil, false, "", "", []string{"word_limit"}},
		{"banned phrase blocks", base, "helpful", "Returns are not guaranteed.", false, nil, false, "", "", []string{"banned_phrase"}},
		{"brand mention needs disclosure", base, "helpful", "Benchwright gates orders in code.", false, nil, false, "", "", []string{"disclose_affiliation"}},
		{"disclosed mention is fine", base, "helpful", "Disclosure: I work on Benchwright. It gates orders in code.", false, nil, false, "", "", nil},
		{"self-promotion rule", rules(base, sources.Rules{NoSelfPromotion: true}), "helpful", "I built Benchwright for this.", false, nil, false, "", "", []string{"no_self_promotion"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Enforce(tt.g, tt.variant, tt.body, tt.link, tt.portfolio)
			if c.IncludeLink != tt.wantLink {
				t.Errorf("IncludeLink = %v, want %v", c.IncludeLink, tt.wantLink)
			}
			if tt.wantBody != "" && !strings.Contains(c.Body, tt.wantBody) {
				t.Errorf("body %q lacks %q", c.Body, tt.wantBody)
			}
			if tt.noBody != "" && strings.Contains(c.Body, tt.noBody) {
				t.Errorf("body %q still contains %q", c.Body, tt.noBody)
			}
			var got []string
			for _, f := range c.Flags {
				got = append(got, f.Rule)
			}
			if !slices.Equal(got, tt.wantRules) {
				t.Errorf("flags = %q, want %q", got, tt.wantRules)
			}
		})
	}
}

func TestDisclosureSeverity(t *testing.T) {
	g := GuardInput{Spec: KindSpecs[KindReply], BrandName: "Benchwright", Rules: sources.Rules{DiscloseAffiliation: true}}
	c := Enforce(g, "helpful", "Benchwright does this.", false, nil)
	if len(c.Flags) != 1 || c.Flags[0].Severity != SevBlock {
		t.Fatalf("flags = %+v, want one block", c.Flags)
	}
	if got := NeedsRevision(c.Flags); got != nil {
		t.Errorf("disclosure alone should not trigger a style revision: %q", got)
	}
}

func TestQuoteIn(t *testing.T) {
	body := "I work on Benchwright. Our risk engine refuses orders —\nit doesn’t advise."
	tests := []struct {
		quote string
		want  bool
	}{
		{"risk engine refuses orders", true},
		{"RISK   ENGINE refuses", true},
		{`"it doesn't advise."`, true},
		{"deterministic risk engine", false},
		{"", false},
		{"it", false},
	}
	for _, tt := range tests {
		if got := QuoteIn(body, tt.quote); got != tt.want {
			t.Errorf("QuoteIn(%q) = %v, want %v", tt.quote, got, tt.want)
		}
	}
}

func TestNegativeScope(t *testing.T) {
	body := "Trello rolled out AI agents. " + strings.Repeat("Details about approvals. ", 60) + "It also supports hiring."
	if strings.Contains(negativeScope("Trello agents", body), "hiring") {
		t.Error("a passing mention deep in the article must not trigger negatives")
	}
	if !strings.Contains(negativeScope("Best hiring planning bots", body), "hiring") {
		t.Error("the title is always checked")
	}
	if !strings.Contains(negativeScope("Bots", "Hiring signal groups promise returns. "+body), "Hiring") {
		t.Error("the opening of the body is checked")
	}
}
