package agycli

import (
	"context"
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// AgyCLIAdapter implements the LLM interface for Antigravity CLI.
// Antigravity's print mode is not stable enough for the coding-agent contract,
// so this adapter intentionally supports the tmux interactive transport only.
type AgyCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewAgyCLIAdapter creates a new AgyCLIAdapter.
func NewAgyCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *AgyCLIAdapter {
	return &AgyCLIAdapter{
		apiKey:  apiKey,
		modelID: modelID,
		logger:  logger,
	}
}

// GenerateContent generates content using Antigravity CLI tmux mode.
func (c *AgyCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	if containsAgyImageContent(messages) {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("agy-cli does not support llmtypes.ImageContent directly; pass the image file path as text instead")
	}

	return c.generateContentTmux(ctx, messages, opts)
}

// SearchWeb asks Antigravity CLI to use its native search capability and
// returns the final text response.
func (c *AgyCLIAdapter) SearchWeb(ctx context.Context, query string, options ...llmtypes.CallOption) (string, error) {
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
		return "", fmt.Errorf("agy cli web search returned no response")
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		return "", fmt.Errorf("agy cli web search returned empty response")
	}
	return content, nil
}

// GetModelID returns the configured model ID.
func (c *AgyCLIAdapter) GetModelID() string {
	return c.modelID
}

// GetModelMetadata returns metadata for Antigravity CLI model selectors.
// Account model availability and pricing can vary, so selectors are exposed
// with conservative generic metadata.
func (c *AgyCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = c.modelID
	}
	metadataModelID := strings.TrimSpace(modelID)
	if metadataModelID == "" {
		metadataModelID = "agy-cli"
	}

	return &llmtypes.ModelMetadata{
		ModelID:           metadataModelID,
		Provider:          "agy-cli",
		ModelName:         "Antigravity CLI (default, pricing varies)",
		ContextWindow:     200000,
		SupportsToolCalls: true,
	}, nil
}

func splitAgySystemPrompt(messages []llmtypes.MessageContent) (string, []llmtypes.MessageContent) {
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

func buildAgyPrompt(messages []llmtypes.MessageContent) string {
	return latestAgyHumanMessage(messages)
}

func latestAgyHumanMessage(messages []llmtypes.MessageContent) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llmtypes.ChatMessageTypeHuman {
			return extractTextFromMessage(messages[i])
		}
	}
	return ""
}

func agyAssistantHistory(messages []llmtypes.MessageContent) []string {
	history := make([]string, 0)
	for _, msg := range messages {
		if msg.Role != llmtypes.ChatMessageTypeAI {
			continue
		}
		text := strings.TrimSpace(extractTextFromMessage(msg))
		if text != "" {
			history = append(history, text)
		}
	}
	return history
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

func containsAgyImageContent(messages []llmtypes.MessageContent) bool {
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

func (c *AgyCLIAdapter) logInfof(format string, args ...interface{}) {
	if c.logger != nil {
		c.logger.Infof(format, args...)
	}
}
