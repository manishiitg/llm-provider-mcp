package vertex

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Gemini model name constants
const (
	// Gemini 1.5 Series
	ModelGemini15Pro   = "gemini-1.5-pro"
	ModelGemini15Flash = "gemini-1.5-flash"

	// Gemini 2.0 Series
	ModelGemini20Flash = "gemini-2.0-flash"

	// Gemini 2.5 Series
	ModelGemini25Pro       = "gemini-2.5-pro"
	ModelGemini25Flash     = "gemini-2.5-flash"
	ModelGemini25FlashLite = "gemini-2.5-flash-lite"

	// Gemini 3 Series
	ModelGemini3ProPreview = "gemini-3-pro-preview"
)

// normalizeToBaseModel normalizes Gemini model IDs to base model names
// Strips numeric version suffixes (-\d{3}) and -exp suffix
// Examples:
//   - "gemini-3-pro-preview" -> "gemini-3-pro-preview" (keep preview as it's part of base name)
//   - "gemini-1.5-pro-001" -> "gemini-1.5-pro"
//   - "gemini-2.5-flash-exp" -> "gemini-2.5-flash"
func normalizeToBaseModel(modelID string) string {
	// Remove version suffixes like "-001", "-002", etc.
	versionPattern := regexp.MustCompile(`-\d{3}$`)
	baseModelID := versionPattern.ReplaceAllString(modelID, "")

	// Remove experimental suffixes like "-exp" (but keep "-preview" as it's part of model name)
	baseModelID = strings.TrimSuffix(baseModelID, "-exp")

	return baseModelID
}

// GetVertexGeminiModelMetadata returns model metadata for a given Gemini model ID
func GetVertexGeminiModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	baseModelID := normalizeToBaseModel(modelID)

	models := map[string]llmtypes.ModelMetadata{
		// Gemini 1.5 Pro - 2M context window
		ModelGemini15Pro: {
			ModelID:                    ModelGemini15Pro,
			ModelName:                  "Gemini 1.5 Pro",
			ContextWindow:              2000000, // 2M tokens
			InputCostPer1MTokens:       3.50,
			OutputCostPer1MTokens:      10.50,
			ReasoningCostPer1MTokens:   0.0,  // No separate reasoning tokens
			CachedInputCostPer1MTokens: 0.35, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
		},
		// Gemini 1.5 Flash - 2M context window
		ModelGemini15Flash: {
			ModelID:                    ModelGemini15Flash,
			ModelName:                  "Gemini 1.5 Flash",
			ContextWindow:              2000000, // 2M tokens
			InputCostPer1MTokens:       0.35,
			OutputCostPer1MTokens:      1.05,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.035, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
		},
		// Gemini 2.0 Flash - 1M context window
		ModelGemini20Flash: {
			ModelID:                    ModelGemini20Flash,
			ModelName:                  "Gemini 2.0 Flash",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       0.15,
			OutputCostPer1MTokens:      0.60,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.015, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
		},
		// Gemini 2.5 Pro - 1M context window
		ModelGemini25Pro: {
			ModelID:                    ModelGemini25Pro,
			ModelName:                  "Gemini 2.5 Pro",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       1.25,
			OutputCostPer1MTokens:      10.00,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.125, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
		},
		// Gemini 2.5 Flash - 1M context window
		ModelGemini25Flash: {
			ModelID:                    ModelGemini25Flash,
			ModelName:                  "Gemini 2.5 Flash",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       0.30,
			OutputCostPer1MTokens:      2.50,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.03, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
		},
		// Gemini 2.5 Flash-Lite - 1M context window
		ModelGemini25FlashLite: {
			ModelID:                    ModelGemini25FlashLite,
			ModelName:                  "Gemini 2.5 Flash-Lite",
			ContextWindow:              1000000, // 1M tokens
			InputCostPer1MTokens:       0.10,
			OutputCostPer1MTokens:      0.40,
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.01, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
		},
		// Gemini 3 Pro Preview - 1M context window (estimated, may vary)
		ModelGemini3ProPreview: {
			ModelID:                    ModelGemini3ProPreview,
			ModelName:                  "Gemini 3 Pro Preview",
			ContextWindow:              1000000, // 1M tokens (estimated)
			InputCostPer1MTokens:       1.25,    // Estimated, similar to Gemini 2.5 Pro
			OutputCostPer1MTokens:      10.00,   // Estimated, similar to Gemini 2.5 Pro
			ReasoningCostPer1MTokens:   0.0,
			CachedInputCostPer1MTokens: 0.125, // Cache read pricing (estimated, 90% discount)
			Provider:                   "vertex",
		},
	}

	metadata, exists := models[baseModelID]
	if !exists {
		return nil, fmt.Errorf("unknown Gemini model: %s (normalized from: %s)", baseModelID, modelID)
	}

	// Preserve the original modelID (which may include version suffixes) for consistency
	// with OpenAI/Anthropic adapters
	metadata.ModelID = modelID

	return &metadata, nil
}
