package brand

import "testing"

func TestPortfolioText(t *testing.T) {
	got := PortfolioText("Title", "Summary.", PortfolioTags{Problems: []string{"a", "b"}})
	want := "Title\n\nSummary.\nProblems: a; b"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestICPText(t *testing.T) {
	got := ICPText("AI product engineer", ICPDefinition{Roles: []string{"engineer"}, PainPoints: []string{"fees"}})
	want := "AI product engineer\nRoles: engineer\nPain points: fees"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
