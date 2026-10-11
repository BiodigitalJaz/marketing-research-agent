package knowledge

import (
	"strings"
	"testing"
)

const doc = "# Security — design decisions\n\nIntro text.\n\n## Instance isolation\n\nEach customer gets a namespace.\n\n### Network\n\nDefault deny.\n\n```sh\n# not a heading\necho hi\n```\n\n## Instance isolation\n\nRepeated heading.\n\n## Empty\n## [Linked](x.md) `code` heading\n\nBody.\n"

func TestSplit(t *testing.T) {
	cs := Split("docs/SECURITY.md", doc)
	var got []string
	for _, c := range cs {
		got = append(got, c.Heading+"#"+c.Anchor)
	}
	want := []string{
		"SECURITY.md#security--design-decisions",
		"SECURITY.md › Instance isolation#instance-isolation",
		"SECURITY.md › Instance isolation › Network#network",
		"SECURITY.md › Instance isolation#instance-isolation-1",
		"SECURITY.md › Linked code heading#linked-code-heading",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("chunks:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(cs[2].Content, "# not a heading") {
		t.Error("headings inside code fences must stay in their section")
	}
	if Title("docs/SECURITY.md", doc) != "Security — design decisions" || Title("docs/x.md", "no heading") != "x.md" {
		t.Error("title")
	}
}

func TestSplitLong(t *testing.T) {
	long := "## Big\n\n" + strings.Repeat(strings.Repeat("word ", 150)+"\n\n", 10)
	cs := Split("docs/a.md", long)
	if len(cs) < 2 || !strings.HasSuffix(cs[1].Heading, "(part 2)") {
		t.Fatalf("long section not split: %d chunks", len(cs))
	}
	for _, c := range cs {
		if len(c.Content) > MaxChunkChars {
			t.Errorf("chunk of %d chars", len(c.Content))
		}
	}
}

func TestExcluded(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{"INTERNAL-RESULTS.md", true},
		{"sub/INTERNAL-RESULTS.md", true},
		{"SECURITY.md", false},
		{"spikes/2026-09-05-oauth/README.md", false},
		{"spikes/2026-09-05-oauth/capture/README.md", true},
	}
	exclude := []string{"INTERNAL-RESULTS.md", "spikes/*/capture/**"}
	for _, tt := range tests {
		if got := Excluded(tt.rel, exclude); got != tt.want {
			t.Errorf("Excluded(%q) = %v, want %v", tt.rel, got, tt.want)
		}
	}
}

func TestBlobURL(t *testing.T) {
	if got := BlobURL("o/r", "abc", "docs/SECURITY.md", "network"); got != "https://github.com/o/r/blob/abc/docs/SECURITY.md#network" {
		t.Error(got)
	}
}
