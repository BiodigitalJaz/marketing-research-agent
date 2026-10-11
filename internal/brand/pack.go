package brand

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Pack is a portable brand knowledge base: the profile, keywords,
// competitors, ICPs, and portfolio of one brand. Seed imports a pack into an
// empty workspace (seed -brand-file); examples/brand-packs holds samples.
type Pack struct {
	// Name is the brand name, used as the workspace name when one is created.
	Name        string              `json:"name" yaml:"name,omitempty"`
	Profile     Profile             `json:"profile" yaml:"profile"`
	Keywords    PackKeywords        `json:"keywords" yaml:"keywords"`
	Competitors []PackCompetitor    `json:"competitors" yaml:"competitors,omitempty"`
	ICPs        []PackICP           `json:"icps" yaml:"icps,omitempty"`
	Portfolio   []PackPortfolioItem `json:"portfolio" yaml:"portfolio,omitempty"`
}

// PackKeywords are tracked terms (what monitors look for) and negative terms
// (items containing them are dropped).
type PackKeywords struct {
	Track    []string `json:"track" yaml:"track,omitempty"`
	Negative []string `json:"negative" yaml:"negative,omitempty"`
}

type PackCompetitor struct {
	Name    string   `json:"name" yaml:"name,omitempty"`
	Domains []string `json:"domains" yaml:"domains,omitempty"`
	Notes   string   `json:"notes" yaml:"notes,omitempty"`
}

type PackICP struct {
	Name          string `json:"name" yaml:"name,omitempty"`
	ICPDefinition `yaml:",inline"`
}

type PackPortfolioItem struct {
	Kind    string        `json:"kind" yaml:"kind,omitempty"`
	Title   string        `json:"title" yaml:"title,omitempty"`
	URL     string        `json:"url" yaml:"url,omitempty"`
	Summary string        `json:"summary" yaml:"summary,omitempty"`
	Tags    PortfolioTags `json:"tags" yaml:"tags,omitempty"`
}

// ParsePack reads a pack from YAML or JSON. Unknown fields are errors, so a
// typo is reported instead of silently dropped.
func ParsePack(data []byte) (*Pack, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("brand pack: %w", err)
	}
	if doc == nil {
		return nil, errors.New("brand pack is empty")
	}
	if _, ok := doc.(map[string]any); !ok {
		// Usually a file path or a stray line pasted instead of the file's contents.
		return nil, errors.New("brand pack must be YAML or JSON with fields such as name, profile, and keywords; paste the file's contents (not its path) or choose the file")
	}
	j, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("brand pack: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	var p Pack
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("brand pack: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks required fields and allowed values.
func (p *Pack) Validate() error {
	var errs []error
	for i, k := range append(append([]string{}, p.Keywords.Track...), p.Keywords.Negative...) {
		if strings.TrimSpace(k) == "" {
			errs = append(errs, fmt.Errorf("keywords[%d] is empty", i))
		}
	}
	for i, c := range p.Competitors {
		if strings.TrimSpace(c.Name) == "" {
			errs = append(errs, fmt.Errorf("competitors[%d].name is required", i))
		}
	}
	for i, c := range p.ICPs {
		if strings.TrimSpace(c.Name) == "" {
			errs = append(errs, fmt.Errorf("icps[%d].name is required", i))
		}
	}
	for i, it := range p.Portfolio {
		if strings.TrimSpace(it.Title) == "" {
			errs = append(errs, fmt.Errorf("portfolio[%d].title is required", i))
		}
		if !slices.Contains(PortfolioKinds, it.Kind) {
			errs = append(errs, fmt.Errorf("portfolio[%d].kind %q must be one of %s", i, it.Kind, strings.Join(PortfolioKinds, ", ")))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("brand pack: %w", errors.Join(errs...))
	}
	return nil
}
