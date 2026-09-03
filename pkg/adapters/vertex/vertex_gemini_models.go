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
	ModelGemini3ProPreview        = "gemini-3-pro-preview"
	ModelGemini31ProPreview       = "gemini-3.1-pro-preview"
	ModelGemini3FlashPreview      = "gemini-3-flash-preview"
	ModelGemini31FlashLitePreview = "gemini-3.1-flash-lite-preview"

	// Gemini 3.5 Series (GA, launched 2026-05-19)
	ModelGemini35FlashLite = "gemini-3.5-flash-lite"

	// Gemini 3.7 Series (GA, launched 2026-08-13)
	ModelGemini37Flash = "gemini-3.7-flash"

	// Gemini 3.8 Series (GA, launched 2026-09-02)
	ModelGemini38Flash = "gemini-3.8-flash"
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
		// Gemini 3.7 Flash - GA, launched 2026-08-13. Successor to Gemini
		// 3.6 Flash, built for coding and agentic workloads.
		// Pricing verified via ai.google.dev/gemini-api/docs/pricing
		// (2026-09-03): introductory rate through 2026-12-31, rising to
		// $1.50/$7.50 input/output (cached $0.15) on 2027-01-01 — revisit
		// this entry before that date.
		// Thinking levels assumed unchanged from Gemini 3.6 Flash (medium,
		// high); not separately confirmed for 3.7 — verify if it turns out
		// 3.7 also gained "low" like 3.8 did.
		ModelGemini37Flash: {
			ModelID:                    ModelGemini37Flash,
			ModelName:                  "Gemini 3.7 Flash",
			ContextWindow:              1048576,
			InputCostPer1MTokens:       0.75,
			OutputCostPer1MTokens:      3.75,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.075,
			Provider:                   "vertex",
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsThinkingLevel:      true,
			ThinkingLevels:             []string{"medium", "high"},
			SupportsReasoningEffort:    false,
		},
		// Gemini 3.8 Flash - GA, launched 2026-09-02. Google's most
		// intelligent Flash model at launch; default Vertex model.
		// Pricing verified via ai.google.dev/gemini-api/docs/pricing
		// (2026-09-03): introductory rate through 2026-12-31, rising to
		// $1.50/$7.50 input/output (cached $0.15) on 2027-01-01 — revisit
		// this entry before that date.
		ModelGemini38Flash: {
			ModelID:                    ModelGemini38Flash,
			ModelName:                  "Gemini 3.8 Flash",
			ContextWindow:              1048576,
			InputCostPer1MTokens:       0.75,
			OutputCostPer1MTokens:      3.75,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.075,
			Provider:                   "vertex",
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsThinkingLevel:      true,
			ThinkingLevels:             []string{"low", "medium", "high"},
			SupportsReasoningEffort:    false,
		},
		// Gemini 3.5 Flash-Lite - GA, launched 2026-07-21.
		ModelGemini35FlashLite: {
			ModelID:                    ModelGemini35FlashLite,
			ModelName:                  "Gemini 3.5 Flash-Lite",
			ContextWindow:              1048576,
			InputCostPer1MTokens:       0.30,
			OutputCostPer1MTokens:      2.50,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.03,
			Provider:                   "vertex",
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsThinkingLevel:      true,
			ThinkingLevels:             []string{"minimal", "medium", "high"},
			SupportsReasoningEffort:    false,
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
