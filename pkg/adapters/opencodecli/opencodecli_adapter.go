package opencodecli

import (
	"context"
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// OpenCodeCLIAdapter implements the LLM interface for OpenCode CLI.
// By default it uses the structured transport (opencode run --format json).
//
// When constructed for a specific sub-provider tile (Kimi / DeepSeek /
// Qwen / MiniMax / GLM / Free) via NewOpenCodeCLIAdapterForSubProvider,
// the adapter remembers the tile's scope and injects the matching
// per-sub-provider env var on every call. Callers do not have to attach
// WithOpenCodeSubProvider/WithOpenCodeSubProviderAPIKey to each call.
//
// Call-time options still override the adapter-level defaults so
// dispatchers can re-scope a single call without rebuilding the adapter.
type OpenCodeCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger

	defaultSubProviderID     string
	defaultSubProviderEnvVar string
	defaultSubProviderAPIKey string
}

// NewOpenCodeCLIAdapter creates a new OpenCodeCLIAdapter scoped to the
// legacy "opencode-cli" tile (single shared OPENCODE_API_KEY).
func NewOpenCodeCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *OpenCodeCLIAdapter {
	return &OpenCodeCLIAdapter{
		apiKey:  apiKey,
		modelID: modelID,
		logger:  logger,
	}
}

// NewOpenCodeCLIAdapterForSubProvider creates an OpenCodeCLIAdapter
// preconfigured for a specific sub-provider tile. Every call inherits the
// tile's scope (the OpenCode-internal provider id, the API-key env var,
// and the API-key value), so callers do not need to attach the
// WithOpenCodeSubProvider/WithOpenCodeSubProviderAPIKey options to each
// GenerateContent invocation.
//
// `subProvider` must be one of OpenCodeSubProviders(); `apiKey` is the
// caller-supplied credential (typically pulled from the workspace-
// encrypted store). When the sub-provider does not require a key
// (Free tile), `apiKey` may be empty.
//
// `sharedAPIKey` is the legacy OPENCODE_API_KEY used for OpenCode-hosted
// services (free tier auth, models cache, etc.). It is independent of the
// per-sub-provider key.
func NewOpenCodeCLIAdapterForSubProvider(sharedAPIKey string, modelID string, subProvider OpenCodeSubProvider, apiKey string, logger interfaces.Logger) *OpenCodeCLIAdapter {
	return &OpenCodeCLIAdapter{
		apiKey:                   sharedAPIKey,
		modelID:                  modelID,
		logger:                   logger,
		defaultSubProviderID:     subProvider.ID,
		defaultSubProviderEnvVar: subProvider.APIKeyEnvVar,
		defaultSubProviderAPIKey: apiKey,
	}
}

// GenerateContent generates content using OpenCode CLI's structured JSON
// transport.
func (c *OpenCodeCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	if containsOpenCodeImageContent(messages) {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("opencode-cli does not support llmtypes.ImageContent directly; pass the image file path as text instead")
	}

	return c.generateContentStructured(ctx, messages, opts)
}

// SearchWeb asks OpenCode CLI to use its web search capability and returns
// the final text response.
func (c *OpenCodeCLIAdapter) SearchWeb(ctx context.Context, query string, options ...llmtypes.CallOption) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	searchPrompt := "Use web search to answer the following query.\n\n" + query
	searchOptions := append([]llmtypes.CallOption{}, options...)
	searchOptions = append(searchOptions, WithAutoApproveWebSearch())
	resp, err := c.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: searchPrompt},
			},
		},
	}, searchOptions...)
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("opencode cli web search returned no response")
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		return "", fmt.Errorf("opencode cli web search returned empty response")
	}
	return content, nil
}

// GetModelID returns the configured model ID.
func (c *OpenCodeCLIAdapter) GetModelID() string {
	return c.modelID
}

// GetModelMetadata returns metadata for OpenCode CLI model selectors. OpenCode
// account model availability and pricing can vary, so unknown selectors are
// exposed with conservative generic metadata.
func (c *OpenCodeCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = c.modelID
	}
	metadataModelID := strings.TrimSpace(modelID)
	if metadataModelID == "" {
		metadataModelID = "opencode-cli"
	}

	switch resolveOpenCodeCLIModelID(modelID) {
	case "":
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "opencode-cli",
			ModelName:         "OpenCode CLI",
			ContextWindow:     200000,
			SupportsToolCalls: true,
		}, nil
	case "openai/gpt-5.1", "openai/gpt-5", "gpt-5":
		return &llmtypes.ModelMetadata{
			ModelID:                 metadataModelID,
			Provider:                "opencode-cli",
			ModelName:               "GPT-5 via OpenCode",
			ContextWindow:           400000,
			SupportsToolCalls:       true,
			SupportsReasoningEffort: true,
		}, nil
	case "anthropic/claude-sonnet-4-5", "sonnet-4":
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "opencode-cli",
			ModelName:         "Claude Sonnet via OpenCode",
			ContextWindow:     200000,
			SupportsToolCalls: true,
		}, nil
	case "opencode/deepseek-v4-flash", "deepseek-v4-flash":
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "opencode-cli",
			ModelName:         "DeepSeek V4 Flash via OpenCode",
			ContextWindow:     128000,
			SupportsToolCalls: true,
		}, nil
	default:
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "opencode-cli",
			ModelName:         "OpenCode: " + metadataModelID,
			ContextWindow:     200000,
			SupportsToolCalls: true,
		}, nil
	}
}

func splitOpenCodeSystemPrompt(messages []llmtypes.MessageContent) (string, []llmtypes.MessageContent) {
	var systems []string
	conversation := make([]llmtypes.MessageContent, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			for _, part := range msg.Parts {
				if textPart, ok := part.(llmtypes.TextContent); ok {
					systems = append(systems, textPart.Text)
				}
			}
			continue
		}
		conversation = append(conversation, msg)
	}
	return strings.Join(systems, "\n\n"), conversation
}

func buildOpenCodePrompt(messages []llmtypes.MessageContent, resume bool) string {
	if resume {
		return latestOpenCodeHumanMessage(messages)
	}
	if len(messages) <= 1 {
		return latestOpenCodeHumanMessage(messages)
	}

	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		text := extractTextFromMessage(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		role := "User"
		if msg.Role == llmtypes.ChatMessageTypeAI {
			role = "Assistant"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", role, text))
	}
	return strings.Join(parts, "\n\n")
}

func latestOpenCodeHumanMessage(messages []llmtypes.MessageContent) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llmtypes.ChatMessageTypeHuman {
			return extractTextFromMessage(messages[i])
		}
	}
	return ""
}

func extractTextFromMessage(msg llmtypes.MessageContent) string {
	var parts []string
	for _, part := range msg.Parts {
		if textPart, ok := part.(llmtypes.TextContent); ok {
			parts = append(parts, textPart.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func containsOpenCodeImageContent(messages []llmtypes.MessageContent) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch part.(type) {
			case llmtypes.ImageContent, *llmtypes.ImageContent:
				return true
			}
		}
	}
	return false
}

func (c *OpenCodeCLIAdapter) logInfof(format string, args ...interface{}) {
	if c.logger != nil {
		c.logger.Infof(format, args...)
	}
}

func (c *OpenCodeCLIAdapter) logDebugf(format string, args ...interface{}) {
	if c.logger != nil {
		c.logger.Debugf(format, args...)
	}
}
