package pipeline

import "github.com/BiodigitalJaz/marketing-research-agent/internal/llm/structured"

// OppTypes are the allowed opportunity types.
var OppTypes = []string{"recommendation_request", "pain_point", "journalist_query", "podcast_guest", "speaking_cfp",
	"guest_post", "rfp", "competitor_complaint", "trending_discussion"}

// Flags an opportunity may carry. The scorer raises all but
// FlagExcludedVenue, which the pipeline sets from the brand's venue list
// (older score prompts may still return it; the schema accepts it so they
// keep validating).
var Flags = []string{FlagRestrictedClaim, "avoided_audience", "non_target_region", "hype_or_scam", FlagExcludedVenue, "competitor_mention"}

// FlagRestrictedClaim: a natural reply would need a claim the brand never
// makes. Score prompts before v3 called it legacyFlagRestrictedClaim; the
// schema still accepts that name and scoreFlags renames it.
const (
	FlagRestrictedClaim       = "restricted_claim_risk"
	legacyFlagRestrictedClaim = "performance_claim_risk"
)

// Schemas follow the strict subset accepted by both adapters: every
// property required, nullable via type unions, no additional properties.
const FlagExcludedVenue = "excluded_venue"

var classifySchema = structured.MustCompile("classify", `{
  "type": "object",
  "additionalProperties": false,
  "required": ["candidates", "not_relevant_reason"],
  "properties": {
    "candidates": {
      "type": "array",
      "maxItems": 3,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["lane", "opp_type", "deadline", "rationale"],
        "properties": {
          "lane": {"type": "string", "enum": ["inbound", "media", "thought_leadership"]},
          "opp_type": {"type": "string", "enum": ["recommendation_request", "pain_point", "journalist_query", "podcast_guest", "speaking_cfp", "guest_post", "rfp", "competitor_complaint", "trending_discussion"]},
          "deadline": {"type": ["string", "null"]},
          "rationale": {"type": "string", "maxLength": 400}
        }
      }
    },
    "not_relevant_reason": {"type": ["string", "null"]}
  }
}`)

var scoreSchema = structured.MustCompile("score", `{
  "type": "object",
  "additionalProperties": false,
  "required": ["relevance", "intent", "fit", "flags", "explanation"],
  "properties": {
    "relevance": {"type": "number", "minimum": 0, "maximum": 1},
    "intent": {"type": "number", "minimum": 0, "maximum": 1},
    "fit": {"type": "number", "minimum": 0, "maximum": 1},
    "flags": {"type": "array", "items": {"type": "string", "enum": ["restricted_claim_risk", "performance_claim_risk", "avoided_audience", "non_target_region", "hype_or_scam", "excluded_venue", "competitor_mention"]}},
    "explanation": {"type": "string", "maxLength": 600}
  }
}`)

type classifyOut struct {
	Candidates []struct {
		Lane      string  `json:"lane"`
		OppType   string  `json:"opp_type"`
		Deadline  *string `json:"deadline"`
		Rationale string  `json:"rationale"`
	} `json:"candidates"`
	NotRelevantReason *string `json:"not_relevant_reason"`
}

type scoreOut struct {
	Relevance   float64  `json:"relevance"`
	Intent      float64  `json:"intent"`
	Fit         float64  `json:"fit"`
	Flags       []string `json:"flags"`
	Explanation string   `json:"explanation"`
}
