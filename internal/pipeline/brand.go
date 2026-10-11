package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// maxBrandChars bounds the brand summary sent with every classify/score call.
const maxBrandChars = 3000

// BrandContext is what prompts know about the brand.
type BrandContext struct {
	Name      string
	Summary   string
	Targeting brand.Targeting
	Tone      brand.Tone
}

// LoadBrand builds the brand summary from the brand profile, ICPs, and
// competitors.
func LoadBrand(ctx context.Context, q *store.Queries, ws uuid.UUID) (BrandContext, error) {
	bc := BrandContext{Name: "the brand"}
	if w, err := q.GetWorkspace(ctx, ws); err == nil {
		bc.Name = w.Name
	}
	var p brand.Profile
	row, err := q.GetBrandProfile(ctx, ws)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return bc, err
	default:
		if err := json.Unmarshal(row.Profile, &p); err != nil {
			return bc, fmt.Errorf("decode brand profile: %w", err)
		}
	}
	bc.Targeting, bc.Tone = p.Targeting, p.Tone
	var b strings.Builder
	section := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", label)
		for _, it := range items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
	}
	section("What it offers", p.Services)
	section("Value", p.ValueProps)
	section("Differentiators", p.Differentiators)
	section("Prioritize", p.Targeting.Prioritize)
	section("Avoid", p.Targeting.Avoid)
	section("Regions served", p.Targeting.Geography)
	section("Venues where it does not post (listening only)", p.Targeting.ExcludedVenues)
	section("Themes it wants to own", p.Targeting.Topics)
	section("Valuable media opportunities", p.Targeting.MediaTargets)
	section("Claims it never makes", p.Tone.Dont)

	icps, err := q.ListICPs(ctx, store.ListICPsParams{WorkspaceID: ws})
	if err != nil {
		return bc, err
	}
	for _, i := range icps {
		var d brand.ICPDefinition
		_ = json.Unmarshal(i.Definition, &d)
		fmt.Fprintf(&b, "Ideal customer: %s", i.Name)
		if len(d.PainPoints) > 0 {
			fmt.Fprintf(&b, " (pain points: %s)", strings.Join(d.PainPoints, "; "))
		}
		b.WriteString("\n")
	}
	comps, err := q.ListCompetitors(ctx, ws)
	if err != nil {
		return bc, err
	}
	if len(comps) > 0 {
		names := make([]string, len(comps))
		for i, c := range comps {
			names[i] = c.Name
		}
		fmt.Fprintf(&b, "Competitors: %s\n", strings.Join(names, ", "))
	}
	bc.Summary = truncate(strings.TrimSpace(b.String()), maxBrandChars)
	return bc, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// excludedVenue reports whether an item's community/source matches one of
// the brand's excluded venues (case-insensitive substring either way, so
// "Hacker News" matches "news.ycombinator.com"-style labels configured as
// "Hacker News" and vice versa).
func excludedVenue(t brand.Targeting, community, sourceName, connector string) bool {
	candidates := []string{strings.ToLower(community), strings.ToLower(sourceName)}
	if connector == "hn" {
		candidates = append(candidates, "hacker news")
	}
	for _, v := range t.ExcludedVenues {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		for _, c := range candidates {
			if c != "" && (strings.Contains(c, v) || strings.Contains(v, c)) {
				return true
			}
		}
	}
	return false
}
