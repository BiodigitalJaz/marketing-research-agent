// Package scoring computes opportunity score factors and the final
// weighted score. Everything here is pure.
package scoring

import (
	"math"
	"time"
)

// Factor names, matching lane_configs.weights keys.
const (
	Relevance = "relevance"
	Intent    = "intent"
	Fit       = "fit"
	Recency   = "recency"
	Reach     = "reach"
	Deadline  = "deadline"
)

// Factors are each in [0, 1].
type Factors map[string]float64

// RecencyScore decays with age: exp(-age / (freshness/3)). Items older than
// the lane's freshness window score ~0.05.
func RecencyScore(age time.Duration, freshnessHours int) float64 {
	if freshnessHours <= 0 {
		return 0
	}
	if age < 0 {
		age = 0
	}
	return clamp(math.Exp(-age.Hours() / (float64(freshnessHours) / 3)))
}

// DeadlineScore (media lane): 1.0 when the deadline is 24-96h away,
// tapering to 0.3 under 6h, 0.5 beyond 14 days, 0.6 if unknown, 0 if past.
func DeadlineScore(deadline *time.Time, now time.Time) float64 {
	if deadline == nil {
		return 0.6
	}
	h := deadline.Sub(now).Hours()
	switch {
	case h <= 0:
		return 0
	case h < 6:
		return 0.3
	case h < 24:
		return 0.3 + 0.7*(h-6)/18 // 6h -> 0.3, 24h -> 1.0
	case h <= 96:
		return 1
	case h <= 14*24:
		return 1 - 0.5*(h-96)/(14*24-96) // 96h -> 1.0, 14d -> 0.5
	default:
		return 0.5
	}
}

// UnknownReach is used when an item has no engagement metrics (feeds, alerts).
const UnknownReach = 0.4

// ReachScore log-scales engagement against the community's p90:
// log1p(v)/log1p(p90), capped at 1. A nil value is UnknownReach.
func ReachScore(value *float64, p90 float64) float64 {
	if value == nil {
		return UnknownReach
	}
	if p90 <= 0 {
		p90 = 10 // no history: treat 10 as a strong signal
	}
	return clamp(math.Log1p(math.Max(*value, 0)) / math.Log1p(p90))
}

// Cosine similarity range mapped onto [0, 1] for relevance. Typical
// embedding similarities for related-but-not-identical text sit in this band.
const (
	cosineFloor = 0.30
	cosineCeil  = 0.80
)

// RelevanceScore blends embedding similarity (rescaled) 50/50 with the
// LLM's relevance judgment; without an embedding it is the LLM value.
func RelevanceScore(maxCosine *float64, llm float64) float64 {
	llm = clamp(llm)
	if maxCosine == nil {
		return llm
	}
	sim := clamp((*maxCosine - cosineFloor) / (cosineCeil - cosineFloor))
	return clamp(0.5*sim + 0.5*llm)
}

// Final is the weighted sum of factors with weights normalized to sum to 1.
// Missing factors count as 0; non-positive weights are ignored.
func Final(weights map[string]float64, f Factors) float64 {
	var total, sum float64
	for k, w := range weights {
		if w <= 0 {
			continue
		}
		total += w
		sum += w * clamp(f[k])
	}
	if total == 0 {
		return 0
	}
	return round3(sum / total)
}

func clamp(v float64) float64 {
	switch {
	case math.IsNaN(v):
		return 0
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
