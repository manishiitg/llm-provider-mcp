package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
)

// claudeStreamEvent is one NDJSON line from
// `claude -p --output-format stream-json`. The event stream is: a
// system/init event (carries session_id + the resolved tool list), then
// assistant events (message.content text/tool_use blocks), then a terminal
// result event (final text in `result`, plus `usage` and `session_id`).
type claudeStreamEvent struct {
	Type      string               `json:"type"`
	Subtype   string               `json:"subtype,omitempty"`
	SessionID string               `json:"session_id,omitempty"`
	Message   *claudeStreamMessage `json:"message,omitempty"`
	Result    string               `json:"result,omitempty"`
	IsError   bool                 `json:"is_error,omitempty"`
	Usage     *claudeStreamUsage   `json:"usage,omitempty"`
}

type claudeStreamMessage struct {
	Content []claudeStreamContentBlock `json:"content,omitempty"`
	Usage   *claudeStreamUsage         `json:"usage,omitempty"`
}

type claudeStreamContentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | ...
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"` // tool_use name
}

type claudeStreamUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

// generateContentStructured drives `claude -p --output-format stream-json` —
// per-turn, one-shot, no tmux dependency. Mirrors the codex/cursor/pi structured
// adapters. Multi-turn continuity is native --resume (claude persists the
// session under --session-id and continues it via --resume). See
// MetadataKeyStructuredTransport for when this is selected instead of tmux.
func (c *ClaudeCodeInteractiveAdapter) generateContentStructured(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if opts != nil && opts.StreamChan != nil {
		defer close(opts.StreamChan)
	}
	emitChunk := func(chunk llmtypes.StreamChunk) {
		if opts == nil || opts.StreamChan == nil {
			return
		}
		select {
		case opts.StreamChan <- chunk:
		case <-ctx.Done():
		}
	}

	binPath, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude not found in PATH: %w", err)
	}

	systemPrompt, conversationMessages := splitSystemPrompt(messages)
	prompt := buildClaudeStructuredPrompt(conversationMessages)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("claude-code prompt is empty")
	}

	workingDir := ""
	var allowedTools, mcpConfigJSON string
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if dir, ok := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); ok {
			workingDir = strings.TrimSpace(dir)
		}
		if v, ok := opts.Metadata.Custom[MetadataKeyAllowedTools].(string); ok {
			allowedTools = strings.TrimSpace(v)
		}
		if v, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok {
			mcpConfigJSON = strings.TrimSpace(v)
		}
	}

	// --output-format stream-json REQUIRES --verbose on current claude builds.
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
	if strings.TrimSpace(c.modelID) != "" {
		args = append(args, "--model", c.modelID)
	}
	if strings.TrimSpace(systemPrompt) != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	// Bridge-only enforcement. --allowedTools is only a permission WHITELIST, and
	// --dangerously-skip-permissions overrides it (found live: claude fell back to
	// its native Bash when the bridge tool kept failing). --disallowedTools is a
	// hard DENYLIST that holds regardless, so explicitly deny claude's built-in
	// shell/file tools — the print-mode analogue of the tmux deny hooks — forcing
	// every tool call through the MCP bridge.
	if allowedTools != "" {
		args = append(args, "--allowedTools", allowedTools)
		args = append(args, "--disallowedTools", "Bash,Read,Edit,Write,MultiEdit,NotebookEdit,Glob,Grep,WebFetch,Task")
	}

	var tempFiles []string
	defer func() {
		for _, f := range tempFiles {
			_ = os.Remove(f)
		}
	}()
	if mcpConfigJSON != "" {
		configPath, cfgErr := writeTempJSONConfig("claude-code-structured-mcp-*.json", mcpConfigJSON)
		if cfgErr != nil {
			return nil, fmt.Errorf("claude structured MCP config: %w", cfgErr)
		}
		tempFiles = append(tempFiles, configPath)
		args = append(args, "--mcp-config", configPath, "--strict-mcp-config")
	}

	// Multi-turn resume: on a resume turn use the prior session id; on a fresh
	// turn mint one (--session-id) so it can be surfaced (claude_code_session_id
	// below) and resumed next turn. Same capture-and-resume contract as pi.
	resumeSessionID := strings.TrimSpace(claudeResumeSessionIDFromStructuredOptions(opts))
	sessionID := resumeSessionID
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	} else {
		sessionID = newClaudeNativeSessionID()
		args = append(args, "--session-id", sessionID)
	}

	if workingDir != "" {
		args = append(args, "--add-dir", workingDir)
		if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
			// Project skills to <workdir>/.claude/skills — claude discovers them
			// natively from the working dir, same as the tmux path.
			_ = c.ProjectSkills(workingDir, skills)
		}
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(prompt) // prompt via stdin (--input-format text default)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if c.logger != nil {
		if resumeSessionID != "" {
			c.logger.Infof("Executing Claude Code structured: claude -p --output-format stream-json --resume <id>")
		} else {
			c.logger.Infof("Executing Claude Code structured: claude -p --output-format stream-json")
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude start: %w", err)
	}

	var finalText strings.Builder
	var resultText string
	var totalUsage llmtypes.Usage
	sawResult := false
	scannerDone := make(chan struct{})

	go func() {
		defer close(scannerDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || line[0] != '{' {
				continue
			}
			var ev claudeStreamEvent
			if jErr := json.Unmarshal([]byte(line), &ev); jErr != nil {
				continue
			}
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}
			switch ev.Type {
			case "assistant":
				if ev.Message != nil {
					for _, block := range ev.Message.Content {
						if block.Type == "text" && block.Text != "" {
							finalText.WriteString(block.Text)
							emitChunk(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: block.Text})
						}
						if block.Type == "tool_use" && block.Name != "" {
							emitChunk(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallStart, ToolName: block.Name})
						}
					}
					if ev.Message.Usage != nil {
						accumulateClaudeUsage(&totalUsage, ev.Message.Usage)
					}
				}
			case "result":
				sawResult = true
				if strings.TrimSpace(ev.Result) != "" {
					resultText = ev.Result
				}
				if ev.Usage != nil {
					accumulateClaudeUsage(&totalUsage, ev.Usage)
				}
				// Terminal event: we have the final answer, so tear the process
				// down rather than wait for it to exit on its own.
				go procshutdown.Graceful(cmd, scannerDone, c.logger)
			}
		}
	}()

	<-scannerDone
	waitErr := cmd.Wait()
	if waitErr != nil && ctx.Err() == nil && !sawResult {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("claude run failed: %w: %s", waitErr, stderrStr)
		}
		return nil, fmt.Errorf("claude run failed: %w", waitErr)
	}

	content := strings.TrimSpace(resultText)
	if content == "" {
		content = strings.TrimSpace(finalText.String())
	}
	if content == "" {
		return nil, fmt.Errorf("claude run returned no text output; stderr: %s", strings.TrimSpace(stderr.String()))
	}

	additional := map[string]any{
		"provider":               "claude-code",
		"claude_code_mode":       "structured",
		"claude_code_session_id": sessionID, // surfaced so mcpagent captures a.ClaudeCodeSessionID and can --resume next turn
	}
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtrIfNonZeroClaude(totalUsage.InputTokens),
		OutputTokens: intPtrIfNonZeroClaude(totalUsage.OutputTokens),
		TotalTokens:  intPtrIfNonZeroClaude(totalUsage.InputTokens + totalUsage.OutputTokens),
		Additional:   additional,
	}
	if totalUsage.CacheTokens != nil && *totalUsage.CacheTokens > 0 {
		v := *totalUsage.CacheTokens
		genInfo.CachedContentTokens = &v
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{Content: content, StopReason: "stop", GenerationInfo: genInfo},
		},
		Usage: &totalUsage,
	}, nil
}

