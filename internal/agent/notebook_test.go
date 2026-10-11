package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNotebookEvidence(t *testing.T) {
	nb := NewNotebook()
	n := nb.Cite(Source{Kind: "web", URL: "https://a.example", Title: "A"})
	nb.Record(n, "snippet text")
	nb.Record(n, "snippet text")              // repeated: kept once
	nb.Record(n, "text")                      // contained in earlier text: skipped
	nb.Record(n, "full page with more words") // new text: appended
	nb.Record(99, "no such source")
	nb.Record(n, "   ")
	if got := nb.Evidence(n); got != "snippet text\n\nfull page with more words" {
		t.Errorf("evidence = %q", got)
	}
	if nb.Evidence(99) != "" {
		t.Error("unknown sources have no evidence")
	}
	nb.Record(n, strings.Repeat("é", maxEvidenceChars)) // capped, cut on a rune boundary
	if got := nb.Evidence(n); len(got) > maxEvidenceChars+4 || !utf8.ValidString(got) {
		t.Errorf("evidence is capped and valid UTF-8: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}
