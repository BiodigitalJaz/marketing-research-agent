package pipeline

import (
	"slices"
	"testing"
)

func TestScoreFlags(t *testing.T) {
	tests := []struct {
		name     string
		llm      []string
		excluded bool
		want     []string
	}{
		{"nil from model", nil, false, []string{}},
		{"model excluded_venue dropped", []string{"excluded_venue", "competitor_mention"}, false, []string{"competitor_mention"}},
		{"venue list adds it", []string{"non_target_region"}, true, []string{"non_target_region", "excluded_venue"}},
		{"no duplicates", []string{"excluded_venue", "hype_or_scam", "hype_or_scam"}, true, []string{"hype_or_scam", "excluded_venue"}},
		{"legacy name renamed", []string{"performance_claim_risk", "restricted_claim_risk"}, false, []string{"restricted_claim_risk"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scoreFlags(tt.llm, tt.excluded); !slices.Equal(got, tt.want) {
				t.Errorf("scoreFlags = %q, want %q", got, tt.want)
			}
		})
	}
}
