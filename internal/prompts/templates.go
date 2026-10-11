package prompts

import (
	"embed"
	"path"
	"strings"
)

//go:embed templates/*.md
var templatesFS embed.FS

// Templates are the report outlines, by name.
var Templates = []string{"freeform", "competitor_teardown", "market_scan", "pain_points", "topic_deep_dive"}

// Template returns a report outline; unknown names fall back to freeform.
func Template(name string) string {
	b, err := templatesFS.ReadFile(path.Join("templates", name+".md"))
	if err != nil {
		b, _ = templatesFS.ReadFile("templates/freeform.md")
	}
	return strings.TrimSpace(string(b))
}
