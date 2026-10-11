package research

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKeywords(t *testing.T) {
	got := keywords("The MCP spec [2] defines tools/call; streaming isn't in 2025.")
	for _, w := range []string{"spec", "defines", "tools", "call", "streaming"} {
		if !got[w] {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	for _, w := range []string{"the", "mcp", "2025", "2", "isn"} {
		if got[w] {
			t.Errorf("%q should be skipped", w)
		}
	}
}

func TestSentences(t *testing.T) {
	got := sentences("First one. Second? Version 1.5 stays.\nNew line without stop")
	want := []string{"First one.", "Second?", "Version 1.5 stays.", "New line without stop"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBestPassage(t *testing.T) {
	text := "Intro about the company and its history. " +
		"Pricing starts at ten dollars per seat per month. " +
		"The API returns results in a single buffered response. " +
		"Support is available by email."
	tests := []struct {
		name, want string
		size       int
		mustHave   string
	}{
		{"fits whole", "pricing", 1000, "Intro about"},
		{"picks the matching sentence", "buffered response results", 70, "single buffered response"},
		{"no match keeps the start", "unrelated words entirely", 60, "Intro about"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bestPassage(text, keywords(tt.want), tt.size)
			if !strings.Contains(got, tt.mustHave) || utf8.RuneCountInString(got) > tt.size {
				t.Errorf("bestPassage(size %d) = %q, want it to contain %q", tt.size, got, tt.mustHave)
			}
		})
	}
	if got := bestPassage(strings.Repeat("x", 500), nil, 100); utf8.RuneCountInString(got) > 100 || got == "" {
		t.Errorf("an over-long sentence is clipped to size: %d chars", utf8.RuneCountInString(got))
	}
}

func TestRenderEvidence(t *testing.T) {
	notes := "Tool results are buffered [1]. Nobody answered the question [3]."
	srcs := []evidenceSource{
		{N: 3, Title: "Is there a way to stream?", URL: "https://q.example/3", Text: "Is there a way to stream a response chunk by chunk?"},
		{N: 1, Title: "Spec", URL: "https://spec.example", Text: strings.Repeat("Unrelated filler text here. ", 40) + "Tool results are returned buffered in one response."},
		{N: 2, Title: "Uncited", URL: "https://u.example", Text: "Never cited."},
		{N: 4, Title: "Empty", URL: "https://e.example"},
	}
	got := renderEvidence(notes+" Also [4].", srcs, 1200)
	if strings.Contains(got, "Uncited") {
		t.Errorf("uncited sources are left out:\n%s", got)
	}
	if strings.Index(got, "[1] Spec") > strings.Index(got, "[3] Is there") {
		t.Errorf("sources are in number order:\n%s", got)
	}
	if !strings.Contains(got, "returned buffered in one response") {
		t.Errorf("the matching passage of a long source is chosen:\n%s", got)
	}
	if !strings.Contains(got, "[4] Empty") || !strings.Contains(got, "(no text from this source was seen)") {
		t.Errorf("a source with no text says so:\n%s", got)
	}
	if renderEvidence("No citations here.", srcs, 0) != "(none)\n" {
		t.Error("notes without citations have no evidence")
	}
}
