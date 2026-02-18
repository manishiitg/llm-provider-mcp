package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Constants for custom metadata keys
const (
	MetadataKeyMCPConfig                 = "mcp_config"
	MetadataKeyDangerouslySkipPermissions = "dangerously_skip_permissions"
	MetadataKeyTools                     = "claude_code_tools"
)

// ClaudeCodeAdapter implements the LLM interface for the Claude Code CLI.
type ClaudeCodeAdapter struct {
	modelID string
	logger  interfaces.Logger
}

// NewClaudeCodeAdapter creates a new instance of the ClaudeCodeAdapter.
func NewClaudeCodeAdapter(apiKey string, modelID string, logger interfaces.Logger) *ClaudeCodeAdapter {
	// apiKey is not used for the CLI adapter as auth is handled by the CLI itself
	return &ClaudeCodeAdapter{
		modelID: modelID,
		logger:  logger,
	}
}

// WithMCPConfig sets the MCP configuration JSON string for the session.
func WithMCPConfig(config string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = config
	}
}

// WithDangerouslySkipPermissions enables the --dangerously-skip-permissions flag.
// CAUTION: This allows the agent to execute any tool without user confirmation.
func WithDangerouslySkipPermissions() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions] = true
	}
}

// WithClaudeCodeTools sets the --tools flag to whitelist specific tools.
// Note: Core tools (Bash, Read, Write, etc.) may persist even if not listed.
// Use "" to disable optional tools (like WebSearch) if desired.
func WithClaudeCodeTools(tools string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyTools] = tools
	}
}

func ensureMetadata(opts *llmtypes.CallOptions) {
	if opts.Metadata == nil {
		opts.Metadata = &llmtypes.Metadata{Custom: make(map[string]interface{})}
	}
	if opts.Metadata.Custom == nil {
		opts.Metadata.Custom = make(map[string]interface{})
	}
}

// GenerateContent generates content using the Claude Code CLI.
func (c *ClaudeCodeAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	// Parse options
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	// 1. Prepare Command Arguments
	args := []string{"-p", "--output-format", "json", "--input-format", "stream-json"}

	// Extract system prompt
	var systemPrompts []string
	var convoMessages []llmtypes.MessageContent

	for _, msg := range messages {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			// Extract text from system message parts
			for _, part := range msg.Parts {
				if textPart, ok := part.(llmtypes.TextContent); ok {
					systemPrompts = append(systemPrompts, textPart.Text)
				}
			}
		} else {
			convoMessages = append(convoMessages, msg)
		}
	}

	if len(systemPrompts) > 0 {
		args = append(args, "--system-prompt", strings.Join(systemPrompts, "\n\n"))
	}

	// Handle Custom Options
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mcpConfig, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && mcpConfig != "" {
			args = append(args, "--mcp-config", mcpConfig)
		}
		if skip, ok := opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions].(bool); ok && skip {
			args = append(args, "--dangerously-skip-permissions")
		}
		if tools, ok := opts.Metadata.Custom[MetadataKeyTools].(string); ok {
			args = append(args, "--tools", tools)
		}
	}

	// 2. Build Stream-JSON Input
	var inputStream bytes.Buffer
	encoder := json.NewEncoder(&inputStream)

	for _, msg := range convoMessages {
		jsonMsg, err := convertMessageToStreamJSON(msg)
		if err != nil {
			c.logger.Errorf("Failed to convert message to stream-json: %v", err)
			return nil, fmt.Errorf("failed to convert message: %w", err)
		}
		if err := encoder.Encode(jsonMsg); err != nil {
			return nil, fmt.Errorf("failed to encode message json: %w", err)
		}
	}

	// 3. Execute Command
	c.logger.Infof("Executing Claude Code CLI: claude %v", args)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = &inputStream
	
	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		c.logger.Errorf("Claude Code CLI failed: %v. Stderr: %s", err, stderr.String())
		return nil, fmt.Errorf("claude cli execution failed: %w, stderr: %s", err, stderr.String())
	}

	// 4. Parse Output
	var response ClaudeCodeResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		c.logger.Errorf("Failed to parse Claude Code response: %v. Output: %s", err, stdout.String())
		return nil, fmt.Errorf("failed to parse response json: %w", err)
	}

	return c.mapResponseToContentResponse(&response)
}

// GetModelID returns the model ID.
func (c *ClaudeCodeAdapter) GetModelID() string {
	return c.modelID
}

// GetModelMetadata returns metadata for the model.
func (c *ClaudeCodeAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	// Claude Code CLI abstracts the underlying model, but typically uses Sonnet 3.5 or Opus.
	// We return generic metadata.
	return &llmtypes.ModelMetadata{
		ModelID:   modelID,
		Provider:  "claude-code",
		ModelName: "Claude Code CLI",
	}, nil
}

// --- Helper Functions & Structs ---

type StreamJSONMessage struct {
	Type    string          `json:"type"`
	Message InternalMessage `json:"message"`
}

type InternalMessage struct {
	Role    string        `json:"role"`
	Content []interface{} `json:"content"`
}

type TextContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func convertMessageToStreamJSON(msg llmtypes.MessageContent) (*StreamJSONMessage, error) {
	role := "user"
	if msg.Role == llmtypes.ChatMessageTypeAI {
		role = "assistant"
	}

	var content []interface{}
	for _, part := range msg.Parts {
		if textPart, ok := part.(llmtypes.TextContent); ok {
			content = append(content, TextContentBlock{
				Type: "text",
				Text: textPart.Text,
			})
		}
		// TODO: Add support for ImageContent when CLI supports it via stream-json or file attachments
	}

	return &StreamJSONMessage{
		Type: role,
		Message: InternalMessage{
			Role:    role,
			Content: content,
		},
	}, nil
}

// ClaudeCodeResponse mirrors the JSON output from `claude -p --output-format json`
type ClaudeCodeResponse struct {
	Type              string             `json:"type"`
	Result            string             `json:"result"`
	Usage             ClaudeUsage        `json:"usage"`
	TotalCostUSD      float64            `json:"total_cost_usd"`
	PermissionDenials []PermissionDenial `json:"permission_denials,omitempty"`
}

type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type PermissionDenial struct {
	ToolName string      `json:"tool_name"`
	Reason   string      `json:"reason"`
	Input    interface{} `json:"input"`
}

func (c *ClaudeCodeAdapter) mapResponseToContentResponse(resp *ClaudeCodeResponse) (*llmtypes.ContentResponse, error) {
	totalTokens := resp.Usage.InputTokens + resp.Usage.OutputTokens

	// Map Usage
	usage := &llmtypes.Usage{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		TotalTokens:  totalTokens,
		CacheTokens:  &resp.Usage.CacheReadInputTokens,
	}

	// Map GenerationInfo
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  &resp.Usage.InputTokens,
		OutputTokens: &resp.Usage.OutputTokens,
		TotalTokens:  &totalTokens,
		Additional: map[string]interface{}{
			"cost_usd":              resp.TotalCostUSD,
			"cache_creation_tokens": resp.Usage.CacheCreationInputTokens,
		},
	}

	// Handle Permission Denials
	if len(resp.PermissionDenials) > 0 {
		genInfo.Additional["permission_denials"] = resp.PermissionDenials
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        resp.Result,
				GenerationInfo: genInfo,
			},
		},
		Usage: usage,
	}, nil
}
