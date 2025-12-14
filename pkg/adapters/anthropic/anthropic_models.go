package anthropic

import (
	"fmt"
	"regexp"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Anthropic model name constants
const (
	// Claude 3 Series
	ModelClaude3Opus   = "claude-3-opus"
	ModelClaude3Sonnet = "claude-3-sonnet"
	ModelClaude3Haiku  = "claude-3-haiku"

	// Claude 3.5 Series
	ModelClaude35Sonnet = "claude-3-5-sonnet"
	ModelClaude35Haiku  = "claude-3-5-haiku"

	// Claude 3.7 Series
	ModelClaude37Sonnet = "claude-3-7-sonnet"

	// Claude 4 Series
	ModelClaudeSonnet4 = "claude-sonnet-4"
	ModelClaudeOpus4   = "claude-opus-4"

	// Claude 4.5 Series
	ModelClaudeSonnet45 = "claude-sonnet-4-5"
	ModelClaudeHaiku45  = "claude-haiku-4-5"
	ModelClaudeOpus45   = "claude-opus-4-5"
)

// normalizeToBaseModel normalizes Anthropic model IDs to base model names
// Anthropic models use date suffixes like "-20241022" or "-20250219"
// Examples:
//   - "claude-3-5-sonnet-20241022" -> "claude-3-5-sonnet"
//   - "claude-sonnet-4-5-20250929" -> "claude-sonnet-4-5"
//   - "claude-3-7-sonnet-20250219" -> "claude-3-7-sonnet"
func normalizeToBaseModel(modelID string) string {
	// Remove date suffixes (format: -YYYYMMDD)
	datePattern := regexp.MustCompile(`-\d{8}$`)
	baseModelID := datePattern.ReplaceAllString(modelID, "")

	// Also handle version suffixes like "-v1:0" (used in Bedrock)
	versionPattern := regexp.MustCompile(`-v\d+:\d+$`)
	baseModelID = versionPattern.ReplaceAllString(baseModelID, "")

	return baseModelID
}

// GetAnthropicModelMetadata returns metadata for Anthropic models including token limits and pricing
// Pricing includes input, output, cached input read tokens, and cached input write tokens
// Cache read cost: 10% of base input cost
// Cache write cost: 125% of base input cost (25% more than base)
// Accepts both base model names (e.g., "claude-3-5-sonnet") and versioned names (e.g., "claude-3-5-sonnet-20241022")
// Versioned names are normalized to base model names
func GetAnthropicModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	// Normalize model ID to base model name (remove date/version suffixes)
	baseModelID := normalizeToBaseModel(modelID)

	// Anthropic model metadata with comprehensive pricing
	// Only base model names are stored - versioned names are normalized to base names
	models := map[string]*llmtypes.ModelMetadata{
		// Claude 3 Series
		ModelClaude3Opus: {
			ModelID:                         ModelClaude3Opus,
			ModelName:                       "Claude 3 Opus",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            15.00,
			OutputCostPer1MTokens:           75.00,
			ReasoningCostPer1MTokens:        0.0,   // No separate reasoning tokens
			CachedInputCostPer1MTokens:      1.50,  // Cache read pricing (10% of base input: 15.00 * 0.1)
			CachedInputCostWritePer1MTokens: 18.75, // Cache write pricing (1.25x base input: 15.00 * 1.25)
			Provider:                        "anthropic",
		},
		ModelClaude3Sonnet: {
			ModelID:                         ModelClaude3Sonnet,
			ModelName:                       "Claude 3 Sonnet",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            3.00,
			OutputCostPer1MTokens:           15.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.30, // Cache read pricing (estimated, similar to Sonnet 4.5)
			CachedInputCostWritePer1MTokens: 3.75, // Cache write pricing (1.25x base input: 3.00 * 1.25)
			Provider:                        "anthropic",
		},
		ModelClaude3Haiku: {
			ModelID:                         ModelClaude3Haiku,
			ModelName:                       "Claude 3 Haiku",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            0.25,
			OutputCostPer1MTokens:           1.25,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.025,  // Cache read pricing (10% of base input: 0.25 * 0.1)
			CachedInputCostWritePer1MTokens: 0.3125, // Cache write pricing (1.25x base input: 0.25 * 1.25)
			Provider:                        "anthropic",
		},

		// Claude 3.5 Series
		ModelClaude35Sonnet: {
			ModelID:                         ModelClaude35Sonnet,
			ModelName:                       "Claude 3.5 Sonnet",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            3.00,
			OutputCostPer1MTokens:           15.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.30, // Cache read pricing (for ≤200K tokens)
			CachedInputCostWritePer1MTokens: 3.75, // Cache write pricing (1.25x base input: 3.00 * 1.25)
			Provider:                        "anthropic",
		},
		ModelClaude35Haiku: {
			ModelID:                         ModelClaude35Haiku,
			ModelName:                       "Claude 3.5 Haiku",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            0.80,
			OutputCostPer1MTokens:           4.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.08, // Cache read pricing (10% of base input: 0.80 * 0.1)
			CachedInputCostWritePer1MTokens: 1.00, // Cache write pricing (1.25x base input: 0.80 * 1.25)
			Provider:                        "anthropic",
		},

		// Claude 3.7 Series
		ModelClaude37Sonnet: {
			ModelID:                         ModelClaude37Sonnet,
			ModelName:                       "Claude 3.7 Sonnet",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            3.00,
			OutputCostPer1MTokens:           15.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.30, // Cache read pricing (for ≤200K tokens, similar to Sonnet 4.5)
			CachedInputCostWritePer1MTokens: 3.75, // Cache write pricing (1.25x base input: 3.00 * 1.25)
			Provider:                        "anthropic",
		},

		// Claude 4 Series
		ModelClaudeSonnet4: {
			ModelID:                         ModelClaudeSonnet4,
			ModelName:                       "Claude Sonnet 4",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            3.00,
			OutputCostPer1MTokens:           15.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.30, // Cache read pricing (for ≤200K tokens, similar to Sonnet 4.5)
			CachedInputCostWritePer1MTokens: 3.75, // Cache write pricing (1.25x base input: 3.00 * 1.25)
			Provider:                        "anthropic",
		},
		ModelClaudeOpus4: {
			ModelID:                         ModelClaudeOpus4,
			ModelName:                       "Claude Opus 4",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            5.00,
			OutputCostPer1MTokens:           25.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.50, // Cache read pricing (similar to Opus 4.5)
			CachedInputCostWritePer1MTokens: 6.25, // Cache write pricing (1.25x base input: 5.00 * 1.25)
			Provider:                        "anthropic",
		},

		// Claude 4.5 Series
		ModelClaudeSonnet45: {
			ModelID:                         ModelClaudeSonnet45,
			ModelName:                       "Claude Sonnet 4.5",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            3.00,
			OutputCostPer1MTokens:           15.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.30, // Cache read pricing (for ≤200K tokens)
			CachedInputCostWritePer1MTokens: 3.75, // Cache write pricing (1.25x base input: 3.00 * 1.25)
			Provider:                        "anthropic",
		},
		ModelClaudeHaiku45: {
			ModelID:                         ModelClaudeHaiku45,
			ModelName:                       "Claude Haiku 4.5",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            1.00,
			OutputCostPer1MTokens:           5.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.10, // Cache read pricing
			CachedInputCostWritePer1MTokens: 1.25, // Cache write pricing (1.25x base input: 1.00 * 1.25)
			Provider:                        "anthropic",
		},
		ModelClaudeOpus45: {
			ModelID:                         ModelClaudeOpus45,
			ModelName:                       "Claude Opus 4.5",
			ContextWindow:                   200000, // 200k tokens
			InputCostPer1MTokens:            5.00,
			OutputCostPer1MTokens:           25.00,
			ReasoningCostPer1MTokens:        0.0,
			CachedInputCostPer1MTokens:      0.50, // Cache read pricing
			CachedInputCostWritePer1MTokens: 6.25, // Cache write pricing (1.25x base input: 5.00 * 1.25)
			Provider:                        "anthropic",
		},
	}

	// Look up the model by base model ID
	metadata, exists := models[baseModelID]
	if !exists {
		return nil, fmt.Errorf("unknown Anthropic model: %s (normalized from: %s)", baseModelID, modelID)
	}

	// Return a copy with the original model ID preserved (in case caller needs it)
	result := *metadata
	result.ModelID = modelID // Preserve original model ID if versioned
	return &result, nil
}