func accumulateClaudeUsage(dst *llmtypes.Usage, src *claudeStreamUsage) {
	if src == nil {
		return
	}
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens = dst.InputTokens + dst.OutputTokens
	if src.CacheReadInputTokens > 0 {
		cur := 0
		if dst.CacheTokens != nil {
			cur = *dst.CacheTokens
		}
		cur += src.CacheReadInputTokens
		dst.CacheTokens = &cur
	}
}

func intPtrIfNonZeroClaude(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// buildClaudeStructuredPrompt joins the conversation messages into a single
// prompt string. On a resume turn the caller sends only the latest human
// message; on a fresh turn it may carry replayed history.
func buildClaudeStructuredPrompt(messages []llmtypes.MessageContent) string {
	var parts []string
	for _, m := range messages {
		var text strings.Builder
		for _, p := range m.Parts {
			if tc, ok := p.(llmtypes.TextContent); ok {
				text.WriteString(tc.Text)
			}
		}
		t := strings.TrimSpace(text.String())
		if t == "" {
			continue
		}
		switch m.Role {
		case llmtypes.ChatMessageTypeAI:
			parts = append(parts, "Assistant: "+t)
		case llmtypes.ChatMessageTypeHuman:
			parts = append(parts, t)
		default:
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func claudeResumeSessionIDFromStructuredOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if id, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		return strings.TrimSpace(id)
	}
	return ""
}
