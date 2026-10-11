package seed

import (
	"strings"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
)

// knownModel holds declared defaults for models referenced by config
// routes. Admins can change all of it in Settings.
type knownModel struct {
	caps  llm.Capabilities
	price router.Price
}

// Claude defaults from the current model table (2026-09): effort-era models
// take adaptive thinking + effort and reject sampling params; Haiku 4.5
// takes a thinking token budget and accepts sampling.
var claudeModels = map[string]knownModel{
	"claude-fable-5-1":  {llm.Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "effort", MaxContext: 1_000_000, MaxOutput: 128_000}, router.Price{InputPerMTok: 10, OutputPerMTok: 50}},
	"claude-opus-5-5":   {llm.Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "effort", MaxContext: 1_000_000, MaxOutput: 128_000}, router.Price{InputPerMTok: 4, OutputPerMTok: 20}},
	"claude-opus-5":     {llm.Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "effort", MaxContext: 1_000_000, MaxOutput: 128_000}, router.Price{InputPerMTok: 5, OutputPerMTok: 25}},
	"claude-sonnet-5-5": {llm.Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "effort", MaxContext: 1_000_000, MaxOutput: 128_000}, router.Price{InputPerMTok: 2, OutputPerMTok: 10}},
	"claude-sonnet-5":   {llm.Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "effort", MaxContext: 1_000_000, MaxOutput: 128_000}, router.Price{InputPerMTok: 2, OutputPerMTok: 10}},
	"claude-haiku-4-5":  {llm.Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "budget", Sampling: true, MaxContext: 200_000, MaxOutput: 64_000}, router.Price{InputPerMTok: 1, OutputPerMTok: 5}},
}

// defaultModel returns declared defaults for a provider/model pair. Unknown
// local models get tools + structured output + sampling; the admin
// confirms vision and thinking in Settings.
func defaultModel(kind, model string) knownModel {
	if kind == "anthropic" {
		for id, km := range claudeModels {
			if model == id || strings.HasPrefix(model, id+"-") { // dated snapshot ids
				return km
			}
		}
		return knownModel{caps: llm.Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "effort"}}
	}
	return knownModel{caps: llm.Capabilities{Tools: true, JSONSchema: true, Sampling: true}}
}
