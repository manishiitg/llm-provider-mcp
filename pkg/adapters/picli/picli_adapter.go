package picli

import (
	"context"
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	DefaultModelID = "google/gemini-3.6-flash"
)

// PiCLIAdapter implements the LLM interface for Pi Coding Agent's tmux TUI.
type PiCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewPiCLIAdapter creates a new Pi CLI adapter.
func NewPiCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *PiCLIAdapter {
	return &PiCLIAdapter{
		apiKey:  strings.TrimSpace(apiKey),
		modelID: strings.TrimSpace(modelID),
		logger:  logger,
	}
}

// GenerateContent generates content using Pi CLI tmux mode.
func (p *PiCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	if containsPiImageContent(messages) {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("pi-cli does not support llmtypes.ImageContent directly; pass the image file path as text instead")
	}

	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if structured, ok := opts.Metadata.Custom[MetadataKeyStructuredTransport].(bool); ok && structured {
			return p.generateContentStructured(ctx, messages, opts)
		}
	}

	return p.generateContentTmux(ctx, messages, opts)
}

// SearchWeb asks Pi to use available web/search capability if the selected
// model/tools support it, and returns the final text response.
func (p *PiCLIAdapter) SearchWeb(ctx context.Context, query string, options ...llmtypes.CallOption) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	resp, err := p.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Use available web search capability to answer this query.\n\n"+query),
	}, options...)
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("pi-cli web search returned no response")
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		return "", fmt.Errorf("pi-cli web search returned empty response")
	}
	return content, nil
}

// GetModelID returns the configured model ID.
func (p *PiCLIAdapter) GetModelID() string {
	if strings.TrimSpace(p.modelID) == "" {
		return DefaultModelID
	}
	return p.modelID
}

// GetModelMetadata returns conservative metadata for Pi-routed models.
func (p *PiCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if strings.TrimSpace(modelID) == "" {
		modelID = p.GetModelID()
	}
	provider, model := resolvePiProviderModel(modelID, "")
	return &llmtypes.ModelMetadata{
		ModelID:                 provider + "/" + model,
		Provider:                "pi-cli",
		ModelName:               "Pi CLI (" + provider + "/" + model + ")",
		ContextWindow:           1048576,
		SupportsToolCalls:       true,
		SupportsReasoningEffort: true,
		ReasoningEffortLevels:   []string{"low", "medium", "high", "xhigh"},
	}, nil
}

func splitPiSystemPrompt(messages []llmtypes.MessageContent) (string, []llmtypes.MessageContent) {
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

func buildPiPrompt(messages []llmtypes.MessageContent) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llmtypes.ChatMessageTypeHuman {
			return extractPiTextFromMessage(messages[i])
		}
	}
	return ""
}

func extractPiTextFromMessage(msg llmtypes.MessageContent) string {
	var parts []string
	for _, part := range msg.Parts {
		if textPart, ok := part.(llmtypes.TextContent); ok {
			parts = append(parts, textPart.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func containsPiImageContent(messages []llmtypes.MessageContent) bool {
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

func (p *PiCLIAdapter) logInfof(format string, args ...interface{}) {
	if p.logger != nil {
		p.logger.Infof(format, args...)
	}
}
