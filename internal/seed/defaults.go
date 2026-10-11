package seed

import "github.com/BiodigitalJaz/marketing-research-agent/internal/store"

type laneConfig struct {
	lane      store.Lane
	weights   map[string]float64
	quota     int32
	freshness int32
	minScore  float64
	draftKind string
}

// defaultLaneConfigs are each lane's scoring weights, daily quota,
// freshness window (hours), minimum score, and draft kind.
var defaultLaneConfigs = []laneConfig{
	{store.LaneInbound, map[string]float64{"relevance": 0.20, "intent": 0.35, "fit": 0.20, "recency": 0.20, "reach": 0.05, "deadline": 0}, 5, 48, 0.5, "reply"},
	{store.LaneMedia, map[string]float64{"relevance": 0.20, "intent": 0.10, "fit": 0.30, "recency": 0.05, "reach": 0.10, "deadline": 0.25}, 3, 336, 0.5, "pitch"},
	{store.LaneThoughtLeadership, map[string]float64{"relevance": 0.25, "intent": 0.05, "fit": 0.20, "recency": 0.15, "reach": 0.35, "deadline": 0}, 4, 96, 0.5, "comment"},
}
