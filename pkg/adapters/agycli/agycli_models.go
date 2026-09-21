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
}

var knownAgyCLIModels = []knownAgyModel{
	{id: "gemini-3.8-flash-high", name: "Gemini 3.8 Flash (High)"},
	{id: "gemini-3.8-flash-medium", name: "Gemini 3.8 Flash (Medium)"},
	{id: "gemini-3.8-flash-low", name: "Gemini 3.8 Flash (Low)"},
	{id: "gemini-3.7-flash-high", name: "Gemini 3.7 Flash (High)"},
	{id: "gemini-3.7-flash-medium", name: "Gemini 3.7 Flash (Medium)"},
	{id: "gemini-3.7-flash-low", name: "Gemini 3.7 Flash (Low)"},
	{id: "gemini-3.6-flash-high", name: "Gemini 3.6 Flash (High)"},
	{id: "gemini-3.6-flash-medium", name: "Gemini 3.6 Flash (Medium)"},
	{id: "gemini-3.6-flash-low", name: "Gemini 3.6 Flash (Low)"},
	{id: "gemini-3.1-pro-high", name: "Gemini 3.1 Pro (High)"},
	{id: "gemini-3.1-pro-low", name: "Gemini 3.1 Pro (Low)"},
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
				Provider:                "agy-cli",
				ModelID:                 known.id,
				ModelName:               known.name,
				ModelSelectionMode:      "dynamic",
				SupportsToolCalls:       true,
				SupportsReasoningEffort: true,
				ReasoningEffortLevels:   append([]string(nil), agyReasoningEffortLevels...),
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
