package picli

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
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
)

// piJSONEvent is one JSONL line from `pi --print --mode json`. Verified live
// against the actually-installed pi 0.80.10. Real shape: session -> agent_start
// -> turn_start -> message_start/message_update/message_end (assistant text
// deltas AND tool calls both flow through message_update.assistantMessageEvent)
// -> tool_execution_start/update/end (the actual tool run, separate from the
// model's toolCall content block) -> turn_end -> ... -> agent_end -> agent_settled.
type piJSONEvent struct {
	Type                string            `json:"type"`
	AssistantMessageEvt *piAssistantEvent `json:"assistantMessageEvent,omitempty"`
	ToolCallID          string            `json:"toolCallId,omitempty"`
	ToolName            string            `json:"toolName,omitempty"`
	Message             *piJSONMessage    `json:"message,omitempty"`
}

type piAssistantEvent struct {
	Type    string `json:"type"` // "text_start" | "text_delta" | "text_end" | "toolcall_start" | "toolcall_delta" | "toolcall_end"
	Delta   string `json:"delta,omitempty"`
	Content string `json:"content,omitempty"`
}

type piJSONMessage struct {
	Role  string       `json:"role,omitempty"`
	Usage *piJSONUsage `json:"usage,omitempty"`
}

type piJSONUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

