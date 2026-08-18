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
	// NOT `cmd.Stderr = &bytes.Buffer{}`. That form makes os/exec create its
	// OWN internal pipe that we never see or control, and -- critically --
	// cmd.Wait() blocks until THAT pipe's internal copy goroutine also
	// reaches EOF, not just until the process exits. Live incident,
	// 2026-08-18, ICICI-BANK-PARSING check-form-26as-xspaces: pi's own
	// process was confirmed gone (no PID, no zombie) and stdout had already
	// closed cleanly (<-scannerDone had already unblocked), yet cmd.Wait()
	// itself stayed blocked for 51 minutes. A goroutine dump caught the exact
	// cause: os/exec's internal stderr-copy goroutine
	// (writerDescriptor.func1, an io.Copy off a pipe WE never had a handle
	// to) still parked in a read() syscall. This is stdout's own
	// "cmd.Wait() hangs on a pipe held open past the process's own exit"
	// failure, applying to stderr instead -- a symmetric failure mode nothing
	// here had ever considered, because stderr never went through
	// cmd.StdoutPipe()-shaped code that anyone reasoned about.
	//
	// Using cmd.StderrPipe() instead makes us the owner of that pipe, exactly
	// like stdout: os/exec auto-closes our read end the instant the tracked
	// process itself exits (proven empirically for stdout; the mechanism is
	// identical for stderr), regardless of what else may still hold the
	// write end. Read into our own buffer instead of accepting whatever text
	// showed up before the drain-or-force-close below settles.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pi stderr pipe: %w", err)
	}
	var stderr strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		buf := make([]byte, 4096)
		for {
			n, rErr := stderrPipe.Read(buf)
			if n > 0 {
				stderr.Write(buf[:n])
			}
			if rErr != nil {
				return
			}
		}
	}()

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
			default:
				// Every other type (agent_start, turn_start, message_start,
				// message_end, agent_end, session, agent_settled, and
				// anything a newer pi version adds) is intentionally ignored
				// for content -- but silently, so an unexpected/renamed event
				// that actually carried the answer or the failure reason left
				// no trace at all. Debug-log the type so that trace exists
				// without changing behavior for the types already handled
				// above.
				//
				// None of these types drive process teardown -- completion is
				// determined by process exit alone (below), never by parsing
				// a specific event. agent_end was tried as a terminal-event
				// trigger and reverted: verified live it fires per model
				// turn/response, not once per run (up to 3x in a single
				// group's run), so treating it as terminal SIGTERM'd a
				// process that was still doing real work. agent_settled DOES
				// reliably fire as the true once-per-run terminal event
				// (verified live, 2026-08-18, real key + real MCP server: it
				// is the last event of every run, and pi exits on its own
				// within ~5s) -- an earlier version of this comment claimed
				// otherwise based on a logging artifact (agent_settled was
				// silently handled and so never logged; only the unhandled
				// agent_end was). Not used as a trigger regardless: a correct
				// event still cannot substitute for process exit as the
				// authority, since a hang can happen after the terminal
				// event fires (see below) or on a run where a bounded
				// backstop has to fire, and building a switch statement to
				// distinguish those cases is more fragile than not needing
				// to.
				if p.logger != nil {
					p.logger.Debugf("pi: unhandled event type=%q", event.Type)
				}
			}
		}
	}()

	// Completion is driven by the process itself exiting, never by a parsed
	// event -- pi has no event that reliably means "the run is over" (see the
	// comment above). Blocking on <-scannerDone first, as this used to, waits
	// for stdout's write end to see EOF from every holder; pi can spawn a
	// persistent child (e.g. its MCP bridge) that inherits the fd and keeps
	// it open long after pi's own turn is done, so EOF never arrives and the
	// call hangs forever with no timeout -- this is what caused a Pulse step
	// to hold its caller's HTTP response open for 65 minutes after its real
	// work had already finished.
	//
	// Running cmd.Wait() concurrently instead sidesteps this: os/exec closes
	// the StdoutPipe read end itself the moment the tracked process (not its
	// descendants) exits, which unblocks the scanner even if a lingering
	// child still holds its own dup of the write end. Verified empirically
	// (10 runs, with and without a lingering grandchild holding the pipe,
	// success and non-zero exit): already-written output is preserved intact
	// and cmd.Wait() returns in single-digit milliseconds once pi itself
	// exits, regardless of any child.
	//
	// cmd.Wait() itself is not guaranteed bounded -- see the StderrPipe
	// comment above for the live incident where it hung for 51 minutes with
	// pi's own process already gone. Rather than guess whether the next hang
	// is that same shape, an unrelated one, or pi genuinely still running
	// (this workflow's tools are allowed up to 90 minutes, so an aggressive
	// timeout here would kill legitimate work), this only makes an abnormal
	// wait IMPOSSIBLE TO MISS: a clear, periodic error-level log instead of
	// requiring a goroutine dump to even notice it's happening.
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var waitErr error
	waitStart := time.Now()
	stallTicker := time.NewTicker(30 * time.Second)
	defer stallTicker.Stop()
waitLoop:
	for {
		select {
		case waitErr = <-waitDone:
			break waitLoop
		case <-stallTicker.C:
			if p.logger != nil {
				p.logger.Errorf("pi: cmd.Wait() has not returned after %s -- pi's own process may still be running, or something (e.g. a lingering child) is still holding stdout/stderr open past its exit; see picli_structured_adapter.go's stderr-pipe comment for the 2026-08-18 incident this class of hang matches",
					time.Since(waitStart).Round(time.Second))
			}
		}
	}

	select {
	case <-scannerDone:
	case <-time.After(5 * time.Second):
		// Should be unreachable given the empirical behavior above. If it
		// ever fires, os/exec's close-on-Wait somehow didn't unblock the
		// reader -- force it rather than hang, since pi's own process is
		// already confirmed exited at this point.
		if p.logger != nil {
			p.logger.Errorf("pi: stdout scanner did not drain within 5s of process exit -- forcing stdout closed")
		}
		stdout.Close()
		<-scannerDone
	}
	select {
	case <-stderrDone:
	case <-time.After(5 * time.Second):
		if p.logger != nil {
			p.logger.Errorf("pi: stderr reader did not drain within 5s of process exit -- forcing stderr closed")
		}
		stderrPipe.Close()
		<-stderrDone
	}

	content := strings.TrimSpace(finalContent)

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
