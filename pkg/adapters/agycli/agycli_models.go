package agycli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// DefaultModelID tracks the CLI default. Verified against `agy models`
// output on 2026-09-20: Gemini 3.8 Flash (High) is the session default.
const DefaultModelID = "gemini-3.8-flash-high"

type knownAgyModel struct {
	id   string
	name string
	// Per-1M-token USD rates (input, output, cached input) of the Gemini API
	// model behind the effort tier; the same rates as the Vertex registry.
	// Zero leaves the usage unpriced rather than guessed.
	input  float64
	out    float64
	cached float64
}

var knownAgyCLIModels = []knownAgyModel{
	{id: "gemini-3.8-flash-high", name: "Gemini 3.8 Flash (High)", input: 0.75, out: 3.75, cached: 0.075},
	{id: "gemini-3.8-flash-medium", name: "Gemini 3.8 Flash (Medium)", input: 0.75, out: 3.75, cached: 0.075},
	{id: "gemini-3.8-flash-low", name: "Gemini 3.8 Flash (Low)", input: 0.75, out: 3.75, cached: 0.075},
	{id: "gemini-3.7-flash-high", name: "Gemini 3.7 Flash (High)", input: 0.75, out: 3.75, cached: 0.075},
	{id: "gemini-3.7-flash-medium", name: "Gemini 3.7 Flash (Medium)", input: 0.75, out: 3.75, cached: 0.075},
	{id: "gemini-3.7-flash-low", name: "Gemini 3.7 Flash (Low)", input: 0.75, out: 3.75, cached: 0.075},
	{id: "gemini-3.6-flash-high", name: "Gemini 3.6 Flash (High)"},
	{id: "gemini-3.6-flash-medium", name: "Gemini 3.6 Flash (Medium)"},
	{id: "gemini-3.6-flash-low", name: "Gemini 3.6 Flash (Low)"},
	{id: "gemini-3.1-pro-high", name: "Gemini 3.1 Pro (High)", input: 2, out: 12, cached: 0.2},
	{id: "gemini-3.1-pro-low", name: "Gemini 3.1 Pro (Low)", input: 2, out: 12, cached: 0.2},
	{id: "claude-sonnet-4-6", name: "Claude Sonnet 4.6 (Thinking)"},
	{id: "claude-opus-4-6-thinking", name: "Claude Opus 4.6 (Thinking)"},
}

// agyReasoningEffortLevels mirrors the effort tiers baked into agy model
// slugs (high/medium/low suffixes in `agy models` output).
var agyReasoningEffortLevels = []string{"low", "medium", "high"}

// GetAgyModelMetadata returns catalog metadata for known ids. Unknown ids
// pass through with the provider set (the CLI resolves them); context and
// pricing stay zero there, not guesses.
func GetAgyModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = DefaultModelID
	}
	for _, known := range knownAgyCLIModels {
		if known.id == modelID {
			return &llmtypes.ModelMetadata{
				Provider:                   "agy-cli",
				ModelID:                    known.id,
				ModelName:                  known.name,
				ModelSelectionMode:         "dynamic",
				SupportsToolCalls:          true,
				SupportsReasoningEffort:    true,
				ReasoningEffortLevels:      append([]string(nil), agyReasoningEffortLevels...),
				InputCostPer1MTokens:       known.input,
				OutputCostPer1MTokens:      known.out,
				CachedInputCostPer1MTokens: known.cached,
			}, nil
		}
	}
	return &llmtypes.ModelMetadata{
		Provider:  "agy-cli",
		ModelID:   modelID,
		ModelName: "Antigravity (" + modelID + ")",
	}, nil
}

// GetAllAgyCLIModels returns the catalog models for listings and the
// tier-defaults published check.
func GetAllAgyCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownAgyCLIModels))
	adapter := &AgyCLIAdapter{}
	for _, known := range knownAgyCLIModels {
		meta, err := adapter.GetModelMetadata(known.id)
		if err != nil || meta == nil {
			continue
		}
		models = append(models, meta)
	}
	return models
}

// agyAttachCost copies the turn's token counts onto the generation info and
// prices them from the model's rates, so the cost ledger records an estimate
// instead of "unpriced". Unknown or unpriced models keep no estimate.
func agyAttachCost(gi *llmtypes.GenerationInfo, model string, usage llmtypes.Usage) {
	if gi == nil {
		return
	}
	if gi.Additional == nil {
		gi.Additional = map[string]interface{}{}
	}
	if gi.InputTokens == nil && usage.InputTokens > 0 {
		gi.InputTokens = intPtrAgy(usage.InputTokens)
	}
	if gi.OutputTokens == nil && usage.OutputTokens > 0 {
		gi.OutputTokens = intPtrAgy(usage.OutputTokens)
	}
	if gi.CachedContentTokens == nil && usage.CacheTokens != nil && *usage.CacheTokens > 0 {
		gi.CachedContentTokens = intPtrAgy(*usage.CacheTokens)
	}
	if gi.ReasoningTokens == nil && usage.ThoughtsTokens != nil && *usage.ThoughtsTokens > 0 {
		gi.ReasoningTokens = intPtrAgy(*usage.ThoughtsTokens)
	}
	meta, err := GetAgyModelMetadata(model)
	if err != nil || meta == nil {
		return
	}
	if cost := llmtypes.ComputeUSDCostFromMetadata(meta, gi); cost > 0 {
		gi.Additional["cost_usd_estimated"] = cost
		gi.Additional["cost_model_id"] = meta.ModelID
	}
}
