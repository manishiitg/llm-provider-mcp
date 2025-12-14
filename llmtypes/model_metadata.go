package llmtypes

// ModelMetadata contains comprehensive metadata about an LLM model including
// token limits and pricing for all token types (input, output, reasoning, cache)
type ModelMetadata struct {
	// ModelID is the unique identifier for the model (e.g., "gpt-4o", "claude-3-5-sonnet-20241022")
	ModelID string

	// ModelName is the human-readable name of the model
	ModelName string

	// ContextWindow is the maximum number of tokens the model can process in a single request
	ContextWindow int

	// InputCostPer1MTokens is the cost per 1 million input tokens (in USD)
	InputCostPer1MTokens float64

	// OutputCostPer1MTokens is the cost per 1 million output tokens (in USD)
	OutputCostPer1MTokens float64

	// ReasoningCostPer1MTokens is the cost per 1 million reasoning tokens (in USD)
	// This applies to reasoning models like o1, o3, gpt-5.1
	// Set to 0 if the model doesn't support reasoning tokens
	ReasoningCostPer1MTokens float64

	// CachedInputCostPer1MTokens is the cost per 1 million cached input tokens (in USD)
	// Cached tokens typically have a 50-90% discount compared to regular input tokens
	// Set to 0 if the model doesn't support prompt caching
	CachedInputCostPer1MTokens float64

	// Provider is the name of the provider (e.g., "openai", "anthropic", "bedrock")
	Provider string
}

// ModelMetadataProvider is an optional interface that models can implement
// to provide metadata about themselves
type ModelMetadataProvider interface {
	// GetModelMetadata returns metadata for the specified model ID
	// If modelID is empty, returns metadata for the default model
	GetModelMetadata(modelID string) (*ModelMetadata, error)
}
