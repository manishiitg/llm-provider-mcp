package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// pendingToolCall tracks a tool call that has started but hasn't received its result yet
type pendingToolCall struct {
	toolName  string
	toolID    string
	toolArgs  string
	startTime time.Time
}

// Constants for custom metadata keys
const (
	MetadataKeyMCPConfig                 = "mcp_config"
	MetadataKeyDangerouslySkipPermissions = "dangerously_skip_permissions"
	MetadataKeyTools                     = "claude_code_tools"
	MetadataKeyAllowedTools              = "claude_code_allowed_tools"
	MetadataKeySettings                  = "claude_code_settings"
	MetadataKeyMaxTurns                  = "claude_code_max_turns"
	MetadataKeyResumeSessionID           = "claude_code_resume_session_id"
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

// WithClaudeCodeSettings sets the --settings flag to a JSON string or file path.
func WithClaudeCodeSettings(settings string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeySettings] = settings
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

// WithAllowedTools sets the --allowed-tools flag to whitelist specific tools
// from requiring permission confirmation.
// Example: "mcp__mcpbridge__*" to allow all tools from the mcpbridge server.
func WithAllowedTools(tools string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAllowedTools] = tools
	}
}

// WithMaxTurns sets the --max-turns flag to limit the number of agentic turns.
// Claude Code exits with an error when the limit is reached.
func WithMaxTurns(maxTurns int) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMaxTurns] = maxTurns
	}
}

