package research

import (
	"context"
	"testing"
	"time"
)

func TestVerifyTimeoutLeavesTimeToWrite(t *testing.T) {
	tests := []struct {
		name string
		left time.Duration
	}{
		{"full grace", synthesisGrace},
		{"one minute", time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.left)
			defer cancel()
			got := verifyTimeout(ctx)
			if got <= 0 || got > tt.left/2 {
				t.Errorf("verifyTimeout = %v with %v left; want a positive share under half", got, tt.left)
			}
		})
	}
	if got := verifyTimeout(context.Background()); got != synthesisGrace {
		t.Errorf("no deadline: verifyTimeout = %v, want %v", got, synthesisGrace)
	}
}

func TestSubQuestionDeadline(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		left      time.Duration
		remaining int
		want      time.Duration
	}{
		{"even share of five", 15 * time.Minute, 5, 3 * time.Minute},
		{"last one gets the rest", 4 * time.Minute, 1, 4 * time.Minute},
		{"never below the minimum", 3 * time.Minute, 4, minSubQuestionTime},
		{"past the deadline still gets the minimum", -time.Minute, 2, minSubQuestionTime},
		{"zero remaining is treated as one", 6 * time.Minute, 0, 6 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subQuestionDeadline(now, now.Add(tt.left), tt.remaining).Sub(now); got != tt.want {
				t.Errorf("share = %v, want %v", got, tt.want)
			}
		})
	}
}
