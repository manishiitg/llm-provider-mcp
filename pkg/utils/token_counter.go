package utils

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/pkoukk/tiktoken-go"
)

// TokenCounter provides provider/model-aware token counting using tiktoken
type TokenCounter struct {
	encodingCache map[string]*tiktoken.Tiktoken
}

// NewTokenCounter creates a new token counter instance
func NewTokenCounter() *TokenCounter {
	return &TokenCounter{
		encodingCache: make(map[string]*tiktoken.Tiktoken),
	}
}

// CountTokens counts tokens for the given content using provider/model-specific encoding
// It uses ModelMetadata to determine the correct encoding based on provider and model
func (tc *TokenCounter) CountTokens(content string, metadata *llmtypes.ModelMetadata) (int, error) {
	if metadata == nil {
		// Fallback to o200k_base if no metadata available
		return tc.countTokensWithEncoding(content, "o200k_base")
	}

	// Get encoding based on provider and model
	encoding := tc.getEncodingForModel(metadata.Provider, metadata.ModelID)
	return tc.countTokensWithEncoding(content, encoding)
}

// CountTokensForProvider counts tokens using provider name and model ID
// This is a convenience method when you don't have ModelMetadata
func (tc *TokenCounter) CountTokensForProvider(content, provider, modelID string) (int, error) {
	encoding := tc.getEncodingForModel(provider, modelID)
	return tc.countTokensWithEncoding(content, encoding)
}

// getEncodingForModel determines the correct tiktoken encoding based on provider and model
// Note: tiktoken is primarily designed for OpenAI models. For other providers, we use the closest approximation.
func (tc *TokenCounter) getEncodingForModel(provider, modelID string) string {
	provider = strings.ToLower(provider)
	modelID = strings.ToLower(modelID)

	switch provider {
	case "openai", "openrouter":
		// OpenAI and OpenRouter models use cl100k_base encoding
		// This is the official encoding for GPT-3.5, GPT-4, GPT-4o, o1, o3, and all OpenAI models
		// Reference: tiktoken-python encoding_for_model() function
		return "cl100k_base"

	case "anthropic":
		// IMPORTANT: Anthropic Claude models use their own proprietary tokenizer
		// tiktoken does NOT have a perfect encoding match for Claude models
		// cl100k_base is used as an approximation, but it may not be 100% accurate
		// Claude's tokenizer typically produces MORE tokens than cl100k_base for the same text
		// For accurate counts, consider using Anthropic's official token counting API
		// This applies to all Claude models (Claude 3, 3.5, 4, etc.)
		return "cl100k_base"

	case "vertex", "google":
		// Google/Vertex Gemini models - encoding depends on model version
		// Gemini 2.0 and newer models use o200k_base encoding
		// Gemini 1.5 and older models also use o200k_base (or similar)
		// Reference: Google's tokenizer for Gemini models
		if strings.Contains(modelID, "gemini-2") || strings.Contains(modelID, "gemini-3") || strings.Contains(modelID, "gemini-1.5") {
			return "o200k_base"
		} else if strings.Contains(modelID, "gemini") {
			// Fallback for any other Gemini models
			return "o200k_base"
		}
		// Default for Google models
		return "o200k_base"

	case "bedrock":
		// AWS Bedrock supports multiple model families
		// Determine encoding based on the underlying model
		if strings.Contains(modelID, "claude") {
			// Bedrock Claude models - same limitation as direct Anthropic
			// Uses cl100k_base as approximation
			return "cl100k_base"
		} else if strings.Contains(modelID, "gemini") {
			// Bedrock Gemini models use o200k_base
			return "o200k_base"
		} else if strings.Contains(modelID, "gpt") || strings.Contains(modelID, "j2") || strings.Contains(modelID, "jurassic") {
			// Bedrock GPT/Jurassic models use cl100k_base
			return "cl100k_base"
		} else if strings.Contains(modelID, "llama") || strings.Contains(modelID, "mistral") {
			// Bedrock Llama/Mistral models - these may vary, but cl100k_base is a reasonable approximation
			return "cl100k_base"
		}
		// Default for unknown Bedrock models
		return "o200k_base"

	default:
		// Default fallback encoding
		// o200k_base is a newer, more general encoding that works reasonably well for many models
		return "o200k_base"
	}
}

// countTokensWithEncoding counts tokens using the specified encoding
// Uses caching to avoid re-initializing encodings
func (tc *TokenCounter) countTokensWithEncoding(content string, encodingName string) (int, error) {
	// Check cache first
	if enc, exists := tc.encodingCache[encodingName]; exists {
		tokens := enc.Encode(content, nil, nil)
		return len(tokens), nil
	}

	// Get encoding from tiktoken
	encoding, err := tiktoken.GetEncoding(encodingName)
	if err != nil {
		// Fallback to character-based approximation if encoding fails
		// Rough estimation: 1 token ≈ 4 characters for English text
		return len(content) / 4, err
	}

	// Cache the encoding for future use
	tc.encodingCache[encodingName] = encoding

	// Count tokens
	tokens := encoding.Encode(content, nil, nil)
	return len(tokens), nil
}

// CountTokensForModel is a convenience function that counts tokens using a Model interface
// It automatically fetches model metadata and uses the appropriate encoding
func CountTokensForModel(content string, model llmtypes.Model) (int, error) {
	tc := NewTokenCounter()

	// Get model metadata
	modelID := model.GetModelID()
	if modelID == "" {
		// Fallback if model ID is not available
		return tc.CountTokensForProvider(content, "", "")
	}

	metadata, err := model.GetModelMetadata(modelID)
	if err != nil {
		// Fallback to default encoding if metadata is unavailable
		return tc.CountTokensForProvider(content, "", modelID)
	}

	return tc.CountTokens(content, metadata)
}
