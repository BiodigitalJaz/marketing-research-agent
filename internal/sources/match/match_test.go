package match

import (
	"reflect"
	"testing"
)

func TestMatch(t *testing.T) {
	const text = "Is it safe to let an AI planning bot manage my Trello account? Stop  loss on fractional shares. C++ fans welcome."
	tests := []struct {
		q    string
		want bool
	}{
		{"", true},
		{"trello", true},
		{"TRELLO", true},
		{"robin", false}, // word boundary
		{"AI planning", true},
		{"AI hiring", false},
		{`"stop loss"`, true}, // whitespace run inside phrase
		{`"loss stop"`, false},
		{"basecamp OR trello", true},
		{"basecamp | asana", false},
		{"hiring OR \"planning bot\"", true},
		{"trello -account", false},
		{"trello -hiring", true},
		{"-hiring", true},
		{"-fractional", false},
		{"C++", true}, // punctuation is quoted literally
		{"safe AI OR nothing", true},
	}
	for _, tt := range tests {
		if got := Compile(tt.q).Match(text); got != tt.want {
			t.Errorf("Compile(%q).Match = %v, want %v", tt.q, got, tt.want)
		}
	}
}

func TestAnyOfAndExclusions(t *testing.T) {
	m := AnyOf([]string{"AI planning bot", "Basecamp alternative"}, []string{"telegram"})
	if !m.Match("best ai  planning BOT?") || m.Match("ai planning bot telegram group") || m.Match("nothing here") {
		t.Fatal("AnyOf")
	}
	if AnyOf(nil, nil).Match("") != true {
		t.Fatal("empty AnyOf should match")
	}
	ex := Compile("trello").WithExclusions([]string{"giveaway"})
	if ex.Match("trello giveaway pump") || !ex.Match("trello fees") {
		t.Fatal("WithExclusions")
	}
}

func TestTerms(t *testing.T) {
	got := Terms(`"AI planning" bot OR trello -hiring | basecamp`)
	want := []string{`"AI planning" bot`, "trello", "basecamp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Terms = %q, want %q", got, want)
	}
}
