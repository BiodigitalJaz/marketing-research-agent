package brand

import (
	"strings"
	"testing"
)

func TestParsePack(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string // substring; empty = valid
	}{
		{"minimal", "name: X\n", ""},
		{"json works too", `{"name":"X","keywords":{"track":["a"]}}`, ""},
		{"full icp", "icps:\n  - name: Owner\n    roles: [Founder]\n    pain_points: [time]\n", ""},
		{"empty", "", "empty"},
		{"a file path", "/home/me/brand-pack.yaml\n", "not its path"},
		{"a list", "- a\n- b\n", "not its path"},
		{"unknown field", "name: X\nprofil: {}\n", `unknown field "profil"`},
		{"bad portfolio kind", "portfolio:\n  - {kind: video, title: T}\n", `kind "video"`},
		{"untitled portfolio", "portfolio:\n  - {kind: blog_post}\n", "title is required"},
		{"empty keyword", "keywords: {track: [\"\"]}\n", "keywords[0] is empty"},
		{"unnamed competitor", "competitors:\n  - {domains: [a.com]}\n", "competitors[0].name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParsePack([]byte(tt.in))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
			if tt.name == "full icp" && (p.ICPs[0].Name != "Owner" || p.ICPs[0].Roles[0] != "Founder") {
				t.Errorf("icp not decoded: %+v", p.ICPs)
			}
		})
	}
}
