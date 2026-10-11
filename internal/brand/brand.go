// Package brand defines the brand knowledge base shapes (stored as JSONB)
// and the text used to embed portfolio items and ICPs.
package brand

import "strings"

// Profile is brand_profiles.profile.
type Profile struct {
	Services        []string  `json:"services" yaml:"services,omitempty"`
	ValueProps      []string  `json:"value_props" yaml:"value_props,omitempty"`
	Differentiators []string  `json:"differentiators" yaml:"differentiators,omitempty"`
	Tone            Tone      `json:"tone" yaml:"tone,omitempty"`
	Targeting       Targeting `json:"targeting" yaml:"targeting,omitempty"`
}

// Targeting steers classification and scoring: who and what to favor or
// avoid, and where the brand will not engage.
type Targeting struct {
	Prioritize []string `json:"prioritize" yaml:"prioritize,omitempty"` // audiences/situations to score up
	Avoid      []string `json:"avoid" yaml:"avoid,omitempty"`           // audiences/situations to score down
	Geography  []string `json:"geography" yaml:"geography,omitempty"`
	// ExcludedVenues are communities the brand will not post in (listening
	// only): they never yield inbound opportunities.
	ExcludedVenues []string `json:"excluded_venues" yaml:"excluded_venues,omitempty"`
	Topics         []string `json:"topics" yaml:"topics,omitempty"`               // thought-leadership themes to own
	MediaTargets   []string `json:"media_targets" yaml:"media_targets,omitempty"` // valuable outlets/opportunity types
}

type Tone struct {
	Voice    string   `json:"voice" yaml:"voice,omitempty"`
	Do       []string `json:"do" yaml:"do,omitempty"`
	Dont     []string `json:"dont" yaml:"dont,omitempty"`
	Examples []string `json:"examples" yaml:"examples,omitempty"`
}

// PortfolioTags is portfolio_items.tags.
type PortfolioTags struct {
	Industries []string `json:"industries" yaml:"industries,omitempty"`
	Services   []string `json:"services" yaml:"services,omitempty"`
	Problems   []string `json:"problems" yaml:"problems,omitempty"`
}

// ICPDefinition is icps.definition.
type ICPDefinition struct {
	Industries    []string `json:"industries" yaml:"industries,omitempty"`
	CompanySizes  []string `json:"company_sizes" yaml:"company_sizes,omitempty"`
	Roles         []string `json:"roles" yaml:"roles,omitempty"`
	PainPoints    []string `json:"pain_points" yaml:"pain_points,omitempty"`
	BuyingSignals []string `json:"buying_signals" yaml:"buying_signals,omitempty"`
}

// PortfolioKinds are the allowed portfolio_items.kind values.
var PortfolioKinds = []string{"case_study", "blog_post", "work_sample", "talk", "other"}

// PortfolioText is the text embedded for a portfolio item.
func PortfolioText(title, summary string, tags PortfolioTags) string {
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(summary)
	writeList(&b, "Industries", tags.Industries)
	writeList(&b, "Services", tags.Services)
	writeList(&b, "Problems", tags.Problems)
	return strings.TrimSpace(b.String())
}

// ICPText is the text embedded for an ideal customer profile.
func ICPText(name string, d ICPDefinition) string {
	var b strings.Builder
	b.WriteString(name)
	writeList(&b, "Industries", d.Industries)
	writeList(&b, "Company sizes", d.CompanySizes)
	writeList(&b, "Roles", d.Roles)
	writeList(&b, "Pain points", d.PainPoints)
	writeList(&b, "Buying signals", d.BuyingSignals)
	return strings.TrimSpace(b.String())
}

func writeList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString("\n")
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(strings.Join(items, "; "))
}
