package agent

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Cited is a source used in a finished text, under its final marker.
type Cited struct {
	Marker string `json:"marker"` // "[1]"
	Source Source `json:"source"`
}

// Finalized is a text whose citations and charts all resolve.
type Finalized struct {
	Text    string
	Cited   []Cited
	Charts  []Chart
	Dropped []string // markers or chart references that named nothing
}

var (
	// [3] or [3, 5] — not a Markdown link label "[3](...)".
	citeRe  = regexp.MustCompile(`\[(\d{1,3}(?:\s*,\s*\d{1,3})*)\](\()?`)
	chartRe = regexp.MustCompile(`\{\{\s*chart:([A-Za-z0-9_-]+)\s*\}\}`)
	fenceRe = regexp.MustCompile("(?s)```.*?```")
	// [d3] — a dataset id written as if it were a source; datasets are
	// shown as charts, never cited.
	datasetRefRe = regexp.MustCompile(`\[d\d{1,3}\](?:\(\))?`)
)

// Finalize renumbers citation markers 1..k in order of first use, removes
// markers that name no source, and keeps only the charts the text
// references (removing references to charts that do not exist). Code
// blocks are left untouched.
func Finalize(text string, sources []Source, charts []Chart) Finalized {
	byN := map[int]Source{}
	for _, s := range sources {
		byN[s.N] = s
	}
	byChart := map[string]Chart{}
	for _, c := range charts {
		byChart[c.ID] = c
	}
	f := Finalized{}
	renum := map[int]int{}
	usedCharts := []string{}

	rewrite := func(seg string) string {
		seg = datasetRefRe.ReplaceAllStringFunc(seg, func(m string) string {
			f.Dropped = append(f.Dropped, m)
			return ""
		})
		seg = citeRe.ReplaceAllStringFunc(seg, func(m string) string {
			sub := citeRe.FindStringSubmatch(m)
			if sub[2] == "(" { // a Markdown link label, not a citation
				return m
			}
			var out strings.Builder
			for _, part := range strings.Split(sub[1], ",") {
				n, _ := strconv.Atoi(strings.TrimSpace(part))
				src, ok := byN[n]
				if !ok {
					f.Dropped = append(f.Dropped, fmt.Sprintf("[%d]", n))
					continue
				}
				k, seen := renum[n]
				if !seen {
					k = len(renum) + 1
					renum[n] = k
					f.Cited = append(f.Cited, Cited{Marker: fmt.Sprintf("[%d]", k), Source: src})
				}
				fmt.Fprintf(&out, "[%d]", k)
			}
			return out.String()
		})
		return chartRe.ReplaceAllStringFunc(seg, func(m string) string {
			id := chartRe.FindStringSubmatch(m)[1]
			if _, ok := byChart[id]; !ok {
				f.Dropped = append(f.Dropped, m)
				return ""
			}
			if !slices.Contains(usedCharts, id) {
				usedCharts = append(usedCharts, id)
			}
			return "{{chart:" + id + "}}"
		})
	}

	var b strings.Builder
	last := 0
	for _, loc := range fenceRe.FindAllStringIndex(text, -1) {
		b.WriteString(rewrite(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(rewrite(text[last:]))
	f.Text = tidy(b.String())
	for _, id := range usedCharts {
		f.Charts = append(f.Charts, byChart[id])
	}
	if f.Cited == nil {
		f.Cited = []Cited{}
	}
	if f.Charts == nil {
		f.Charts = []Chart{}
	}
	return f
}

var (
	spaceBeforePunct = regexp.MustCompile(`[ \t]+([.,;:!?])`)
	multiSpace       = regexp.MustCompile(`[ \t]{2,}`)
)

func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "|") {
			continue // tables keep their spacing
		}
		lines[i] = strings.TrimRight(spaceBeforePunct.ReplaceAllString(multiSpace.ReplaceAllString(l, " "), "$1"), " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// DropUnknown removes citation markers that name no known source, keeping
// the numbering of real ones (for intermediate notes).
func DropUnknown(text string, sources []Source) (string, int) {
	known := map[int]bool{}
	for _, s := range sources {
		known[s.N] = true
	}
	dropped := len(datasetRefRe.FindAllString(text, -1))
	text = datasetRefRe.ReplaceAllString(text, "")
	out := citeRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := citeRe.FindStringSubmatch(m)
		if sub[2] == "(" {
			return m
		}
		var b strings.Builder
		for _, part := range strings.Split(sub[1], ",") {
			n, _ := strconv.Atoi(strings.TrimSpace(part))
			if known[n] {
				fmt.Fprintf(&b, "[%d]", n)
			} else {
				dropped++
			}
		}
		return b.String()
	})
	return tidy(out), dropped
}

// SourceList renders a "Sources" section for Markdown export.
func SourceList(cited []Cited) string {
	if len(cited) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Sources\n\n")
	for _, c := range cited {
		title := c.Source.Title
		if title == "" {
			title = c.Source.URL
		}
		fmt.Fprintf(&b, "%s %s — %s\n", c.Marker, title, c.Source.URL)
	}
	return b.String()
}
