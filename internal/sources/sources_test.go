package sources

import (
	"bytes"
	"strings"
	"testing"
)

func TestNormalizeAndHash(t *testing.T) {
	it := Normalize(Item{Title: "  <b>AI</b>   planning ", Body: "<p>Line one</p>\n\n\n<p>Line   two</p>" + strings.Repeat("x", MaxBodyRunes)})
	if it.Title != "AI planning" || !strings.HasPrefix(it.Body, "Line one\n\nLine two") || len([]rune(it.Body)) != MaxBodyRunes || it.Metrics == nil {
		t.Fatalf("normalize: %q %q", it.Title, it.Body[:30])
	}
	a := ContentHash(Item{Title: "Hello World", Body: "Same  body"})
	b := ContentHash(Item{Title: "hello world", Body: "same body"})
	c := ContentHash(Item{Title: "hello world", Body: "different"})
	if !bytes.Equal(a, b) || bytes.Equal(a, c) {
		t.Fatal("content hash should ignore case/whitespace but not content")
	}
}

func TestDecodeConfigStrict(t *testing.T) {
	var v struct {
		URL string `json:"url"`
	}
	if err := DecodeConfig([]byte(`{"url":"x","nope":1}`), &v); err == nil {
		t.Fatal("unknown field must error")
	}
	if err := DecodeConfig(nil, &v); err != nil {
		t.Fatal(err)
	}
}

func TestRulesInConfig(t *testing.T) {
	type cfg struct {
		FeedURL string `json:"feed_url"`
	}
	tests := []struct {
		name    string
		raw     string
		want    Rules
		wantErr string
	}{
		{"no rules", `{"feed_url":"u"}`, Rules{}, ""},
		{"rules accepted by every connector", `{"feed_url":"u","rules":{"no_links":true,"notes":[" Be kind ",""]}}`, Rules{NoLinks: true, Notes: []string{"Be kind"}}, ""},
		{"null rules", `{"rules":null}`, Rules{}, ""},
		{"unknown rule", `{"rules":{"no_emoji":true}}`, Rules{}, "invalid rules"},
		{"note too long", `{"rules":{"notes":["` + strings.Repeat("x", MaxRuleNoteChars+1) + `"]}}`, Rules{}, "limited to"},
		{"other fields still strict", `{"feed":"u","rules":{}}`, Rules{}, "invalid config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c cfg
			err := DecodeConfig([]byte(tt.raw), &c)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := RulesFromConfig([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got.NoLinks != tt.want.NoLinks || strings.Join(got.Notes, "|") != strings.Join(tt.want.Notes, "|") {
				t.Errorf("rules = %+v, want %+v", got, tt.want)
			}
		})
	}
}
