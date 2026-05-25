package llmtypes

import "strings"

// WithModel sets the model ID
func WithModel(model string) CallOption {
	return func(opts *CallOptions) {
		opts.Model = model
	}
}

// WithTemperature sets the temperature
func WithTemperature(temperature float64) CallOption {
	return func(opts *CallOptions) {
		opts.Temperature = temperature
	}
}

// WithMaxTokens sets the maximum tokens
func WithMaxTokens(maxTokens int) CallOption {
	return func(opts *CallOptions) {
		opts.MaxTokens = maxTokens
	}
}

// WithTopP sets the nucleus-sampling probability cutoff. Pass a value in
// (0, 1]; 1.0 effectively disables nucleus sampling. Zero is treated as
// "do not set" and the provider's own default applies.
func WithTopP(topP float64) CallOption {
	return func(opts *CallOptions) {
		opts.TopP = topP
	}
}

// WithTopK sets the top-k sampling cutoff. Only forwarded to providers
// that accept top_k (Anthropic Messages API does; OpenAI Chat Completions
// does not). Pass zero to leave the provider's default in place.
func WithTopK(topK int) CallOption {
	return func(opts *CallOptions) {
		opts.TopK = topK
	}
}

// WithInspectorSink attaches a debug-event sink for this call. Adapters
// that participate in the inspector contract will emit
// InspectorEvents at request/event/tool_call/completion/error
// boundaries. Pass nil (the default) to disable inspector emission
// entirely.
func WithInspectorSink(sink InspectorSink) CallOption {
	return func(opts *CallOptions) {
		opts.InspectorSink = sink
	}
}

// WithStopSequences sets the strings that, if generated, terminate
// sampling immediately. Pass an empty slice (or nil) to clear.
func WithStopSequences(seqs []string) CallOption {
	return func(opts *CallOptions) {
		if seqs == nil {
			opts.StopSequences = nil
			return
		}
		out := make([]string, 0, len(seqs))
		for _, s := range seqs {
			if s != "" {
				out = append(out, s)
			}
		}
		opts.StopSequences = out
	}
}

// WithJSONMode enables JSON mode
func WithJSONMode() CallOption {
	return func(opts *CallOptions) {
		opts.JSONMode = true
	}
}

// WithJSONSchema enables JSON Schema structured outputs
// schema: The JSON Schema definition as a map
// name: The name of the schema
// description: Description of what the schema represents
// strict: Whether to enforce strict schema compliance (default: true)
func WithJSONSchema(schema map[string]interface{}, name, description string, strict bool) CallOption {
	return func(opts *CallOptions) {
		opts.JSONSchema = &JSONSchemaConfig{
			Name:        name,
			Description: description,
			Schema:      schema,
			Strict:      strict,
		}
	}
}

// WithTools sets the tools available for the LLM
func WithTools(tools []Tool) CallOption {
	return func(opts *CallOptions) {
		opts.Tools = tools
	}
}

// WithToolChoice sets the tool choice strategy
func WithToolChoice(toolChoice *ToolChoice) CallOption {
	return func(opts *CallOptions) {
		opts.ToolChoice = toolChoice
	}
}

// WithToolChoiceString creates a ToolChoice from a string type ("auto", "none", "required") and sets it
func WithToolChoiceString(choiceType string) CallOption {
	return func(opts *CallOptions) {
		opts.ToolChoice = &ToolChoice{Type: choiceType}
	}
}

// WithStreamingChan sets the streaming channel for receiving chunks
// The channel receives structured StreamChunk objects that can be either content or tool calls
// The channel will be closed when streaming completes
func WithStreamingChan(ch chan<- StreamChunk) CallOption {
	return func(opts *CallOptions) {
		opts.StreamChan = ch
	}
}

// WithCodingProviderLaunchOnly asks tmux-backed coding-agent adapters to start
// or reacquire their interactive TUI and return once the prompt is ready,
// without sending a user message.
func WithCodingProviderLaunchOnly() CallOption {
	return func(opts *CallOptions) {
		if opts.Metadata == nil {
			opts.Metadata = &Metadata{Custom: make(map[string]interface{})}
		}
		if opts.Metadata.Custom == nil {
			opts.Metadata.Custom = make(map[string]interface{})
		}
		opts.Metadata.Custom[CodingProviderLaunchOnlyMetadataKey] = true
	}
}

func CodingProviderLaunchOnlyFromOptions(opts *CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[CodingProviderLaunchOnlyMetadataKey].(bool)
	return enabled
}