// WithResumeSessionID sets the --resume flag with a session ID so the Claude Code CLI
// resumes an existing session instead of starting a new one.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
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
	// 0. Check for 'claude' binary
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claude cli not found in PATH. Please install it first (npm install -g @anthropics/claude-code)")
	}

	// Parse options
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	// 1. Prepare Command Arguments
	// Note: --input-format stream-json requires --output-format stream-json and --verbose
	args := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--include-partial-messages"}

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
		args = append(args, "--append-system-prompt", strings.Join(systemPrompts, "\n\n"))
	}

	// Handle Custom Options
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mcpConfig, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && mcpConfig != "" {
			args = append(args, "--mcp-config", mcpConfig, "--strict-mcp-config")
		}
		if settings, ok := opts.Metadata.Custom[MetadataKeySettings].(string); ok && settings != "" {
			args = append(args, "--settings", settings)
		}
		if skip, ok := opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions].(bool); ok && skip {
			args = append(args, "--dangerously-skip-permissions")
		}
		if tools, ok := opts.Metadata.Custom[MetadataKeyTools].(string); ok {
			args = append(args, "--tools", tools)
		}
		if allowedTools, ok := opts.Metadata.Custom[MetadataKeyAllowedTools].(string); ok && allowedTools != "" {
			args = append(args, "--allowed-tools", allowedTools)
		}
		if maxTurns, ok := opts.Metadata.Custom[MetadataKeyMaxTurns].(int); ok && maxTurns > 0 {
			args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
		}
		if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && resumeID != "" {
			args = append(args, "--resume", resumeID)
		}
	}

	// Ensure StreamChan is closed on exit if it was provided
	if opts.StreamChan != nil {
		defer close(opts.StreamChan)
	}

	// Check if we're resuming an existing session
	resumeID := ""
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if rid, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
			resumeID = rid
		}
	}

	// 2. Build Stream-JSON Input
	var inputStream bytes.Buffer
	encoder := json.NewEncoder(&inputStream)

	if resumeID != "" {
		// Resuming: only send the last user message (CLI has full history internally)
		for i := len(convoMessages) - 1; i >= 0; i-- {
			if convoMessages[i].Role == llmtypes.ChatMessageTypeHuman {
				jsonMsg, err := convertMessageToStreamJSON(convoMessages[i])
				if err != nil {
					c.logger.Errorf("Failed to convert message to stream-json: %v", err)
					return nil, fmt.Errorf("failed to convert message: %w", err)
				}
				if err := encoder.Encode(jsonMsg); err != nil {
					return nil, fmt.Errorf("failed to encode message json: %w", err)
				}
				break
			}
		}
	} else {
		// First turn: send full history as before
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
	}

	// 3. Execute Command
	c.logger.Infof("Executing Claude Code CLI: claude %v", args)
	c.logger.Debugf("Input stream: %s", inputStream.String())
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = &inputStream

	// Filter out CLAUDECODE env var to allow nested invocation (e.g., when
	// this adapter is called from within a Claude Code session during testing)
	var filteredEnv []string
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "CLAUDECODE=") {
			filteredEnv = append(filteredEnv, env)
		}
	}
	cmd.Env = filteredEnv

	// Use Pipe for stdout to parse as a stream
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr so we can log it (helps debug permission prompts / errors)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start claude cli: %w", err)
	}

	// 4. Parse Streamed Output
	var finalResponse *llmtypes.ContentResponse
	decoder := json.NewDecoder(stdoutPipe)
	
	// Count AI messages in history to skip them during playback streaming
	// When resuming, we only sent the new user message so there's no history to skip
	aiHistoryCount := 0
	if resumeID == "" {
		for _, msg := range convoMessages {
			if msg.Role == llmtypes.ChatMessageTypeAI {
				aiHistoryCount++
			}
		}
	}
	aiSeenCount := 0

	// Create a channel to signal completion of decoding
	decodeDone := make(chan bool)

	var currentToolName string
	var currentToolID string
	var currentToolInput strings.Builder
	var inToolBlock bool
	hasStreamEvents := false
	// Buffer pending tool calls to match with tool_result for complete events
	pendingTools := make(map[string]*pendingToolCall)

	go func() {
		c.logger.Infof("Starting stream decode loop...")
		for decoder.More() {
			var raw map[string]interface{}
			if err := decoder.Decode(&raw); err != nil {
				c.logger.Errorf("Failed to decode stream-json object: %v", err)
				break
			}
			c.logger.Infof("Decoded raw stream object of type: %v, raw: %+v", raw["type"], raw)

			msgType, _ := raw["type"].(string)
			switch msgType {
			case "stream_event":
				hasStreamEvents = true
				event, _ := raw["event"].(map[string]interface{})
				if event == nil {
					continue
				}
				eventType, _ := event["type"].(string)

				switch eventType {
				case "content_block_start":
					cb, _ := event["content_block"].(map[string]interface{})
					if cb == nil {
						break
					}
					cbType, _ := cb["type"].(string)
					if cbType == "tool_use" {
						currentToolName, _ = cb["name"].(string)
						currentToolID, _ = cb["id"].(string)
						currentToolInput.Reset()
						inToolBlock = true
						
						// Track start time for duration calculation
						pendingTools[currentToolID] = &pendingToolCall{
							toolName:  currentToolName,
							toolID:    currentToolID,
							startTime: time.Now(),
						}
					}

				case "content_block_delta":
					delta, _ := event["delta"].(map[string]interface{})
					if delta == nil {
						break
					}
					deltaType, _ := delta["type"].(string)
					if deltaType == "text_delta" {
						if txt, ok := delta["text"].(string); ok && txt != "" && !inToolBlock {
							if opts.StreamChan != nil {
								opts.StreamChan <- llmtypes.StreamChunk{
									Type:    llmtypes.StreamChunkTypeContent,
									Content: txt,
								}
							}
						}
					} else if deltaType == "input_json_delta" {
						if partialJSON, ok := delta["partial_json"].(string); ok {
							currentToolInput.WriteString(partialJSON)
						}
					}

				case "content_block_stop":
					if inToolBlock {
						toolArgs := currentToolInput.String()
						// Emit ToolCallStart now that we have the full arguments
						if opts.StreamChan != nil {
							opts.StreamChan <- llmtypes.StreamChunk{
								Type:       llmtypes.StreamChunkTypeToolCallStart,
								ToolName:   currentToolName,
								ToolCallID: currentToolID,
								ToolArgs:   toolArgs,
							}
						}

						// Save args to pending tool (don't emit ToolCallEnd yet — wait for tool_result)
						if pt, ok := pendingTools[currentToolID]; ok {
							pt.toolArgs = toolArgs
						}
						inToolBlock = false
						currentToolName = ""
						currentToolID = ""
						currentToolInput.Reset()
					}
				}

			case "user":
				// Parse tool_result messages to complete pending tool calls
				if msg, ok := raw["message"].(map[string]interface{}); ok {
					if content, ok := msg["content"].([]interface{}); ok {
						for _, cPart := range content {
							cp, ok := cPart.(map[string]interface{})
							if !ok {
								continue
							}
							if cp["type"] != "tool_result" {
								continue
							}
							toolUseID, _ := cp["tool_use_id"].(string)
							if toolUseID == "" {
								continue
							}
							resultContent, _ := cp["content"].(string)

							if pt, ok := pendingTools[toolUseID]; ok {
								duration := time.Since(pt.startTime)
								if opts.StreamChan != nil {
									opts.StreamChan <- llmtypes.StreamChunk{
										Type:         llmtypes.StreamChunkTypeToolCallEnd,
										ToolName:     pt.toolName,
										ToolCallID:   pt.toolID,
										ToolArgs:     pt.toolArgs,
										ToolResult:   resultContent,
										ToolDuration: duration,
									}
								}
								delete(pendingTools, toolUseID)
							}
						}
					}
				}

			case "assistant":
				aiSeenCount++
				// Only stream tokens if we've passed all historical AI messages
				if aiSeenCount <= aiHistoryCount {
					continue
				}

				// If we are getting stream_events, we don't need to parse the consolidated assistant message for text streaming
				if hasStreamEvents {
					continue
				}

				// Handle assistant message (could be a chunk or a complete message)
				if msg, ok := raw["message"].(map[string]interface{}); ok {
					if content, ok := msg["content"].([]interface{}); ok {
						for _, cPart := range content {
							if cp, ok := cPart.(map[string]interface{}); ok {
								if txt, ok := cp["text"].(string); ok && txt != "" {
									// If user requested streaming, send chunk
									if opts.StreamChan != nil {
										opts.StreamChan <- llmtypes.StreamChunk{
											Type:    llmtypes.StreamChunkTypeContent,
											Content: txt,
										}
									}
								}
							}
						}
					}
				}
			case "result":
				// Flush any remaining pending tool calls that never got a tool_result
				for _, pt := range pendingTools {
					if opts.StreamChan != nil {
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:         llmtypes.StreamChunkTypeToolCallEnd,
							ToolName:     pt.toolName,
							ToolCallID:   pt.toolID,
							ToolArgs:     pt.toolArgs,
							ToolDuration: time.Since(pt.startTime),
						}
					}
				}
				pendingTools = make(map[string]*pendingToolCall)

				// Parse the final result summary
				var claudeResp ClaudeCodeResponse
				jsonBytes, _ := json.Marshal(raw)
				if err := json.Unmarshal(jsonBytes, &claudeResp); err == nil {
					finalResponse, _ = c.mapResponseToContentResponse(&claudeResp)
				}
			}
		}
		decodeDone <- true
	}()

	// Wait for command completion or context cancellation
	var cmdErr error
	select {
	case <-ctx.Done():
		c.logger.Errorf("Context cancelled/timed out: %v", ctx.Err())
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmdErr = ctx.Err()
	case <-decodeDone:
		cmdErr = cmd.Wait()
	}

	// Log stderr output from Claude CLI (captures permission prompts, errors, debug info)
	if stderrOutput := stderrBuf.String(); stderrOutput != "" {
		c.logger.Infof("Claude Code CLI stderr:\n%s", stderrOutput)
	}

	if cmdErr != nil {
		c.logger.Errorf("Claude Code CLI failed with error: %v. stderr: %s", cmdErr, stderrBuf.String())
		// If we already have a final response (sometimes CLI errors out after finishing), we might still want to return it
		if finalResponse == nil {
			return nil, fmt.Errorf("claude cli execution failed: %w", cmdErr)
		}
	}

	if finalResponse == nil {
		return nil, fmt.Errorf("failed to receive final result from claude cli")
	}

	return finalResponse, nil
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
		switch p := part.(type) {
		case llmtypes.TextContent:
			content = append(content, TextContentBlock{
				Type: "text",
				Text: p.Text,
			})
		}
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
	SessionID         string             `json:"session_id"`
	Result            string             `json:"result"`
	Usage             ClaudeUsage        `json:"usage"`
	TotalCostUSD      float64            `json:"total_cost_usd"`
	DurationMs        float64            `json:"duration_ms"`
	DurationAPIMs     float64            `json:"duration_api_ms"`
	NumTurns          int                `json:"num_turns"`
	ModelUsage        map[string]ModelUsageEntry `json:"modelUsage,omitempty"`
	PermissionDenials []PermissionDenial `json:"permission_denials,omitempty"`
}

