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
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/toolclock"
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
	Args                json.RawMessage   `json:"args,omitempty"`
	Result              json.RawMessage   `json:"result,omitempty"`
	IsError             bool              `json:"isError,omitempty"`
	Message             *piJSONMessage    `json:"message,omitempty"`
}

type piStructuredToolCall struct {
	Name string
	Args string
}

func compactPiStructuredJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		return compact.String()
	}
	return strings.TrimSpace(string(raw))
}

func piStructuredToolStartChunk(event piJSONEvent) (llmtypes.StreamChunk, piStructuredToolCall) {
	call := piStructuredToolCall{
		Name: strings.TrimSpace(event.ToolName),
		Args: compactPiStructuredJSON(event.Args),
	}
	return llmtypes.StreamChunk{
		Type:       llmtypes.StreamChunkTypeToolCallStart,
		Content:    call.Name,
		ToolName:   call.Name,
		ToolCallID: event.ToolCallID,
		ToolArgs:   call.Args,
	}, call
}

func piStructuredToolEndChunk(event piJSONEvent, call piStructuredToolCall, duration time.Duration) llmtypes.StreamChunk {
	name := strings.TrimSpace(event.ToolName)
	if name == "" {
		name = call.Name
	}
	return llmtypes.StreamChunk{
		Type:         llmtypes.StreamChunkTypeToolCallEnd,
		Content:      event.ToolCallID,
		ToolName:     name,
		ToolCallID:   event.ToolCallID,
		ToolArgs:     call.Args,
		ToolResult:   compactPiStructuredJSON(event.Result),
		ToolDuration: duration,
	}
}

type piAssistantEvent struct {
	Type    string `json:"type"` // "text_start" | "text_delta" | "text_end" | "toolcall_start" | "toolcall_delta" | "toolcall_end"
	Delta   string `json:"delta,omitempty"`
	Content string `json:"content,omitempty"`
}

// piApplyAssistantEvent updates the accumulated turn-text buffer for one
// assistantMessageEvent and returns any delta text to stream immediately.
//
// pi delimits each logical assistant text segment with its own
// text_start/text_end pair, verified live against pi 0.80.10 — including a
// captured turn with TWO separate text_start/text_end blocks (narration
// before a tool call, then narration after it, both inside one
// turn_start/turn_end pair). Before this, only text_delta was handled and the
// buffer was reset solely at turn_end, so two genuinely separate assistant
// messages within one turn would be concatenated with no separator at all —
// e.g. "...about to read b.txt.The file b.txt contained...". A fresh
// text_start now inserts a paragraph break when the buffer already holds a
// prior segment; the very first segment of a turn (empty buffer) gets no
// spurious separator.
func piApplyAssistantEvent(buf *strings.Builder, evt *piAssistantEvent) (streamDelta string) {
	if evt == nil {
		return ""
	}
	switch evt.Type {
	case "text_start":
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
	case "text_delta":
		if evt.Delta != "" {
			buf.WriteString(evt.Delta) // verbatim — never split/rejoin a token
			return evt.Delta
		}
	}
	return ""
}

type piJSONMessage struct {
	Role  string       `json:"role,omitempty"`
	Usage *piJSONUsage `json:"usage,omitempty"`
}

type piJSONUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	Reasoning  int `json:"reasoning"`
	Total      int `json:"totalTokens"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

func applyPiJSONUsage(message *piJSONMessage, totalUsage *llmtypes.Usage, cacheWriteTokens *int) {
	if message == nil || message.Usage == nil || totalUsage == nil || cacheWriteTokens == nil {
		return
	}
	u := message.Usage
	totalUsage.InputTokens = u.Input
	totalUsage.OutputTokens = u.Output
	totalUsage.TotalTokens = u.Total
	if totalUsage.TotalTokens == 0 {
		totalUsage.TotalTokens = u.Input + u.Output
	}
	if u.Reasoning > 0 {
		reasoning := u.Reasoning
		totalUsage.ReasoningTokens = &reasoning
	}
	if u.CacheRead > 0 {
		cacheRead := u.CacheRead
		totalUsage.CacheTokens = &cacheRead
	}
	*cacheWriteTokens = u.CacheWrite
}

// resolveStructuredProviderModel picks the provider/model a structured turn
// will actually run, honoring the caller's provider override exactly as the
// interactive path does.
//
// Extracted so a test can assert THIS decision rather than re-deriving it from
// the same helpers the adapter uses -- a test that calls
// resolvePiProviderModel itself passes whether or not the adapter bothers to
// consult the override, which is how the bug survived review in the first
// place.
func (p *PiCLIAdapter) resolveStructuredProviderModel(opts *llmtypes.CallOptions) (string, string) {
	// Hardcoding "" here ignored the override, starting the wrong provider AND
	// -- because this same value selects the API-key variable names --
	// injecting the wrong credentials for whatever ran.
	return resolvePiProviderModel(p.GetModelID(), piProviderFromOptions(opts))
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
	// Resolve once: the same provider/model pair drives BOTH the argv flags
	// (which model pi actually runs) and the API key env var names (which
	// credential that provider expects). Deriving them separately is how they
	// drift apart.
	provider, model := p.resolveStructuredProviderModel(opts)
	args := buildPiStructuredArgs(provider, model, sessionID, piBridgeOnlyToolsFromOptions(opts), mcpConfigSet, piMCPExtensionFromOptions(opts), workingDir != "", skillDir)
	if p.logger != nil {
		p.logger.Infof("Pi CLI structured: running provider=%s model=%s session=%s", provider, model, sessionID)
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = llmtypes.MergeCodingAgentSecretEnvironment(os.Environ(), opts)
	// p.apiKey is the resolved key initializePiCLI already picked (workspace-
	// scoped, then shared, then local pi auth) -- but until now nothing here
	// ever turned it into an env var, so the subprocess only ever saw whatever
	// happened to already be in the backend's own ambient environment. Strip
	// any ambient value for the same key names first so a stale/wrong ambient
	// key can never silently win over the one that was actually resolved.
	if p.apiKey != "" {
		keyEnv := piAPIKeyEnv(provider, p.apiKey)
		cmd.Env = piOverrideEnv(cmd.Env, keyEnv)
		if p.logger != nil {
			p.logger.Infof("Pi CLI structured: injected %d API key env var(s) for provider %s", len(keyEnv), provider)
		}
	} else if p.logger != nil {
		p.logger.Infof("Pi CLI structured: no resolved API key -- relying on ambient environment / local pi auth")
	}
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
	var cacheWriteTokens int
	sawTerminal := false

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	// StreamChunk.ToolDuration existed but was never set by any
	// structured adapter, so the whole chain downstream reported zero:
	// ToolCallEndEvent.Duration, ToolCallEntry.Duration, and the persisted
	// timing summary's total_duration_ms. A turn then looked entirely
	// generation-bound even when real tool time was part of its wall clock.
	toolStartedAt := map[string]time.Time{}
	toolCalls := map[string]piStructuredToolCall{}
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
			// Pi 0.84 reports final non-zero usage on message_end/turn_end.
			// Earlier versions also exposed it on message_update. Accept every
			// message-bearing event with last-seen-wins semantics so both shapes
			// remain valid and a legitimate final usage record is never dropped.
			applyPiJSONUsage(event.Message, &totalUsage, &cacheWriteTokens)

			// IsError is a structured signal pi already sends, but nothing read
			// it: a real failure (bad key, quota, unknown model) could arrive as
			// exactly this and still leave finalContent empty with zero trace of
			// why -- "pi run returned no text output" without even the type of
			// the one event that actually explained it.
			//
			// Only the TYPE and the tool name are logged. The raw event body is
			// deliberately not, because an error event carries the failing
			// tool's output, which can contain file contents or a credential
			// the tool was handed. The type is what makes the failure
			// traceable; the payload is what makes it a disclosure.
			if event.IsError && p.logger != nil {
				p.logger.Errorf("pi: received error event type=%q tool=%q", event.Type, event.ToolName)
			}

			switch event.Type {
			case "message_update":
				if event.AssistantMessageEvt == nil {
					continue
				}
				if d := piApplyAssistantEvent(&turnTextBuf, event.AssistantMessageEvt); d != "" {
					emitChunk(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: d})
				}
			case "tool_execution_start":
				toolStartedAt[event.ToolCallID] = time.Now()
				chunk, call := piStructuredToolStartChunk(event)
				toolCalls[event.ToolCallID] = call
				emitChunk(chunk)
			case "tool_execution_end":
				emitChunk(piStructuredToolEndChunk(
					event,
					toolCalls[event.ToolCallID],
					toolclock.Elapsed(toolStartedAt, event.ToolCallID),
				))
				delete(toolCalls, event.ToolCallID)
			case "turn_end":
				// The LAST turn_end's accumulated text is the real final answer —
				// a tool-use turn's turn_end has empty/no final text (the follow-up
				// turn after the tool result carries it), so later turns correctly
				// overwrite earlier ones rather than needing special-casing.
				if s := strings.TrimSpace(turnTextBuf.String()); s != "" {
					finalContent = s
				}
				turnTextBuf.Reset()
			// Both are accepted, and that redundancy is the point. This adapter
			// was verified against pi 0.80.10, whose stream ended
			// `agent_end -> agent_settled`, and it took `agent_settled` as the
			// sole teardown trigger. pi 0.84.2 does not emit `agent_settled` at
			// all: measured across a full day of production logs it appeared 0
			// times, while `agent_end` appeared 57. The only trigger was
			// therefore dead code against the installed pi, and any run where pi
			// did not exit on its own -- notably a continued native session,
			// which stays alive between turns -- blocked forever on
			// <-scannerDone, with no timeout anywhere in this path.
			//
			// Live incident (2026-08-18, ICICI-BANK-PARSING-v2 group manishiitg):
			// the step finished its real work at 09:45 and emitted agent_end,
			// then held its caller for 65 minutes until the stack was stopped by
			// hand. Same shape as the codex hang its sibling adapter already
			// fixed -- see codexcli_structured_adapter.go's teardown comment.
			//
			// agent_end is pi's genuine completion signal; the interactive
			// adapter has always treated it as authoritative. Keeping
			// agent_settled means an older pi still terminates on the event it
			// does emit, so this tolerates drift in both directions instead of
			// trading one hard version dependency for another.
			case "agent_end", "agent_settled":
				sawTerminal = true
				go procshutdown.GracefulAfterNaturalExit(cmd, scannerDone, 3*time.Second, p.logger)
			default:
				// Every other type (agent_start, turn_start, message_start,
				// message_end, session, and anything a newer pi
				// version adds) is intentionally ignored for content -- but
				// silently, so an unexpected/renamed event that actually
				// carried the answer or the failure reason left no trace at
				// all. Debug-log the type so that trace exists without
				// changing behavior for the types already handled above.
				if p.logger != nil {
					p.logger.Debugf("pi: unhandled event type=%q", event.Type)
				}
			}
		}
	}()
	<-scannerDone

	waitErr := cmd.Wait()
	content := strings.TrimSpace(finalContent)

	// sawTerminal used to be `_ = sawTerminal` -- computed and thrown away, so
	// "pi told us it finished" and "pi's stdout happened to close" were
	// indistinguishable here. They are not the same thing: stdout closing
	// without a terminal event is how a killed, crashed, or truncated run
	// looks, and reporting that as a clean answer is what makes a partial
	// result silently pass for a complete one. Content still wins when we have
	// it -- this only adds a trace for the case that used to be invisible.
	if !sawTerminal && p.logger != nil {
		p.logger.Infof("pi: stdout closed without a terminal event (agent_end/agent_settled); "+
			"treating the run as finished, but the result may be truncated (content_len=%d, wait_err=%v)",
			len(content), waitErr)
	}

	if waitErr != nil && content == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("pi run failed: %w: %s", waitErr, stderrStr)
		}
		return nil, fmt.Errorf("pi run failed: %w", waitErr)
	}
	if content == "" {
		// pi can exit 0 with nothing on stdout when it rejected the run for a
		// reason it only wrote to stderr (bad key, quota, unknown model) --
		// that stderr was being captured and then silently discarded here,
		// which is exactly what turned "invalid Gemini key" into an opaque
		// "no text output" with nothing to trace it back to.
		if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
			return nil, fmt.Errorf("pi run returned no text output: %s", stderrStr)
		}
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
	if totalUsage.ReasoningTokens != nil && *totalUsage.ReasoningTokens > 0 {
		reasoning := *totalUsage.ReasoningTokens
		genInfo.ReasoningTokens = &reasoning
	}
	if totalUsage.CacheTokens != nil && *totalUsage.CacheTokens > 0 {
		v := *totalUsage.CacheTokens
		genInfo.CachedContentTokens = &v
		additional["cache_read_input_tokens"] = v
	}
	if cacheWriteTokens > 0 {
		additional["cache_creation_input_tokens"] = cacheWriteTokens
	}
	if (totalUsage.CacheTokens != nil && *totalUsage.CacheTokens > 0) || cacheWriteTokens > 0 {
		// Pi reports fresh input separately from its cache buckets.
		additional["prompt_tokens_include_cache"] = false
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

// piOverrideEnv returns base with any existing entry for a key present in
// overrides removed, then overrides appended. Filtering first rather than
// relying on "last assignment wins" keeps this correct regardless of
// platform-specific duplicate-env-var behavior in exec.
func piOverrideEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	blocked := make(map[string]bool, len(overrides))
	for _, entry := range overrides {
		key, _, _ := strings.Cut(entry, "=")
		blocked[key] = true
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if blocked[key] {
			continue
		}
		out = append(out, entry)
	}
	return append(out, overrides...)
}
