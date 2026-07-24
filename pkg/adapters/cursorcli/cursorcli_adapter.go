package cursorcli

import (
	"context"
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// CursorCLIAdapter implements the LLM interface for Cursor Agent CLI.
// Transport is tmux-only — cursor-cli requires a persistent tmux session
// for every turn. The structured stream-json path was removed; see git
// history for the prior implementation if it ever needs reinstating.
type CursorCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewCursorCLIAdapter creates a new CursorCLIAdapter.
func NewCursorCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *CursorCLIAdapter {
	return &CursorCLIAdapter{
		apiKey:  apiKey,
		modelID: modelID,
		logger:  logger,
	}
}

// GenerateContent generates content using Cursor Agent CLI via the
// tmux transport. Cursor is tmux-only: if the caller did not pass an
// interactive session ID, generateContentTmux auto-generates a bounded
// one. The structured stream-json path is intentionally bypassed here
// to keep a single contract across chat and workflow steps.
func (c *CursorCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	// Defensive backstop for direct adapter callers that bypass the
	// orchestrator's image-content pre-processing. In practice, callers
	// running through mcp-agent-builder-go funnel CLI image input as a
	// TEXT message containing the absolute workspace path (see
	// workspace_advanced_tools.go:1509-1517 `pathBasedImageAnalysisProvider`
	// branch); the model uses its filesystem-read capability to view the
	// file. So image input across all CLI providers IS uniform — text +
	// path — and this rejection rarely fires.
	if containsCursorImageContent(messages) {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("cursor-cli does not support llmtypes.ImageContent directly; pass the image file path as text instead")
	}

	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if structured, ok := opts.Metadata.Custom[MetadataKeyStructuredTransport].(bool); ok && structured {
			return c.generateContentStructured(ctx, messages, opts, nil)
		}
	}
	return c.generateContentTmux(ctx, messages, opts)
}

// SearchWeb asks Cursor Agent CLI to use its web search capability and returns
// the final text response.
func (c *CursorCLIAdapter) SearchWeb(ctx context.Context, query string, options ...llmtypes.CallOption) (string, error) {
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
		return "", fmt.Errorf("cursor cli web search returned no response")
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		return "", fmt.Errorf("cursor cli web search returned empty response")
	}
	return content, nil
}

// GetModelID returns the configured model ID.
func (c *CursorCLIAdapter) GetModelID() string {
	return c.modelID
}

// GetModelMetadata returns metadata for Cursor Agent CLI model selectors. Cursor
// account model availability and pricing can vary, so unknown selectors are
// exposed with conservative generic metadata.
func (c *CursorCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = c.modelID
	}
	metadataModelID := strings.TrimSpace(modelID)
	if metadataModelID == "" {
		metadataModelID = "cursor-cli"
	}

	switch resolveCursorCLIModelID(modelID) {
	case "":
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "cursor-cli",
			ModelName:         "Auto (Cursor Agent CLI)",
			ContextWindow:     200000,
			SupportsToolCalls: true,
		}, nil
	case "composer-2.5":
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "cursor-cli",
			ModelName:         "Composer 2.5 (Cursor Agent CLI)",
			ContextWindow:     200000,
			SupportsToolCalls: true,
		}, nil
	case "grok-4.5-xhigh":
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "cursor-cli",
			ModelName:         "Grok 4.5 (Cursor Agent CLI)",
			ContextWindow:     500000,
			SupportsToolCalls: true,
		}, nil
	case "gpt-5":
		return &llmtypes.ModelMetadata{
			ModelID:                 metadataModelID,
			Provider:                "cursor-cli",
			ModelName:               "GPT-5 (Cursor Agent CLI)",
			ContextWindow:           400000,
			SupportsToolCalls:       true,
			SupportsReasoningEffort: true,
		}, nil
	case "sonnet-4":
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "cursor-cli",
			ModelName:         "Claude Sonnet 4 (Cursor Agent CLI)",
			ContextWindow:     200000,
			SupportsToolCalls: true,
		}, nil
	case "sonnet-4-thinking":
		return &llmtypes.ModelMetadata{
			ModelID:                 metadataModelID,
			Provider:                "cursor-cli",
			ModelName:               "Claude Sonnet 4 Thinking (Cursor Agent CLI)",
			ContextWindow:           200000,
			SupportsToolCalls:       true,
			SupportsReasoningEffort: true,
		}, nil
	default:
		return &llmtypes.ModelMetadata{
			ModelID:           metadataModelID,
			Provider:          "cursor-cli",
			ModelName:         "Cursor Agent CLI (default, pricing varies)",
			ContextWindow:     200000,
			SupportsToolCalls: true,
		}, nil
	}
}

func splitCursorSystemPrompt(messages []llmtypes.MessageContent) (string, []llmtypes.MessageContent) {
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

func buildCursorPrompt(messages []llmtypes.MessageContent, resume bool) string {
	if resume {
		return latestCursorHumanMessage(messages)
	}
	if len(messages) <= 1 {
		return latestCursorHumanMessage(messages)
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

func latestCursorHumanMessage(messages []llmtypes.MessageContent) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llmtypes.ChatMessageTypeHuman {
			return extractTextFromMessage(messages[i])
		}
	}
	return ""
}

func cursorAssistantHistory(messages []llmtypes.MessageContent) []string {
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

func containsCursorImageContent(messages []llmtypes.MessageContent) bool {
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

func (c *CursorCLIAdapter) logInfof(format string, args ...interface{}) {
	if c.logger != nil {
		c.logger.Infof(format, args...)
	}
}
