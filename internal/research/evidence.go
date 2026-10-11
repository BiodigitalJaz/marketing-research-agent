package research

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Claim checking compares what the notes say with what the cited sources
// actually said. A source's recorded text can be long, so for each source
// the passage that best matches the sentences citing it is sent to the
// verifier, sized to fit the model's window.

const (
	minExcerptChars = 200
	maxExcerptChars = 700
)

// stopwords are left out when matching sentences to passages.
var stopwords = map[string]bool{
	"about": true, "after": true, "also": true, "been": true, "before": true, "being": true, "between": true,
	"both": true, "could": true, "does": true, "each": true, "even": true, "from": true, "have": true,
	"into": true, "more": true, "most": true, "only": true, "other": true, "over": true, "same": true,
	"some": true, "such": true, "than": true, "that": true, "their": true, "them": true, "then": true,
	"there": true, "these": true, "they": true, "this": true, "those": true, "through": true, "very": true,
	"were": true, "what": true, "when": true, "where": true, "which": true, "while": true, "will": true,
	"with": true, "would": true, "your": true, "source": true, "sources": true,
}

// keywords returns the distinct content words of s (four letters or more,
// lowercased, without stopwords or citation numbers).
func keywords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= 4 && !stopwords[w] && !isDigits(w) {
			out[w] = true
		}
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// sentences splits text at line breaks and sentence ends.
func sentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		start := 0
		for i := 0; i < len(line); i++ {
			if (line[i] == '.' || line[i] == '?' || line[i] == '!') && (i+1 == len(line) || line[i+1] == ' ') {
				if s := strings.TrimSpace(line[start : i+1]); s != "" {
					out = append(out, s)
				}
				start = i + 1
			}
		}
		if s := strings.TrimSpace(line[start:]); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// bestPassage returns the run of consecutive sentences of text, at most
// size characters, sharing the most words with want. Ties go to the
// earliest passage; text that fits is returned whole.
func bestPassage(text string, want map[string]bool, size int) string {
	text = strings.TrimSpace(text)
	if len(text) <= size {
		return text
	}
	ss := sentences(text)
	best, bestScore := "", -1
	for i := range ss {
		var b strings.Builder
		for j := i; j < len(ss); j++ {
			if b.Len()+1+len(ss[j]) > size {
				break
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(ss[j])
		}
		passage := b.String()
		if passage == "" { // a single sentence longer than size
			passage = clip(ss[i], size-1)
		}
		score := 0
		for w := range keywords(passage) {
			if want[w] {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = passage, score
		}
	}
	return best
}

// citingSentences maps each source number to the note sentences that cite it.
func citingSentences(notes string) map[int][]string {
	out := map[int][]string{}
	for _, s := range sentences(notes) {
		for n := range citedIn(s) {
			out[n] = append(out[n], s)
		}
	}
	return out
}

// evidenceSource is what the verifier needs to know about one source.
type evidenceSource struct {
	N          int
	Title, URL string
	Text       string // everything the tools showed for it
}

// renderEvidence lists each source cited in notes with the passage of its
// recorded text that best matches the sentences citing it, within about
// budgetChars in total (0 = the largest excerpts).
func renderEvidence(notes string, srcs []evidenceSource, budgetChars int) string {
	citing := citingSentences(notes)
	var cited []evidenceSource
	for _, s := range srcs {
		if len(citing[s.N]) > 0 {
			cited = append(cited, s)
		}
	}
	if len(cited) == 0 {
		return "(none)\n"
	}
	slices.SortFunc(cited, func(a, b evidenceSource) int { return a.N - b.N })
	size := maxExcerptChars
	if budgetChars > 0 {
		size = min(max(budgetChars/len(cited)-120, minExcerptChars), maxExcerptChars)
	}
	var b strings.Builder
	for _, s := range cited {
		fmt.Fprintf(&b, "[%d] %s — %s\n", s.N, clip(orURL(s.Title, s.URL), 90), clip(s.URL, 120))
		passage := bestPassage(s.Text, keywords(strings.Join(citing[s.N], " ")), size)
		if passage == "" {
			b.WriteString("Excerpt: (no text from this source was seen)\n\n")
			continue
		}
		fmt.Fprintf(&b, "Excerpt: %s\n\n", oneLine(passage))
	}
	return b.String()
}
