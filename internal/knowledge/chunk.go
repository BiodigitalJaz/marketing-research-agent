// Package knowledge keeps an internal knowledge base: Markdown docs synced
// read-only from a Git repository (main branch), split into citable
// sections, and a memory bank of core summary files generated from them
// (after Cline's memory bank). Research uses it; drafting never does.
package knowledge

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// Chunk is one citable section of a document.
type Chunk struct {
	Heading string // "SECURITY.md › Instance isolation › Network"
	Anchor  string // GitHub heading anchor ("" for the document top)
	Content string
}

// MaxChunkChars bounds a section; longer ones are split at paragraphs.
const MaxChunkChars = 3500

var headingRe = regexp.MustCompile(`^(#{1,3})\s+(.+?)\s*#*\s*$`)

// Title returns a document's first level-1 heading, or its file name.
func Title(docPath, content string) string {
	fenced := false
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if m := headingRe.FindStringSubmatch(line); !fenced && m != nil && len(m[1]) == 1 {
			return strings.TrimSpace(m[2])
		}
	}
	return path.Base(docPath)
}

// Split cuts a Markdown document into sections at level 1–3 headings
// (ignoring headings inside code fences), labelling each with its heading
// path and GitHub anchor. Long sections are split at paragraph breaks.
func Split(docPath, content string) []Chunk {
	base := path.Base(docPath)
	slugs := newSlugger()
	var out []Chunk
	var stack [3]string // h1, h2, h3 texts
	anchor := ""
	var buf strings.Builder
	flush := func() {
		text := strings.TrimSpace(buf.String())
		buf.Reset()
		if text == "" || headingOnly(text) {
			return
		}
		parts := []string{base}
		for _, h := range stack[1:] { // the h1 is the document title
			if h != "" {
				parts = append(parts, h)
			}
		}
		heading := strings.Join(parts, " › ")
		for i, piece := range splitLong(text, MaxChunkChars) {
			h := heading
			if i > 0 {
				h = fmt.Sprintf("%s (part %d)", heading, i+1)
			}
			out = append(out, Chunk{Heading: h, Anchor: anchor, Content: piece})
		}
	}
	fenced := false
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if m := headingRe.FindStringSubmatch(line); !fenced && m != nil {
			flush()
			level := len(m[1])
			text := cleanHeading(m[2])
			stack[level-1] = text
			for i := level; i < len(stack); i++ {
				stack[i] = ""
			}
			anchor = slugs.slug(text)
		}
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	flush()
	return out
}

func headingOnly(text string) bool {
	return !strings.Contains(text, "\n") && headingRe.MatchString(text)
}

var mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// cleanHeading drops Markdown link syntax and emphasis from heading text.
func cleanHeading(s string) string {
	s = mdLinkRe.ReplaceAllString(s, "$1")
	return strings.TrimSpace(strings.NewReplacer("**", "", "`", "", "__", "").Replace(s))
}

// splitLong splits text at blank lines into pieces of at most n characters
// (a single over-long paragraph is cut at n).
func splitLong(text string, n int) []string {
	if len(text) <= n {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		for len(para) > n {
			if cur.Len() > 0 {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
			out = append(out, para[:n])
			para = para[n:]
		}
		if cur.Len()+len(para)+2 > n && cur.Len() > 0 {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(para)
		cur.WriteString("\n\n")
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// slugger reproduces GitHub's heading anchors, including -1, -2 suffixes
// for repeated headings.
type slugger struct{ seen map[string]int }

func newSlugger() *slugger { return &slugger{seen: map[string]int{}} }

func (s *slugger) slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	base := b.String()
	n := s.seen[base]
	s.seen[base] = n + 1
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, n)
}

// Excluded reports whether rel (a path relative to the docs prefix) matches
// one of the patterns. A pattern matches the relative path or the file
// name ("CUSTOMER-LIST.md"), and "dir/**" matches everything under dir.
func Excluded(rel string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(strings.TrimPrefix(p, "/"))
		if p == "" {
			continue
		}
		if dir, ok := strings.CutSuffix(p, "/**"); ok {
			// Everything under a directory matching dir (which may use globs).
			for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
				if m, _ := path.Match(dir, d); m {
					return true
				}
			}
			continue
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		if ok, _ := path.Match(p, path.Base(rel)); ok && !strings.Contains(p, "/") {
			return true
		}
	}
	return false
}

// DefaultExclude pre-fills a new repository's exclude list. It is empty:
// what to keep out of the knowledge base depends on the repository.
var DefaultExclude = []string{}