// WithCodingProviderLaunchSystemPrompt carries the agent's accumulated
// system prompt through the launch-only contract so the adapter can
// project its provider-specific rule file (.cursor/rules/mlp-system.mdc,
// .agents/rules/mlp-system.md, AGENTS.md, GEMINI.md, CLAUDE.md, etc.)
// even though no user message is being sent. Without this, launch-only
// (used by the resumed-terminal restore path) hits the adapter with
// nil messages → split*SystemPrompt returns empty → the rule file is
// never written for that session.
func WithCodingProviderLaunchSystemPrompt(systemPrompt string) CallOption {
	return func(opts *CallOptions) {
		if strings.TrimSpace(systemPrompt) == "" {
			return
		}
		if opts.Metadata == nil {
			opts.Metadata = &Metadata{Custom: make(map[string]interface{})}
		}
		if opts.Metadata.Custom == nil {
			opts.Metadata.Custom = make(map[string]interface{})
		}
		opts.Metadata.Custom[CodingProviderLaunchSystemPromptMetadataKey] = systemPrompt
	}
}

// CodingProviderLaunchSystemPromptFromOptions returns the launch-only
// system prompt (if any) injected via WithCodingProviderLaunchSystemPrompt.
// Empty string when not set.
func CodingProviderLaunchSystemPromptFromOptions(opts *CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	prompt, _ := opts.Metadata.Custom[CodingProviderLaunchSystemPromptMetadataKey].(string)
	return prompt
}

// WithStreamingFunc is a convenience function that creates a channel and callback
// This maintains backward compatibility for simple use cases
// For better control, use WithStreamingChan directly
func WithStreamingFunc(fn func(StreamChunk)) CallOption {
	ch := make(chan StreamChunk, 100) // Buffered channel to avoid blocking
	go func() {
		for chunk := range ch {
			fn(chunk)
		}
	}()
	return WithStreamingChan(ch)
}

// TextPart creates a single text part message content
func TextPart(role ChatMessageType, text string) MessageContent {
	return MessageContent{
		Role:  role,
		Parts: []ContentPart{TextContent{Text: text}},
	}
}

// TextParts creates a message content with multiple text parts
func TextParts(role ChatMessageType, texts ...string) MessageContent {
	parts := make([]ContentPart, len(texts))
	for i, text := range texts {
		parts[i] = TextContent{Text: text}
	}
	return MessageContent{
		Role:  role,
		Parts: parts,
	}
}

// ImagePart creates a message content with a single image part
// sourceType should be "base64" or "url"
// For base64: mediaType is required (e.g., "image/jpeg"), data is base64-encoded string
// For url: mediaType is ignored, data is the image URL
func ImagePart(role ChatMessageType, sourceType, mediaType, data string) MessageContent {
	return MessageContent{
		Role: role,
		Parts: []ContentPart{
			ImageContent{
				SourceType: sourceType,
				MediaType:  mediaType,
				Data:       data,
			},
		},
	}
}

// ImagePartBase64 creates a message content with a base64-encoded image
func ImagePartBase64(role ChatMessageType, mediaType, base64Data string) MessageContent {
	return ImagePart(role, "base64", mediaType, base64Data)
}

// ImagePartURL creates a message content with an image URL
func ImagePartURL(role ChatMessageType, imageURL string) MessageContent {
	return ImagePart(role, "url", "", imageURL)
}

// WithEmbeddingModel sets the embedding model ID
func WithEmbeddingModel(model string) EmbeddingOption {
	return func(opts *EmbeddingOptions) {
		opts.Model = model
	}
}

// WithDimensions sets the dimensions parameter for embedding generation
// This is only supported for text-embedding-3 models
func WithDimensions(dimensions int) EmbeddingOption {
	return func(opts *EmbeddingOptions) {
		opts.Dimensions = &dimensions
	}
}

// WithReasoningEffort sets the reasoning effort level for models that support it (e.g., gpt-5.1)
// Valid values: "minimal", "low", "medium", "high"
// When set to "minimal", the model uses minimal reasoning effort
// Higher values enable deeper reasoning for complex problems
func WithReasoningEffort(effort string) CallOption {
	return func(opts *CallOptions) {
		opts.ReasoningEffort = effort
	}
}

// WithVerbosity sets the verbosity level for the model's response (for reasoning models)
// Valid values: "low", "medium", "high"
// Lower values result in more concise responses, higher values result in more verbose responses
func WithVerbosity(verbosity string) CallOption {
	return func(opts *CallOptions) {
		opts.Verbosity = verbosity
	}
}

// WithThinkingLevel sets the thinking level for models that support it (e.g., Gemini 3 Pro)
// Valid values: "low", "high"
// "low" reduces latency for simpler tasks, "high" enables deeper reasoning for complex tasks.
// Default is "high" for Gemini 3 Pro.
func WithThinkingLevel(level string) CallOption {
	return func(opts *CallOptions) {
		opts.ThinkingLevel = level
	}
}

// WithThinkingBudget sets the thinking budget (token limit) for models that support it
// (e.g., Gemini 2.5 Flash Thinking)
func WithThinkingBudget(budget int) CallOption {
	return func(opts *CallOptions) {
		opts.ThinkingBudget = budget
	}
}

// WithAllowedTools sets the list of explicitly allowed tools for the model (e.g., gpt-5.2-codex)
func WithAllowedTools(tools []string) CallOption {
	return func(opts *CallOptions) {
		opts.AllowedTools = tools
	}
}