type ClaudeUsage struct {
	InputTokens              int              `json:"input_tokens"`
	OutputTokens             int              `json:"output_tokens"`
	CacheReadInputTokens     int              `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int              `json:"cache_creation_input_tokens"`
	ServiceTier              string           `json:"service_tier,omitempty"`
	ServerToolUse            *ServerToolUse   `json:"server_tool_use,omitempty"`
}

type ServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests"`
	WebFetchRequests  int `json:"web_fetch_requests"`
}

type ModelUsageEntry struct {
	InputTokens           int     `json:"inputTokens"`
	OutputTokens          int     `json:"outputTokens"`
	CacheReadInputTokens  int     `json:"cacheReadInputTokens"`
	CacheCreationTokens   int     `json:"cacheCreationInputTokens"`
	WebSearchRequests     int     `json:"webSearchRequests"`
	CostUSD               float64 `json:"costUSD"`
	ContextWindow         int     `json:"contextWindow"`
	MaxOutputTokens       int     `json:"maxOutputTokens"`
}

type PermissionDenial struct {
	ToolName  string      `json:"tool_name"`
	ToolUseID string      `json:"tool_use_id"`
	ToolInput interface{} `json:"tool_input"`
}

func (c *ClaudeCodeAdapter) mapResponseToContentResponse(resp *ClaudeCodeResponse) (*llmtypes.ContentResponse, error) {
	// In the Anthropic API, input_tokens = non-cached input only.
	// Total input context = input_tokens + cache_read_input_tokens.
	totalInputTokens := resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens
	totalTokens := totalInputTokens + resp.Usage.OutputTokens

	// Map Usage
	usage := &llmtypes.Usage{
		InputTokens:  totalInputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		TotalTokens:  totalTokens,
		CacheTokens:  &resp.Usage.CacheReadInputTokens,
	}

	// Map GenerationInfo
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:         &totalInputTokens,
		OutputTokens:        &resp.Usage.OutputTokens,
		TotalTokens:         &totalTokens,
		CachedContentTokens: &resp.Usage.CacheReadInputTokens,
		Additional: map[string]interface{}{
			"cost_usd":               resp.TotalCostUSD,
			"cache_creation_tokens":  resp.Usage.CacheCreationInputTokens,
			"claude_code_session_id": resp.SessionID,
		},
	}

	// Add duration and turn count from result event
	if resp.DurationMs > 0 {
		genInfo.Additional["claude_code_duration_ms"] = resp.DurationMs
	}
	if resp.DurationAPIMs > 0 {
		genInfo.Additional["claude_code_duration_api_ms"] = resp.DurationAPIMs
	}
	if resp.NumTurns > 0 {
		genInfo.Additional["claude_code_num_turns"] = resp.NumTurns
	}

	// Add per-model usage breakdown (includes resolved model name, context window, cost)
	if len(resp.ModelUsage) > 0 {
		genInfo.Additional["claude_code_model_usage"] = resp.ModelUsage
		// Extract the resolved model name (first key in modelUsage)
		for modelName := range resp.ModelUsage {
			genInfo.Additional["claude_code_model"] = modelName
			break
		}
	}

	// Add service tier
	if resp.Usage.ServiceTier != "" {
		genInfo.Additional["claude_code_service_tier"] = resp.Usage.ServiceTier
	}

	// Add server tool use counts (web search, web fetch)
	if resp.Usage.ServerToolUse != nil {
		if resp.Usage.ServerToolUse.WebSearchRequests > 0 {
			genInfo.Additional["claude_code_web_search_requests"] = resp.Usage.ServerToolUse.WebSearchRequests
		}
		if resp.Usage.ServerToolUse.WebFetchRequests > 0 {
			genInfo.Additional["claude_code_web_fetch_requests"] = resp.Usage.ServerToolUse.WebFetchRequests
		}
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
