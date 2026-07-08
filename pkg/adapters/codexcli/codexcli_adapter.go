package codexcli

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var codexRateLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b429\b`),
	regexp.MustCompile(`\b503\b`),
	regexp.MustCompile(`(?i)\brate[- ]limit(ed|ing)?\b`),
	regexp.MustCompile(`(?i)\btoo many requests\b`),
	regexp.MustCompile(`(?i)\bservice unavailable\b`),
	regexp.MustCompile(`(?i)\busage limit\b`),
	regexp.MustCompile(`(?i)\boverloaded\b`),
}

func looksLikeCodexRateLimit(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	for _, pattern := range codexRateLimitPatterns {
		if pattern.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// CodexCLIAdapter implements the LLM interface for the OpenAI Codex CLI.
type CodexCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewCodexCLIAdapter creates a new instance of the CodexCLIAdapter.
func NewCodexCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *CodexCLIAdapter {
	return &CodexCLIAdapter{
		apiKey:  apiKey,
		modelID: modelID,
		logger:  logger,
	}
}

// GenerateContent generates content using Codex CLI through the tmux transport.
// Direct callers that do not pass an interactive owner session get a bounded
// temporary owner ID so the old non-interactive entry point still works without
// falling back to `codex exec --json`.
func (c *CodexCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	// Defensive backstop for direct adapter callers that bypass the
	// orchestrator's image-content pre-processing. The app funnels CLI image
	// input as a TEXT message containing the absolute workspace path; the model
	// then uses its filesystem-read capability to inspect it.
	if len(collectCodexImageContent(messages)) > 0 {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("codex-cli tmux transport does not support llmtypes.ImageContent directly; pass the image file path as text instead")
	}

	if codexInteractiveSessionIDFromOptions(opts) == "" {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = fmt.Sprintf("codex-bounded-%d-%s", time.Now().UnixNano(), codexRandomHex(4))
	}

	return c.generateContentInteractive(ctx, messages, opts)
}

// SearchWeb uses Codex CLI's native web search capability and returns the final text response.
func (c *CodexCLIAdapter) SearchWeb(ctx context.Context, query string, options ...llmtypes.CallOption) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	searchPrompt := "Use web search to answer the following query.\n\n" + query
	resp, err := c.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: searchPrompt},
			},
		},
	}, options...)
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("codex cli web search returned no response")
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		return "", fmt.Errorf("codex cli web search returned empty response")
	}
	return content, nil
}

// GetModelID returns the model ID.
func (c *CodexCLIAdapter) GetModelID() string {
	return c.modelID
}

// GetModelMetadata returns metadata for the model.
func (c *CodexCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = c.modelID
	}
	originalModelID := modelID
	modelID = resolveCodexCLIModelID(modelID)
	metadataModelID := strings.TrimSpace(originalModelID)
	if metadataModelID == "" {
		metadataModelID = modelID
	}

	// Known model metadata
	switch {
	case strings.Contains(modelID, "gpt-5.5"):
		return &llmtypes.ModelMetadata{
			ModelID:                    metadataModelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.5",
			ContextWindow:              1050000,
			InputCostPer1MTokens:       5.00,
			OutputCostPer1MTokens:      30.00,
			CachedInputCostPer1MTokens: 0.50,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"none", "low", "medium", "high", "xhigh"},
		}, nil

	case strings.Contains(modelID, "gpt-5.4-mini"):
		return &llmtypes.ModelMetadata{
			ModelID:                    metadataModelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.4 Mini",
			ContextWindow:              400000,
			InputCostPer1MTokens:       0.75,
			OutputCostPer1MTokens:      4.50,
			CachedInputCostPer1MTokens: 0.075,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"low", "medium", "high", "xhigh"},
		}, nil

	case strings.Contains(modelID, "gpt-5.4"):
		return &llmtypes.ModelMetadata{
			ModelID:                    metadataModelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.4",
			ContextWindow:              1100000,
			InputCostPer1MTokens:       2.50,
			OutputCostPer1MTokens:      15.00,
			CachedInputCostPer1MTokens: 0.25,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"none", "low", "medium", "high", "xhigh"},
		}, nil

	case strings.Contains(modelID, "gpt-5.3-codex-spark"):
		return &llmtypes.ModelMetadata{
			ModelID:                    metadataModelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.3-Codex-Spark",
			ContextWindow:              400000,
			InputCostPer1MTokens:       1.75,
			OutputCostPer1MTokens:      14.00,
			CachedInputCostPer1MTokens: 0.175,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"low", "medium", "high", "xhigh"},
		}, nil

	case strings.Contains(modelID, "gpt-5.3-codex"):
		return &llmtypes.ModelMetadata{
			ModelID:                    metadataModelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.3-Codex",
			ContextWindow:              400000,
			InputCostPer1MTokens:       1.75,
			OutputCostPer1MTokens:      14.00,
			CachedInputCostPer1MTokens: 0.175,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"low", "medium", "high", "xhigh"},
		}, nil

	default:
		// Generic fallback for unknown models
		return &llmtypes.ModelMetadata{
			ModelID:                 metadataModelID,
			Provider:                "codex-cli",
			ModelName:               "OpenAI Codex CLI (pricing varies)",
			ContextWindow:           200000,
			SupportsToolCalls:       true,
			SupportsReasoningEffort: true,
			ReasoningEffortLevels:   []string{"low", "medium", "high", "xhigh"},
		}, nil
	}
}

// --- Helper Functions ---

func extractTextFromMessage(msg llmtypes.MessageContent) string {
	var parts []string
	for _, part := range msg.Parts {
		if textPart, ok := part.(llmtypes.TextContent); ok {
			parts = append(parts, textPart.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func collectCodexImageContent(messages []llmtypes.MessageContent) []llmtypes.ImageContent {
	var images []llmtypes.ImageContent
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llmtypes.ImageContent:
				images = append(images, p)
			case *llmtypes.ImageContent:
				if p != nil {
					images = append(images, *p)
				}
			}
		}
	}
	return images
}

func codexStringConfigOverride(key string, value string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal codex config override %s: %w", key, err)
	}
	return key + "=" + string(encoded), nil
}
