package openai

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// OpenAI model name constants
const (
	// GPT-5.2 family
	ModelGPT52 = "gpt-5.2"
	// GPT-5.2 specialized variants
	ModelGPT52Instant  = "gpt-5.2-instant"
	ModelGPT52Thinking = "gpt-5.2-thinking"
	// GPT-5.5 family
	ModelGPT55 = "gpt-5.5"
	// GPT-5.4 family
	ModelGPT54     = "gpt-5.4"
	ModelGPT54Mini = "gpt-5.4-mini"
	// GPT-4.1 family
	ModelGPT41     = "gpt-4.1"
	ModelGPT41Mini = "gpt-4.1-mini"
	ModelGPT41Nano = "gpt-4.1-nano"
)

// getOpenAIModels returns the map of OpenAI model metadata
func getOpenAIModels() map[string]*llmtypes.ModelMetadata {
	return map[string]*llmtypes.ModelMetadata{
		// GPT-5.2 base (same pricing as Instant/Thinking family; standard capabilities)
		ModelGPT52: {
			ModelID:                    ModelGPT52,
			ModelName:                  "GPT-5.2",
			ContextWindow:              400000, // 400k tokens (input context)
			InputCostPer1MTokens:       1.75,
			OutputCostPer1MTokens:      14.00,
			ReasoningCostPer1MTokens:   0.0,   // No separate reasoning tokens documented yet
			CachedInputCostPer1MTokens: 0.175, // 90% discount
			Provider:                   "openai",
			// Capabilities (conservative defaults; refine as OpenAI docs evolve)
			SupportsToolCalls:     true,
			SupportsJSONMode:      true,
			SupportsThinkingLevel: false,
		},
		// GPT-5.2 Instant - fast, same pricing as base, no explicit reasoning-effort knob
		ModelGPT52Instant: {
			ModelID:                    ModelGPT52Instant,
			ModelName:                  "GPT-5.2 Instant",
			ContextWindow:              400000, // 400k tokens (input context)
			InputCostPer1MTokens:       1.75,
			OutputCostPer1MTokens:      14.00,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.175, // 90% discount
			Provider:                   "openai",
			// Capabilities
			SupportsToolCalls:       true,
			SupportsJSONMode:        true,
			SupportsReasoningEffort: false,
		},
		// GPT-5.2 Thinking - deeper reasoning, same pricing as base, supports reasoning-effort knob
		ModelGPT52Thinking: {
			ModelID:                    ModelGPT52Thinking,
			ModelName:                  "GPT-5.2 Thinking",
			ContextWindow:              400000, // 400k tokens (input context)
			InputCostPer1MTokens:       1.75,
			OutputCostPer1MTokens:      14.00,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.175, // 90% discount
			Provider:                   "openai",
			// Capabilities
			SupportsToolCalls:       true,
			SupportsJSONMode:        true,
			SupportsReasoningEffort: true,
			ReasoningEffortLevels:   []string{"low", "medium", "high"},
		},
		// GPT-5.5 frontier — 1.05M context, highest reasoning
		ModelGPT55: {
			ModelID:                    ModelGPT55,
			ModelName:                  "GPT-5.5",
			ContextWindow:              1050000,
			InputCostPer1MTokens:       5.00,
			OutputCostPer1MTokens:      30.00,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.50,
			Provider:                   "openai",
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"none", "low", "medium", "high", "xhigh"},
		},
		// GPT-5.4 flagship — 1.1M context, strongest reasoning
		ModelGPT54: {
			ModelID:                    ModelGPT54,
			ModelName:                  "GPT-5.4",
			ContextWindow:              1100000, // 1.1M tokens
			InputCostPer1MTokens:       2.50,
			OutputCostPer1MTokens:      15.00,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.25, // 90% discount
			Provider:                   "openai",
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"none", "low", "medium", "high", "xhigh"},
		},
		// GPT-5.4 Mini — 400K context, fast and affordable
		ModelGPT54Mini: {
			ModelID:                    ModelGPT54Mini,
			ModelName:                  "GPT-5.4 Mini",
			ContextWindow:              400000, // 400K tokens
			InputCostPer1MTokens:       0.75,
			OutputCostPer1MTokens:      4.50,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.075, // 90% discount
			Provider:                   "openai",
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"low", "medium", "high", "xhigh"},
		},
		ModelGPT41: {
			ModelID:                    ModelGPT41,
			ModelName:                  "GPT-4.1",
			ContextWindow:              1047576, // ~1M tokens (1,047,576 tokens)
			InputCostPer1MTokens:       3.00,
			OutputCostPer1MTokens:      12.00,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.75, // 75% discount
			Provider:                   "openai",
		},
		ModelGPT41Mini: {
			ModelID:                    ModelGPT41Mini,
			ModelName:                  "GPT-4.1 Mini",
			ContextWindow:              1047576, // ~1M tokens (1,047,576 tokens)
			InputCostPer1MTokens:       0.80,
			OutputCostPer1MTokens:      3.20,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.20, // 75% discount
			Provider:                   "openai",
		},
		ModelGPT41Nano: {
			ModelID:                    ModelGPT41Nano,
			ModelName:                  "GPT-4.1 Nano",
			ContextWindow:              1047576, // ~1M tokens (1,047,576 tokens)
			InputCostPer1MTokens:       0.10,
			OutputCostPer1MTokens:      0.40,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.025, // 75% discount
			Provider:                   "openai",
		},
	}
}

// GetAllOpenAIModels returns a list of all available OpenAI models
func GetAllOpenAIModels() []*llmtypes.ModelMetadata {
	models := getOpenAIModels()
	result := make([]*llmtypes.ModelMetadata, 0, len(models))
	for _, m := range models {
		result = append(result, m)
	}
	return result
}

// GetOpenAIModelMetadata returns metadata for OpenAI models including token limits and pricing
// Pricing includes input, output, reasoning (for reasoning models), and cached input tokens
// Accepts both base model names (e.g., "gpt-4o") and versioned names (e.g., "gpt-4o-2024-11-20")
// Versioned names are normalized to base model names
func GetOpenAIModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	// Normalize model ID to base model name (remove version suffixes)
	baseModelID := normalizeToBaseModel(modelID)

	// OpenAI model metadata with comprehensive pricing
	// Only base model names are stored - versioned names are normalized to base names
	models := getOpenAIModels()

	// Look up by base model name (versioned names are normalized)
	if metadata, exists := models[baseModelID]; exists {
		// Create a copy with the original modelID (preserve versioned name if provided)
		result := *metadata
		result.ModelID = modelID // Keep original modelID (may be versioned)
		return &result, nil
	}

	return nil, fmt.Errorf("model metadata not found for OpenAI model: %s (normalized: %s)", modelID, baseModelID)
}

// normalizeToBaseModel normalizes model IDs to base model names by removing version suffixes
// Examples:
//   - "gpt-4o-2024-08-06" -> "gpt-4o"
//   - "gpt-4o-2024-11-20" -> "gpt-4o"
//   - "gpt-4-turbo-preview" -> "gpt-4-turbo"
//   - "gpt-4o" -> "gpt-4o" (no change)
func normalizeToBaseModel(modelID string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelID))

	// Remove version date patterns (YYYY-MM-DD) using regex to match trailing date suffix
	// e.g., "gpt-4o-2024-08-06" -> "gpt-4o"
	// The regex matches "-YYYY-MM-DD" at the end of the string
	datePattern := regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)
	normalized = datePattern.ReplaceAllString(normalized, "")

	// Remove common suffixes like "-preview"
	normalized = strings.TrimSuffix(normalized, "-preview")

	return normalized
}
