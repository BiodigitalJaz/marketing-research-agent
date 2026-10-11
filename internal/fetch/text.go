package fetch

import (
	"strings"

	"golang.org/x/net/html"
)

// HTMLToText converts an HTML fragment to plain text: tags removed,
// entities decoded, block elements separated by newlines, scripts and
// styles dropped, whitespace collapsed.
func HTMLToText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return collapse(s)
	}
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return collapse(b.String())
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript":
				skip++
			case "br", "p", "div", "li", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "blockquote", "pre":
				b.WriteString("\n")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript":
				if skip > 0 {
					skip--
				}
			case "p", "div", "li", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "blockquote", "pre":
				b.WriteString("\n")
			}
		}
	}
}

// collapse trims each line, collapses runs of spaces, and keeps at most one
// blank line between paragraphs.
func collapse(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	blank := false
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
