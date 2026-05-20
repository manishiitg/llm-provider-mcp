package vertex

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Gemini model name constants
const (
	// Gemini 3 Series
	ModelGemini3ProPreview       = "gemini-3-pro-preview"
	ModelGemini31ProPreview      = "gemini-3.1-pro-preview"
	ModelGemini3FlashPreview     = "gemini-3-flash-preview"
	ModelGemini31FlashLitePreview = "gemini-3.1-flash-lite-preview"

	// Gemini 3.5 Series (GA, launched 2026-05-19)
	ModelGemini35Flash = "gemini-3.5-flash"
)

// normalizeToBaseModel normalizes Gemini model IDs to base model names
// Strips numeric version suffixes (-\d{3}) and -exp suffix
// Examples:
//   - "gemini-3-pro-preview" -> "gemini-3-pro-preview" (keep preview as it's part of base name)
//   - "gemini-3-flash-preview" -> "gemini-3-flash-preview" (keep preview as it's part of base name)
//   - "gemini-3.5-flash-001" -> "gemini-3.5-flash"
//   - "gemini-3-flash-preview-exp" -> "gemini-3-flash-preview"
func normalizeToBaseModel(modelID string) string {
	// Remove version suffixes like "-001", "-002", etc.
	versionPattern := regexp.MustCompile(`-\d{3}$`)
	baseModelID := versionPattern.ReplaceAllString(modelID, "")

	// Remove experimental suffixes like "-exp" (but keep "-preview" as it's part of model name)
	baseModelID = strings.TrimSuffix(baseModelID, "-exp")

	return baseModelID
}

// getVertexGeminiModels returns the map of Vertex Gemini model metadata
func getVertexGeminiModels() map[string]llmtypes.ModelMetadata {
	return map[string]llmtypes.ModelMetadata{
		// Gemini 3 Pro Preview - 1M context window
		// Pricing tiered by input length (verified May 2026):
		//   ≤200K tokens: $2.00 input / $12.00 output / $0.20 cached
		//   >200K tokens: $4.00 input / $18.00 output  (long-context tier)
		// The registry below only tracks the ≤200K tier — ModelMetadata
		// has no field for the long-context tier today. Cost math under-
		// estimates by 2× when prompts exceed 200K tokens.
		ModelGemini3ProPreview: {
			ModelID:                    ModelGemini3ProPreview,
			ModelName:                  "Gemini 3 Pro Preview",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       2.00,    // ≤200K tier
			OutputCostPer1MTokens:      12.00,   // ≤200K tier
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.20, // Cache read pricing (≤200K tier)
			Provider:                   "vertex",
			// Capabilities
			SupportsToolCalls:       true,
			SupportsJSONMode:        true,
			SupportsThinkingLevel:   true,
			ThinkingLevels:          []string{"low", "high"},
			SupportsReasoningEffort: false,
		},
		// Gemini 3.1 Pro Preview - 1M context window
		// Pricing verified May 2026: same tier structure as Gemini 3 Pro Preview
		// (≤200K: $2.00/$12.00/$0.20 cached). Long-context (>200K) tier not tracked.
		ModelGemini31ProPreview: {
			ModelID:                    ModelGemini31ProPreview,
			ModelName:                  "Gemini 3.1 Pro Preview",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       2.00,    // ≤200K tier
			OutputCostPer1MTokens:      12.00,   // ≤200K tier
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.20, // Cache read pricing (≤200K tier)
			Provider:                   "vertex",
			// Capabilities
			SupportsToolCalls:       true,
			SupportsJSONMode:        true,
			SupportsThinkingLevel:   true,
			ThinkingLevels:          []string{"low", "medium", "high"},
			SupportsReasoningEffort: false,
		},
		// Gemini 3.1 Flash-Lite Preview - 1M context window (launched March 2026)
		ModelGemini31FlashLitePreview: {
			ModelID:                    ModelGemini31FlashLitePreview,
			ModelName:                  "Gemini 3.1 Flash-Lite Preview",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       0.25,
			OutputCostPer1MTokens:      1.50,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.025, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
			// Capabilities
			SupportsToolCalls:       true,
			SupportsJSONMode:        true,
			SupportsThinkingLevel:   false,
			ThinkingLevels:          nil,
			SupportsReasoningEffort: false,
		},
		// Gemini 3.5 Flash - GA, launched 2026-05-19. New default Vertex model.
		// Pricing: https://simonwillison.net/2026/May/19/gemini-35-flash/
		// 3× the price of gemini-3-flash-preview ($0.50/$3.00) — Google
		// positioned it as the new general-purpose default.
		ModelGemini35Flash: {
			ModelID:                    ModelGemini35Flash,
			ModelName:                  "Gemini 3.5 Flash",
			ContextWindow:              1000000, // 1M tokens (1,048,576)
			InputCostPer1MTokens:       1.50,
			OutputCostPer1MTokens:      9.00,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.15,
			Provider:                   "vertex",
			// Capabilities
			SupportsToolCalls:       true,
			SupportsJSONMode:        true,
			// Dynamic thinking is enabled by default server-side; the
			// Vertex API does not currently expose a thinkingLevel /
			// thinkingBudget knob for this model. Flip to true here if
			// Google later exposes one.
			SupportsThinkingLevel:   false,
			ThinkingLevels:          nil,
			SupportsReasoningEffort: false,
		},
		// Gemini 3 Flash Preview - 1M context window
		ModelGemini3FlashPreview: {
			ModelID:                    ModelGemini3FlashPreview,
			ModelName:                  "Gemini 3 Flash Preview",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       0.50,    // Pricing from https://blog.google/products/gemini/gemini-3-flash/
			OutputCostPer1MTokens:      3.00,    // Pricing from https://blog.google/products/gemini/gemini-3-flash/
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.05, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
			// Capabilities
			SupportsToolCalls:       true,
			SupportsJSONMode:        true,
			SupportsThinkingLevel:   false, // Not documented for Flash; reserved for Pro
			ThinkingLevels:          nil,
			SupportsReasoningEffort: false,
		},
	}
}

// GetAllVertexGeminiModels returns a list of all available Vertex Gemini models
func GetAllVertexGeminiModels() []*llmtypes.ModelMetadata {
	models := getVertexGeminiModels()
	result := make([]*llmtypes.ModelMetadata, 0, len(models))
	for _, m := range models {
		// Make a copy to avoid referencing loop variable
		metadata := m
		result = append(result, &metadata)
	}
	return result
}

// GetVertexGeminiModelMetadata returns model metadata for a given Gemini model ID
func GetVertexGeminiModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	baseModelID := normalizeToBaseModel(modelID)

	models := getVertexGeminiModels()

	metadata, exists := models[baseModelID]
	if !exists {
		return nil, fmt.Errorf("unknown Gemini model: %s (normalized from: %s)", baseModelID, modelID)
	}

	// Preserve the original modelID (which may include version suffixes) for consistency
	// with OpenAI/Anthropic adapters
	metadata.ModelID = modelID

	return &metadata, nil
}
