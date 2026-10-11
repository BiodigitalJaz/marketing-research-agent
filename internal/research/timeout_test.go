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