// generateContentStructured drives `pi --print --mode json` — per-turn,
// one-shot, no tmux dependency. See MetadataKeyStructuredTransport doc comment
// for when to use this instead of the tmux interactive transport (tmux stays
// default).
func (p *PiCLIAdapter) generateContentStructured(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	// Structured contract §7: close the stream channel after process exit or
	// error — every return path here runs before the event goroutine starts or
	// after <-scannerDone, so this is safe exactly once on any return.
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

	binPath, err := exec.LookPath("pi")
	if err != nil {
		return nil, fmt.Errorf("pi not found in PATH: %w", err)
	}

	prompt := buildPiStructuredPrompt(messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("pi-cli prompt is empty")
	}

	workingDir := piWorkingDirFromOptions(opts)

	var configCleanups []func()
	defer func() {
		for _, fn := range configCleanups {
			fn()
		}
	}()
	if workingDir != "" {
		if mcpJSON := piMCPConfigFromOptions(opts); mcpJSON != "" {
			// Pi expects the SAME {"mcpServers": {...}} wrapper Cursor uses (NOT
			// Codex's flat map) — confirmed by reading normalizePiMCPConfig
			// directly rather than assuming, after getting this exact mismatch
			// wrong once already tonight for Codex.
			normalized, nErr := normalizePiMCPConfig(mcpJSON)
			if nErr != nil {
				return nil, nErr
			}
			mcpPath := filepath.Join(workingDir, ".pi", "mcp.json")
			cleanup, wErr := writePiRestoredFile(mcpPath, normalized)
			if wErr != nil {
				return nil, wErr
			}
			configCleanups = append(configCleanups, cleanup)
		}
	}

	mcpConfigSet := strings.TrimSpace(piMCPConfigFromOptions(opts)) != ""

	// Session id: on a resume turn the caller supplies the prior turn's id (via
	// MetadataKeyResumeSessionID); on a fresh turn we mint one so it can be
	// surfaced (pi_session_id below) and resumed next turn. The full session-
	// continuity / containment rationale now lives on buildPiStructuredArgs.
	sessionID := strings.TrimSpace(piResumeSessionIDFromOptions(opts))
	if sessionID == "" {
		sessionID = generatePiNativeSessionID()
	}
	// Skill projection is a disk side-effect; do it first, then hand the
	// resolved dir to the (unit-tested) argv builder.
	skillDir := ""
	if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 && workingDir != "" {
		// Was completely unwired until now — the tmux path projects skills to
		// disk and passes --skill <dir>; structured mode silently dropped them
		// entirely. Mirrors picli_interactive_adapter.go exactly.
		if err := p.ProjectSkills(workingDir, skills); err != nil {
			return nil, fmt.Errorf("pi project skills: %w", err)
		}
		skillDir = piProjectedSkillsPath(workingDir)
	}
	args := buildPiStructuredArgs(sessionID, piBridgeOnlyToolsFromOptions(opts), mcpConfigSet, piMCPExtensionFromOptions(opts), workingDir != "", skillDir)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pi stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if p.logger != nil {
		p.logger.Infof("Executing Pi CLI structured: pi --print --mode json")
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pi start: %w", err)
	}

	var finalContent string
	var turnTextBuf strings.Builder
	var totalUsage llmtypes.Usage
	sawTerminal := false

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	scannerDone := make(chan struct{})
	go func() {
		defer close(scannerDone)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var event piJSONEvent
			if err := json.Unmarshal(line, &event); err != nil {
				if p.logger != nil {
					p.logger.Debugf("pi: failed to parse event: %v", err)
				}
				continue
			}

			switch event.Type {
			case "message_update":
				if event.AssistantMessageEvt == nil {
					continue
				}
				switch event.AssistantMessageEvt.Type {
				case "text_delta":
					if d := event.AssistantMessageEvt.Delta; d != "" {
						turnTextBuf.WriteString(d) // verbatim — never split/rejoin a token
						emitChunk(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: d})
					}
				}
				if event.Message != nil && event.Message.Usage != nil {
					// pi reports a running total per response step, not a delta —
					// last-seen-wins rather than summing avoids massive overcount.
					u := event.Message.Usage
					totalUsage.InputTokens = u.Input
					totalUsage.OutputTokens = u.Output
					totalUsage.TotalTokens = u.Input + u.Output
					if u.CacheRead > 0 {
						cacheRead := u.CacheRead
						totalUsage.CacheTokens = &cacheRead
					}
				}
			case "tool_execution_start":
				emitChunk(llmtypes.StreamChunk{
					Type:       llmtypes.StreamChunkTypeToolCallStart,
					Content:    event.ToolName,
					ToolCallID: event.ToolCallID,
				})
			case "tool_execution_end":
				emitChunk(llmtypes.StreamChunk{
					Type:       llmtypes.StreamChunkTypeToolCallEnd,
					Content:    event.ToolCallID,
					ToolCallID: event.ToolCallID,
				})
			case "turn_end":
				// The LAST turn_end's accumulated text is the real final answer —
				// a tool-use turn's turn_end has empty/no final text (the follow-up
				// turn after the tool result carries it), so later turns correctly
				// overwrite earlier ones rather than needing special-casing.
				if s := strings.TrimSpace(turnTextBuf.String()); s != "" {
					finalContent = s
				}
				turnTextBuf.Reset()
			case "agent_settled":
				sawTerminal = true
				go procshutdown.Graceful(cmd, scannerDone, p.logger)
			}
		}
	}()
	<-scannerDone
	_ = sawTerminal

	waitErr := cmd.Wait()
	content := strings.TrimSpace(finalContent)

	if waitErr != nil && content == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("pi run failed: %w: %s", waitErr, stderrStr)
		}
		return nil, fmt.Errorf("pi run failed: %w", waitErr)
	}
	if content == "" {
		return nil, fmt.Errorf("pi run returned no text output")
	}

	additional := map[string]any{
		"provider":      "pi-cli",
		"pi_mode":       "structured",
		"pi_session_id": sessionID, // surfaced so mcpagent captures a.PiSessionID and can --session-id resume next turn
	}
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtrIfNonZeroPi(totalUsage.InputTokens),
		OutputTokens: intPtrIfNonZeroPi(totalUsage.OutputTokens),
		TotalTokens:  intPtrIfNonZeroPi(totalUsage.TotalTokens),
		Additional:   additional,
	}
	if totalUsage.CacheTokens != nil && *totalUsage.CacheTokens > 0 {
		v := *totalUsage.CacheTokens
		genInfo.CachedContentTokens = &v
		additional["cache_read_input_tokens"] = v
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

// buildPiStructuredPrompt concatenates the FULL non-system history — a
// structured call is a fresh process each time with no persistent session to
// hold prior turns.
func buildPiStructuredPrompt(messages []llmtypes.MessageContent) string {
	var b strings.Builder
	for _, m := range messages {
		text := extractTextFromPiMessage(m)
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch m.Role {
		case llmtypes.ChatMessageTypeHuman:
			b.WriteString("User: ")
		case llmtypes.ChatMessageTypeAI:
			b.WriteString("Assistant: ")
		case llmtypes.ChatMessageTypeSystem:
			b.WriteString("System: ")
		}
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func extractTextFromPiMessage(m llmtypes.MessageContent) string {
	var parts []string
	for _, part := range m.Parts {
		if tc, ok := part.(llmtypes.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func intPtrIfNonZeroPi(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}
