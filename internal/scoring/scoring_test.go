package scoring

import (
	"math"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestRecency(t *testing.T) {
	for _, tt := range []struct {
		age       time.Duration
		freshness int
		want      float64
	}{
		{0, 48, 1},
		{16 * time.Hour, 48, math.Exp(-1)},
		{48 * time.Hour, 48, math.Exp(-3)},
		{-time.Hour, 48, 1}, // clock skew
		{time.Hour, 0, 0},
	} {
		if got := RecencyScore(tt.age, tt.freshness); !near(got, tt.want) {
			t.Errorf("RecencyScore(%v, %d) = %v, want %v", tt.age, tt.freshness, got, tt.want)
		}
	}
}

func TestDeadline(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	at := func(h float64) *time.Time { d := now.Add(time.Duration(h * float64(time.Hour))); return &d }
	for _, tt := range []struct {
		name string
		d    *time.Time
		want float64
	}{
		{"unknown", nil, 0.6},
		{"past", at(-1), 0},
		{"3h", at(3), 0.3},
		{"15h midpoint", at(15), 0.65},
		{"48h", at(48), 1},
		{"96h", at(96), 1},
		{"14d", at(14 * 24), 0.5},
		{"30d", at(30 * 24), 0.5},
	} {
		if got := DeadlineScore(tt.d, now); !near(got, tt.want) {
			t.Errorf("%s: DeadlineScore = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestReachAndRelevance(t *testing.T) {
	v := func(x float64) *float64 { return &x }
	if ReachScore(nil, 50) != UnknownReach || !near(ReachScore(v(50), 50), 1) || ReachScore(v(5000), 50) != 1 || ReachScore(v(0), 50) != 0 {
		t.Fatal("reach")
	}
	if !near(ReachScore(v(4), 0), math.Log1p(4)/math.Log1p(10)) {
		t.Fatal("reach without p90")
	}
	if RelevanceScore(nil, 0.7) != 0.7 || !near(RelevanceScore(v(0.80), 0.4), 0.7) || !near(RelevanceScore(v(0.30), 1), 0.5) || RelevanceScore(v(0.95), 2) != 1 {
		t.Fatal("relevance")
	}
}

func TestFinal(t *testing.T) {
	// inbound weights from the plan (sum 1.0)
	w := map[string]float64{Relevance: 0.20, Intent: 0.35, Fit: 0.20, Recency: 0.20, Reach: 0.05, Deadline: 0}
	f := Factors{Relevance: 1, Intent: 1, Fit: 1, Recency: 1, Reach: 1}
	if got := Final(w, f); got != 1 {
		t.Fatalf("all ones = %v", got)
	}
	// weights are normalized: doubling all weights changes nothing
	w2 := map[string]float64{}
	for k, x := range w {
		w2[k] = 2 * x
	}
	f2 := Factors{Relevance: 0.5, Intent: 0.8, Fit: 0.2, Recency: 0.9, Reach: 0.4, Deadline: 1}
	if Final(w, f2) != Final(w2, f2) {
		t.Fatal("normalization")
	}
	want := round3((0.20*0.5 + 0.35*0.8 + 0.20*0.2 + 0.20*0.9 + 0.05*0.4) / 1.0)
	if got := Final(w, f2); got != want {
		t.Fatalf("Final = %v want %v (deadline weight 0 must be ignored)", got, want)
	}
	if Final(map[string]float64{}, f2) != 0 || Final(w, Factors{Intent: math.NaN()}) != 0 {
		t.Fatal("degenerate")
	}
}
