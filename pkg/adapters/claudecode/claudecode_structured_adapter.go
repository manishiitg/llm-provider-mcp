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
	Type string `json:"type"` // "text" | "tool_use" | "tool_result"
	Text string `json:"text,omitempty"`
	// tool_use fields (assistant events).
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result fields (user events) — Claude's structured stream reports a
	// completed tool call as a subsequent "user"-role event carrying one of
	// these per tool_use_id (verified live against claude-code 2026.07.23).
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// claudeToolResultText renders a tool_result block's content as plain text.
// Claude's structured stream reports it as a plain JSON string in every case
// observed live; a content-block array is handled defensively since that is
// the general Anthropic Messages API shape for a tool_result.
func claudeToolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			b.WriteString(blk.Text)
		}
		return b.String()
	}
	return string(raw)
}

type claudeStreamUsage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
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

	var tempFiles []string
	defer func() {
		for _, f := range tempFiles {
			_ = os.Remove(f)
		}
	}()
	// MCP-config file write is a disk side-effect; the resolved path feeds the
	// argv builder below.
	mcpConfigPath := ""
	if mcpConfigJSON != "" {
		configPath, cfgErr := writeTempJSONConfig("claude-code-structured-mcp-*.json", mcpConfigJSON)
		if cfgErr != nil {
			return nil, fmt.Errorf("claude structured MCP config: %w", cfgErr)
		}
		tempFiles = append(tempFiles, configPath)
		mcpConfigPath = configPath
	}

	// Mint a fresh session id only when NOT resuming (avoid burning an id on a
	// resume turn). argv SHAPE + which id the turn runs under are owned by the
	// unit-tested builder — see buildClaudeStructuredArgs / TestBuildClaudeStructuredArgs.
	resumeSessionID := strings.TrimSpace(claudeResumeSessionIDFromStructuredOptions(opts))
	freshSessionID := ""
	if resumeSessionID == "" {
		freshSessionID = newClaudeNativeSessionID()
	}
	args, sessionID := buildClaudeStructuredArgs(c.modelID, systemPrompt, allowedTools, mcpConfigPath, resumeSessionID, freshSessionID, workingDir)

	if workingDir != "" {
		if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
			// Project skills to <workdir>/.claude/skills — claude discovers them
			// natively from the working dir, same as the tmux path. (Disk
			// side-effect; --add-dir is added by the builder.)
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
	// totalUsage is fed ONLY from the terminal result event's usage, which
	// Anthropic documents as the per-run cumulative total — NOT summed with
	// each intermediate assistant event's own usage. Verified live: summing
	// both roughly doubled input/cache token counts (which dominate real
	// cost) — e.g. a real capture showed assistant-event usages of
	// input=2+2=4 and cache_read=23591+37449=61040, each EXACTLY matching the
	// terminal result's own input=4/cache_read=61040 — proving the result
	// event already IS the total, not a delta to add on top.
	// assistantUsageFallback is a safety net for the rare case a result event
	// carries no usage at all.
	var totalUsage llmtypes.Usage
	var assistantUsageFallback llmtypes.Usage
	sawResult := false
	resultIsError := false
	// pendingToolCalls correlates a ToolCallEnd back to its Start: the name
	// and args are only present on the tool_use block (an "assistant" event),
	// while the completion is a SEPARATE later "user" event carrying only the
	// result, keyed by tool_use_id. Without this, ToolCallEnd streamed with
	// no name/args at all — family-server's history reconstruction reads
	// exclusively from End chunks, so every reconstructed tool call had a
	// blank name and arguments.
	pendingToolCalls := map[string]struct {
		name string
		args string
	}{}
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
							if block.ID != "" {
								pendingToolCalls[block.ID] = struct {
									name string
									args string
								}{name: block.Name, args: string(block.Input)}
							}
							emitChunk(llmtypes.StreamChunk{
								Type:       llmtypes.StreamChunkTypeToolCallStart,
								ToolName:   block.Name,
								ToolCallID: block.ID,
							})
						}
					}
					if ev.Message.Usage != nil {
						accumulateClaudeUsage(&assistantUsageFallback, ev.Message.Usage)
					}
				}
			case "user":
				// The completion side of a tool call's lifecycle: Claude's
				// structured stream reports a finished tool call as a
				// subsequent "user"-role event carrying a tool_result block
				// keyed by tool_use_id — verified live. Without this, every
				// structured tool call streamed a Start with no matching End.
				if ev.Message != nil {
					for _, block := range ev.Message.Content {
						if block.Type != "tool_result" || block.ToolUseID == "" {
							continue
						}
						result := claudeToolResultText(block.Content)
						if block.IsError {
							result = "[ERROR] " + result
						}
						pending := pendingToolCalls[block.ToolUseID]
						delete(pendingToolCalls, block.ToolUseID)
						emitChunk(llmtypes.StreamChunk{
							Type:       llmtypes.StreamChunkTypeToolCallEnd,
							ToolCallID: block.ToolUseID,
							ToolName:   pending.name,
							ToolArgs:   pending.args,
							ToolResult: result,
						})
					}
				}
			case "result":
				sawResult = true
				resultIsError = ev.IsError
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
	// A "result" event with is_error=true is a genuine semantic failure (e.g.
	// an API error surfaced as the final message) — it must not be reported
	// as a successful StopReason:"stop" response. Checked regardless of
	// waitErr/sawResult: the CLI can exit 0 while still reporting is_error.
	if resultIsError {
		msg := strings.TrimSpace(resultText)
		if msg == "" {
			msg = strings.TrimSpace(stderr.String())
		}
		return nil, fmt.Errorf("claude run reported an error result: %s", msg)
	}
	if totalUsage.TotalTokens == 0 {
		totalUsage = assistantUsageFallback
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
