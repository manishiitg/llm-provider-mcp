package cursorcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type cursorEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// system init
	SessionID      string `json:"session_id,omitempty"`
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`

	// text/thinking deltas
	Text string `json:"text,omitempty"`

	// assistant / user message
	Message *cursorEventMessage `json:"message,omitempty"`

	// tool_call
	CallID   string          `json:"call_id,omitempty"`
	ToolCall json.RawMessage `json:"tool_call,omitempty"`

	// result
	Result    string            `json:"result,omitempty"`
	IsError   bool              `json:"is_error,omitempty"`
	Usage     *cursorEventUsage `json:"usage,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

type cursorEventMessage struct {
	Role    string               `json:"role"`
	Content []cursorEventContent `json:"content"`
}

type cursorEventContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cursorEventUsage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
}

func (c *CursorCLIAdapter) generateContentStructured(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions, sink *llmtypes.StreamSink) (*llmtypes.ContentResponse, error) {
	emitChunk := func(chunk llmtypes.StreamChunk) {
		if sink != nil {
			if err := sink.Emit(ctx, chunk); err != nil {
				c.logDebugf("cursor: stream emit failed: %v", err)
			}
			return
		}
		if opts.StreamChan == nil {
			return
		}
		select {
		case opts.StreamChan <- chunk:
		case <-ctx.Done():
		}
	}

	binPath, err := exec.LookPath("cursor-agent")
	if err != nil {
		return nil, fmt.Errorf("cursor-agent not found in PATH: %w", err)
	}

	systemPrompt, conversationMessages := splitCursorSystemPrompt(messages)
	prompt := buildCursorPrompt(conversationMessages, false)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("cursor-cli prompt is empty")
	}

	if strings.TrimSpace(systemPrompt) != "" {
		prompt = "[System Instructions]\n" + systemPrompt + "\n\n[User Message]\n" + prompt
	}

	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--trust",
		"--force",
	}

	workingDir := cursorWorkingDirFromOptions(opts)
	if workingDir != "" {
		args = append(args, "--workspace", workingDir)
	}

	modelToUse := resolveCursorCLIModelID(c.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyCursorModel].(string); ok && strings.TrimSpace(model) != "" {
			modelToUse = resolveCursorCLIModelID(model)
		}
	}
	if modelToUse != "" {
		args = append(args, "--model", modelToUse)
	}

	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mode, ok := opts.Metadata.Custom[MetadataKeyMode].(string); ok && strings.TrimSpace(mode) != "" {
			args = append(args, "--mode", strings.TrimSpace(mode))
		}
		if sandbox, ok := opts.Metadata.Custom[MetadataKeySandbox].(string); ok && strings.TrimSpace(sandbox) != "" {
			args = append(args, "--sandbox", strings.TrimSpace(sandbox))
		}
		if approve, ok := opts.Metadata.Custom[MetadataKeyApproveMCPs].(bool); ok && approve {
			args = append(args, "--approve-mcps")
		}
		if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && strings.TrimSpace(resumeID) != "" {
			args = append(args, "--resume", strings.TrimSpace(resumeID))
		}
	}

	var configCleanups []func()
	defer func() {
		for _, fn := range configCleanups {
			fn()
		}
	}()
	if workingDir != "" && opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		cursorDir := filepath.Join(workingDir, ".cursor")
		if mcpJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpJSON) != "" {
			cleanup, werr := writeCursorRestoredFile(filepath.Join(cursorDir, "mcp.json"), []byte(mcpJSON))
			if werr != nil {
				return nil, fmt.Errorf("cursor MCP config: %w", werr)
			}
			configCleanups = append(configCleanups, cleanup)
		}
	}

	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = buildCursorStructuredEnv(c.apiKey)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cursor stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	c.logInfof("Executing Cursor CLI structured: cursor-agent --print --output-format stream-json")
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cursor start: %w", err)
	}

	var finalContent string
	var totalUsage llmtypes.Usage
	var sessionID string
	var modelName string

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var event cursorEvent
		if err := json.Unmarshal(line, &event); err != nil {
			c.logDebugf("cursor: failed to parse event: %v", err)
			continue
		}

		if sessionID == "" && event.SessionID != "" {
			sessionID = event.SessionID
		}

		switch event.Type {
		case "system":
			if event.Model != "" {
				modelName = event.Model
			}
			if event.SessionID != "" {
				sessionID = event.SessionID
			}

		case "thinking":
			if event.Subtype == "delta" && event.Text != "" {
				emitChunk(llmtypes.StreamChunk{
					Type:    llmtypes.StreamChunkTypeContent,
					Content: event.Text,
				})
			}

		case "assistant":
			if event.Message != nil {
				text := cursorEventMessageText(event.Message)
				if text != "" {
					finalContent = text
					emitChunk(llmtypes.StreamChunk{
						Type:    llmtypes.StreamChunkTypeContent,
						Content: text,
					})
				}
			}

		case "tool_call":
			switch event.Subtype {
			case "started":
				emitChunk(llmtypes.StreamChunk{
					Type:       llmtypes.StreamChunkTypeToolCallStart,
					Content:    fmt.Sprintf("tool_call(%s)", event.CallID),
					ToolCallID: event.CallID,
				})
			case "completed":
				emitChunk(llmtypes.StreamChunk{
					Type:       llmtypes.StreamChunkTypeToolCallEnd,
					Content:    event.CallID,
					ToolCallID: event.CallID,
				})
			}

		case "result":
			if event.Result != "" {
				finalContent = event.Result
			}
			if event.Usage != nil {
				totalUsage.InputTokens += event.Usage.InputTokens
				totalUsage.OutputTokens += event.Usage.OutputTokens
				totalUsage.TotalTokens += event.Usage.InputTokens + event.Usage.OutputTokens
				if event.Usage.CacheReadTokens > 0 {
					cacheRead := event.Usage.CacheReadTokens
					totalUsage.CacheTokens = &cacheRead
				}
			}
			if event.SessionID != "" {
				sessionID = event.SessionID
			}
		}
	}

	waitErr := cmd.Wait()

	content := strings.TrimSpace(finalContent)

	if waitErr != nil && content == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("cursor run failed: %w: %s", waitErr, stderrStr)
		}
		return nil, fmt.Errorf("cursor run failed: %w", waitErr)
	}

	if content == "" {
		return nil, fmt.Errorf("cursor run returned no text output")
	}

	additional := map[string]any{
		"provider":          "cursor-cli",
		"cursor_mode":       "structured",
		"cursor_session_id": sessionID,
		"cursor_model":      modelName,
	}
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtrFromInt(totalUsage.InputTokens),
		OutputTokens: intPtrFromInt(totalUsage.OutputTokens),
		TotalTokens:  intPtrFromInt(totalUsage.TotalTokens),
		Additional:   additional,
	}
	if totalUsage.CacheTokens != nil && *totalUsage.CacheTokens > 0 {
		v := *totalUsage.CacheTokens
		genInfo.CachedContentTokens = &v
		// Mirror under the raw Anthropic-style key the cost ledger reads.
		additional["cache_read_input_tokens"] = v
	}
	// Cost lookup: prefer the cursor-reported effective model name, fall
	// back to the requested model alias.
	costLookupModel := modelName
	if costLookupModel == "" {
		costLookupModel = modelToUse
	}
	if costLookupModel != "" {
		if meta, _ := c.GetModelMetadata(costLookupModel); meta != nil {
			if cost := llmtypes.ComputeUSDCostFromMetadata(meta, genInfo); cost > 0 {
				additional["cost_usd_estimated"] = cost
				additional["cost_model_id"] = costLookupModel
			}
		}
	}
	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        content,
				StopReason:     "stop",
				GenerationInfo: genInfo,
			},
		},
		Usage: &totalUsage,
	}, nil
}

func intPtrFromInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func cursorEventMessageText(msg *cursorEventMessage) string {
	var parts []string
	for _, c := range msg.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "")
}

func buildCursorStructuredEnv(apiKey string) []string {
	env := os.Environ()
	if strings.TrimSpace(apiKey) != "" {
		env = append(env, "CURSOR_API_KEY="+strings.TrimSpace(apiKey))
	}
	return env
}

func (c *CursorCLIAdapter) logDebugf(format string, args ...interface{}) {
	if c.logger != nil {
		c.logger.Debugf(format, args...)
	}
}
