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
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/toolclock"
)

const claudeStructuredNaturalExitGrace = 3 * time.Second

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
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
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
	var tools, allowedTools, permissionMode, mcpConfigJSON string
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if dir, ok := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); ok {
			workingDir = strings.TrimSpace(dir)
		}
		if v, ok := opts.Metadata.Custom[MetadataKeyAllowedTools].(string); ok {
			allowedTools = strings.TrimSpace(v)
		}
		if v, ok := opts.Metadata.Custom[MetadataKeyTools].(string); ok {
			tools = strings.TrimSpace(v)
		}
		if v, ok := opts.Metadata.Custom[MetadataKeyPermissionMode].(string); ok {
			permissionMode = strings.TrimSpace(v)
		}
		if v, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok {
			mcpConfigJSON = strings.TrimSpace(v)
		}
	}

	var tempFiles []string
	defer func() {
		// removeFiles, not a bare os.Remove loop: writeClaudeCodeProjectInstructionFile
		// may register an operator's prior CLAUDE.md bytes for byte-restore, and
		// deleting the path instead of restoring it would destroy their content.
		removeFiles(tempFiles)
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

	// Project the system prompt into <workingDir>/CLAUDE.md, as the interactive
	// adapter does. The prompt still goes through --append-system-prompt below:
	// that is the structured transport's carrier and the stronger channel, so it
	// is deliberately NOT skipped here the way project-instruction-only mode
	// skips --system-prompt-file on the tmux path.
	//
	// The file exists for everything else that reads project instructions —
	// nested claude invocations, sub-agents, tooling pointed at the workspace —
	// which previously saw nothing at all under structured transport, since this
	// adapter had no projection. It also makes the live prompt inspectable on
	// disk while a turn runs, which is otherwise impossible to check here.
	//
	// Structured turns are one-shot, so the file is written per turn and cleaned
	// up by the deferred removeFiles above.
	if writeProjectInstructionFromOptions(opts) && workingDir != "" && strings.TrimSpace(systemPrompt) != "" {
		if rulePath, err := writeClaudeCodeProjectInstructionFile(workingDir, systemPrompt, restoreProjectFilesFromOptions(opts)); err != nil {
			// Best-effort: --append-system-prompt already carries the prompt, so a
			// projection failure must never fail the turn.
			_ = err
		} else if rulePath != "" {
			tempFiles = append(tempFiles, rulePath)
		}
	}

	// Mint a fresh session id only when NOT resuming (avoid burning an id on a
	// resume turn). argv SHAPE + which id the turn runs under are owned by the
	// unit-tested builder — see buildClaudeStructuredArgs / TestBuildClaudeStructuredArgs.
	resumeSessionID := strings.TrimSpace(claudeResumeSessionIDFromStructuredOptions(opts))
	freshSessionID := ""
	if resumeSessionID == "" {
		freshSessionID = newClaudeNativeSessionID()
	}
	args, sessionID := buildClaudeStructuredArgs(c.modelID, systemPrompt, tools, allowedTools, permissionMode, mcpConfigPath, resumeSessionID, freshSessionID, workingDir)

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
	cmd.Env = llmtypes.MergeCodingAgentSecretEnvironment(os.Environ(), opts)
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
	var totalCacheReadTokens, totalCacheWriteTokens int
	var fallbackCacheReadTokens, fallbackCacheWriteTokens int
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
		name      string
		args      string
		startedAt time.Time
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
									name      string
									args      string
									startedAt time.Time
								}{name: block.Name, args: string(block.Input), startedAt: time.Now()}
							}
							// Carry the arguments on the START chunk. They are already
							// known here (block.Input), and the ToolCallStart event is
							// the only one with a field for them — ToolCallEnd has no
							// arguments field, so attaching them to the end chunk
							// silently drops them and the UI renders a permanently
							// empty "Arguments: (no arguments)" section.
							emitChunk(llmtypes.StreamChunk{
								Type:       llmtypes.StreamChunkTypeToolCallStart,
								ToolName:   block.Name,
								ToolCallID: block.ID,
								ToolArgs:   string(block.Input),
							})
						}
					}
					if ev.Message.Usage != nil {
						accumulateClaudeUsage(&assistantUsageFallback, ev.Message.Usage)
						fallbackCacheReadTokens += ev.Message.Usage.CacheReadInputTokens
						fallbackCacheWriteTokens += ev.Message.Usage.CacheCreationInputTokens
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
						// StreamChunk.ToolDuration existed but was never set here, so
						// every consumer downstream saw zero: ToolCallEndEvent.Duration
						// was zero, ToolCallEntry.Duration was zero, and the persisted
						// timing summary reported total_duration_ms=0 across successful
						// calls. That made a turn look purely generation-bound when part
						// of its wall clock was genuinely tool time.
						emitChunk(llmtypes.StreamChunk{
							Type:         llmtypes.StreamChunkTypeToolCallEnd,
							ToolCallID:   block.ToolUseID,
							ToolName:     pending.name,
							ToolArgs:     pending.args,
							ToolResult:   result,
							ToolDuration: toolclock.Since(pending.startedAt),
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
					totalCacheReadTokens += ev.Usage.CacheReadInputTokens
					totalCacheWriteTokens += ev.Usage.CacheCreationInputTokens
				}
				// Claude emits the result before its resumable transcript is
				// guaranteed to be flushed. Let it exit naturally first; the
				// signal sequence remains a backstop for a stuck CLI.
				go procshutdown.GracefulAfterNaturalExit(cmd, scannerDone, claudeStructuredNaturalExitGrace, c.logger)
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
		totalCacheReadTokens = fallbackCacheReadTokens
		totalCacheWriteTokens = fallbackCacheWriteTokens
	}

	content := strings.TrimSpace(resultText)
	if content == "" {
		content = strings.TrimSpace(finalText.String())
	}
	if content == "" {
		return nil, fmt.Errorf("claude run returned no text output; stderr: %s", strings.TrimSpace(stderr.String()))
	}

	additional := map[string]any{
		"provider":                   "claude-code",
		"claude_code_mode":           "structured",
		"claude_code_session_id":     sessionID, // surfaced so mcpagent captures a.ClaudeCodeSessionID and can --resume next turn
		"context_window_usage_known": false,
	}
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtrIfNonZeroClaude(totalUsage.InputTokens),
		OutputTokens: intPtrIfNonZeroClaude(totalUsage.OutputTokens),
		TotalTokens:  intPtrIfNonZeroClaude(totalUsage.InputTokens + totalUsage.OutputTokens),
		Additional:   additional,
	}
	if totalCacheReadTokens > 0 {
		v := totalCacheReadTokens
		genInfo.CachedContentTokens = &v
		additional["cache_read_input_tokens"] = v
	}
	if totalCacheWriteTokens > 0 {
		additional["cache_creation_input_tokens"] = totalCacheWriteTokens
	}
	if totalCacheReadTokens > 0 || totalCacheWriteTokens > 0 {
		additional["prompt_tokens_include_cache"] = true
	}
	// Record the cwd this conversation was actually created in, exactly as the
	// tmux adapter does. Claude keys a resumable conversation by working
	// directory (~/.claude/projects/<slugified-cwd>/), so a handle without it
	// forces the resume path to guess — and when the caller isolates the
	// session in its own workspace, guessing meant resuming against the user's
	// real workflow dir and failing with "No conversation found with session
	// ID", silently restarting the turn as a fresh conversation.
	llmtypes.AttachCodingProviderSessionHandle(genInfo, llmtypes.CodingProviderSessionHandle{
		Provider:        "claude-code",
		Transport:       llmtypes.CodingProviderTransportStructured,
		NativeSessionID: sessionID,
		WorkingDir:      workingDir,
		Model:           c.modelID,
		Status:          llmtypes.CodingProviderSessionStatusIdle,
	})

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{Content: content, StopReason: "stop", GenerationInfo: genInfo},
		},
		Usage: &totalUsage,
	}, nil
}

// accumulateClaudeUsage folds one stream usage event into the running total.
//
// Anthropic reports input_tokens EXCLUSIVE of cache read/write tokens, while
// this adapter's normalized prompt count is the complete input footprint.
// Fold both cache buckets into InputTokens and CacheTokens; GenerationInfo
// preserves their separate raw values so pricing can apply the correct read
// and write rates exactly once.
func accumulateClaudeUsage(dst *llmtypes.Usage, src *claudeStreamUsage) {
	if src == nil {
		return
	}
	dst.InputTokens += src.InputTokens + src.CacheReadInputTokens + src.CacheCreationInputTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens = dst.InputTokens + dst.OutputTokens
	if cacheTokens := src.CacheReadInputTokens + src.CacheCreationInputTokens; cacheTokens > 0 {
		cur := 0
		if dst.CacheTokens != nil {
			cur = *dst.CacheTokens
		}
		cur += cacheTokens
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
