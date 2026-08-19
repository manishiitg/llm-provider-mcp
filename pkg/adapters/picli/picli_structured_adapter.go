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
	"sync/atomic"
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

// piStallPollInterval is how often the wait loop checks; it is NOT how often it
// logs. See piStallReporter.
const piStallPollInterval = 30 * time.Second

// piStallSilenceThreshold is how long pi's stream must be completely silent
// before an unfinished run is worth reporting. Duration alone means nothing --
// a step legitimately making many tool calls can run long -- only silence
// does. (An earlier version of this comment claimed "this workflow permits
// 90m tool executions"; that was never verified and was wrong -- see
// piMaxTurnDuration below for what actually bounds a turn, and why nothing
// did until it was added.)
const piStallSilenceThreshold = 5 * time.Minute

// piWedgeSilenceThreshold is how long pi's stream may be COMPLETELY silent,
// with no tool call in flight, before the turn is treated as wedged and killed.
//
// This is the fast path out of the failure PLAT-153 documents, and it exists
// because piMaxTurnDuration alone is too blunt to be the only recovery: a
// wedged turn burned the full 45m ceiling before anything reclaimed it, and a
// workflow with several such steps pays that repeatedly.
//
// It is safe to be this aggressive ONLY because of the outstanding-tool guard.
// A tool call may legitimately run up to
// codingtimeout.LongRunningMCPToolTimeout (90m default), during which pi emits
// nothing at all -- killing on silence alone would murder healthy long tool
// calls. With zero tools in flight, the only legitimate reason for silence is
// waiting on the model to start responding, and once it starts, pi streams
// chunks continuously.
//
// 10 minutes: comfortably above every no-tool-in-flight gap observed in this
// deployment (the slowest full model turns measured were 2m19s and 1m9s,
// streaming throughout), and far below the 26+ minutes of dead silence the
// real wedge produced. Configurable via PI_STRUCTURED_WEDGE_SILENCE because,
// like the ceiling, this is reasoned from a day of observation rather than a
// long baseline.
func piWedgeSilenceThreshold() time.Duration {
	if v := strings.TrimSpace(os.Getenv("PI_STRUCTURED_WEDGE_SILENCE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Minute
}

// piMaxTurnDuration is the hard ceiling on one structured pi CLI turn.
//
// Before this, nothing bounded it. TOOL_EXECUTION_TIMEOUT
// (agent_go/cmd/server/agent_tuning.go) only wraps each individual tool call
// pi makes through the bridge -- and in this deployment was not even set,
// leaving the actual per-tool default at 5 minutes, not the 90 minutes
// earlier assumed without checking. Neither that setting nor anything else
// placed a deadline on the ctx passed into generateContentStructured itself;
// a pi process that got internally wedged -- not blocked on any one tool
// call, just stopped making progress -- could run until the server itself
// restarted.
//
// This is pi's own documented failure mode, not speculation: pi-coding-agent
// upstream (github.com/earendil-works/pi#8004) reports two real sessions
// frozen 5.5 and 8.7 hours by exactly this shape (a spawned/nested process
// that completed its actual work but never exited, keeping Node's event loop
// alive with nothing for `pi --print` to wait on), and states plainly there is
// "no general tool-call timeout" and "no abort handle" once a call is running.
// Confirmed live 2026-08-19: a form-26as step's pi process sat with a
// completely idle Node event loop (main thread parked in uv__io_poll/kevent,
// every worker blocked on uv_cond_wait, no thread in any syscall) for over an
// hour, its last actual tool call having finished in 28ms with nothing after.
//
// 45 minutes: comfortably above every turn duration observed in this
// deployment today (healthy multi-tool-call turns finished in single-digit
// minutes), while still turning an unbounded, hours-long wedge into a bounded
// one. Configurable via PI_STRUCTURED_MAX_TURN_DURATION because 45m is a
// reasoned default, not a measured one -- no turn-duration history exists yet
// to derive it from.
func piMaxTurnDuration() time.Duration {
	if v := strings.TrimSpace(os.Getenv("PI_STRUCTURED_MAX_TURN_DURATION")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 45 * time.Minute
}

// piMaxStallReportInterval caps the doubling backoff, so a genuinely stuck run
// keeps a heartbeat without flooding.
const piMaxStallReportInterval = 16 * time.Minute

// piStallReporter decides WHEN an outstanding cmd.Wait() is worth a log line.
//
// Extracted from the wait loop so this decision is testable: the first version
// of this logging reported purely on elapsed run time and produced ~34
// error-level lines for a perfectly healthy 17-minute step (measured live
// 2026-08-19), which is the same alert-fatigue failure that made the
// TOOL_ERROR_SUSPECT channel unreadable at 929 lines. A predicate that is only
// exercised through a real subprocess is a predicate nobody checks.
type piStallReporter struct {
	lastReportedAt time.Time
	nextInterval   time.Duration
}

// shouldReport reports whether to emit a stall line now.
//
//   - terminal (pi said it finished but has not exited) is the deadlock: it
//     never resolves on its own, so report it on the very first check.
//   - otherwise, stay silent until pi's stream has been quiet for
//     piStallSilenceThreshold. Events still flowing means it is working.
//   - after any report, back off by doubling so a stuck run leaves a readable
//     trail rather than hundreds of identical lines.
func (r *piStallReporter) shouldReport(terminal bool, silence time.Duration, now time.Time) bool {
	if !terminal && silence < piStallSilenceThreshold {
		return false
	}
	if !r.lastReportedAt.IsZero() && now.Sub(r.lastReportedAt) < r.nextInterval {
		return false
	}
	r.lastReportedAt = now
	if r.nextInterval == 0 {
		r.nextInterval = piStallPollInterval
	} else if r.nextInterval < piMaxStallReportInterval {
		r.nextInterval *= 2
	}
	return true
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

	// Bound the whole turn, independent of whatever deadline (if any) the
	// caller's ctx already carries -- see piMaxTurnDuration's doc comment for
	// why nothing did before this and what it costs when nothing does.
	turnCtx, cancelTurn := context.WithTimeout(ctx, piMaxTurnDuration())
	defer cancelTurn()

	cmd := exec.CommandContext(turnCtx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// exec.CommandContext's DEFAULT cancellation is cmd.Process.Kill() -- the
	// single tracked pid only. Setpgid above exists so a killer CAN target the
	// whole group, but the default kill does not do that, so a grandchild pi
	// forks off (a shell tail-command that was not exec-replaced, an MCP
	// bridge, anything) survives as an orphan even though the tracked process
	// died. Measured directly while testing this: a fake pi script's own
	// recorded pid died correctly, and three separate test runs each still
	// leaked a live, reparented (ppid=1) grandchild.
	//
	// procshutdown.GracefulAfterNaturalExit already gets this right, via
	// syscall.Kill(-pid, ...) -- the negative pid form targets the whole
	// process group. cmd.Cancel overrides CommandContext's default with the
	// same primitive, so cancellation for ANY reason (this ceiling, or the
	// caller's own ctx) reaps the whole group, not just the leader.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
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
	// Both are set by the scanner goroutine and read by the wait loop's stall
	// logger, so they must be race-safe.
	var sawTerminal atomic.Bool
	// UnixNano of the last event pi emitted. This -- not total run time -- is
	// what says whether pi is stalled: a step that legitimately runs 20 minutes
	// while emitting tool events every few seconds is healthy, and the first
	// version of this logging called it a stall anyway, producing ~34
	// error-level lines per healthy run. That is the same alert-fatigue failure
	// that made the TOOL_ERROR_SUSPECT channel unreadable.
	var lastEventUnixNano atomic.Int64
	lastEventUnixNano.Store(time.Now().UnixNano())
	// How many tool calls pi currently has in flight, from its OWN events.
	// This is what separates "wedged" from "legitimately slow": a tool call may
	// run up to codingtimeout.LongRunningMCPToolTimeout (90m by default), and pi
	// is silent for that whole time by design. With nothing in flight, the only
	// legitimate reason for silence is waiting on the model, which is bounded in
	// practice by the provider's own timeouts.
	//
	// KNOWN LIMITATION, recorded rather than papered over: this trusts pi to
	// emit a tool_execution_end for every tool_execution_start. Ends really do
	// go missing on other providers -- agent_go's PLAT-141 settleOpenToolCalls
	// exists precisely because of it, and was observed closing 14 of 14
	// unreported tool calls in a live Codex chat session on 2026-08-19. If that
	// ever happens on the pi structured path, this counter never returns to
	// zero and the fast wedge path below silently stops firing. That degrades
	// to the piMaxTurnDuration ceiling -- slower, but still bounded, and never
	// a false kill -- which is the safe direction for this to fail in.
	var outstandingTools atomic.Int32
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
			// Any parsed event is proof of life, whatever its type -- including
			// ones this adapter ignores for content. Recorded before the switch
			// so the stall check measures silence on pi's stream rather than
			// how long the step has been running.
			lastEventUnixNano.Store(time.Now().UnixNano())
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
				outstandingTools.Add(1)
				toolStartedAt[event.ToolCallID] = time.Now()
				chunk, call := piStructuredToolStartChunk(event)
				toolCalls[event.ToolCallID] = call
				emitChunk(chunk)
			case "tool_execution_end":
				outstandingTools.Add(-1)
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
			case "agent_settled":
				// pi's true once-per-run terminal event, verified live against
				// the real CLI and in pi's own source: emitted from a `finally`
				// block in _runAgentPrompt, so it cannot be skipped on success,
				// error, or abort, and only AFTER the
				// `while (_handlePostAgentRun())` loop that drives multi-step
				// tool use and retries has fully drained. (agent_end fires
				// INSIDE that loop, many times per run, which is why treating
				// IT as terminal killed still-working processes and was
				// reverted.)
				//
				// Seeing it does not end the turn here -- the process exiting
				// does. It arms a bounded teardown, because pi finishing its
				// work does not guarantee pi EXITS: print mode returns without
				// process.exit() and relies on Node's event loop draining, and
				// pi's own source warns that extensions can keep a one-shot
				// command alive. The MCP extension spawns mcpbridge as a child,
				// whose live process handle keeps that loop from draining,
				// while mcpbridge itself sits waiting on a stdin pi never
				// closes -- a deadlock neither side breaks.
				//
				// Measured live 2026-08-19 with this teardown missing: two pi
				// processes idle for 28 and 65 minutes, each with a live
				// mcpbridge child, neither holding any network socket, both
				// with cmd.Wait() blocked in syscall.Wait4 because the child
				// genuinely was still alive. GracefulAfterNaturalExit gives pi
				// 3s to exit on its own first, so a normal run (which does exit
				// in ~5s) is never signalled.
				sawTerminal.Store(true)
				go procshutdown.GracefulAfterNaturalExit(cmd, scannerDone, 3*time.Second, p.logger)
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
	// cmd.Wait() itself is not guaranteed bounded. Rather than guess whether
	// the next hang is a pipe problem, an exit deadlock, or pi genuinely still
	// working (this workflow's tools are allowed up to 90 minutes, so an
	// aggressive timeout here would kill legitimate work), this does not kill
	// anything -- it only makes an abnormal wait visible without needing a
	// goroutine dump to notice it.
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var waitErr error
	waitStart := time.Now()
	// Poll frequently, but only LOG when something is actually wrong. The two
	// cases below have opposite urgency, and treating them the same is what
	// made the first version of this useless:
	//
	//   sawTerminal -> pi said it finished and has not exited. That is the
	//     deadlock, it never resolves on its own, and the teardown that should
	//     be killing it has evidently not worked. Report it immediately.
	//   otherwise   -> pi has not finished. Long is not the same as stuck: this
	//     workflow allows 90m tool executions, and a healthy step emits events
	//     throughout. Only genuine SILENCE on pi's stream is suspicious.
	//
	// The interval also doubles after each report (capped), so a genuinely
	// stuck run leaves a readable trail instead of hundreds of identical lines.
	stallTicker := time.NewTicker(piStallPollInterval)
	defer stallTicker.Stop()
	stall := piStallReporter{}
waitLoop:
	for {
		select {
		case waitErr = <-waitDone:
			break waitLoop
		case <-stallTicker.C:
			silence := time.Since(time.Unix(0, lastEventUnixNano.Load()))
			terminal := sawTerminal.Load()

			// Wedged: pi has gone completely silent with nothing in flight to
			// explain it. Cut it loose now rather than letting it burn the full
			// piMaxTurnDuration ceiling -- cancelTurn triggers cmd.Cancel's
			// group kill, cmd.Wait() returns, and the caller gets a failed turn
			// it can retry, which is what actually recovers the workflow.
			if !terminal && outstandingTools.Load() == 0 && silence >= piWedgeSilenceThreshold() {
				if p.logger != nil {
					pid := -1
					if cmd.Process != nil {
						pid = cmd.Process.Pid
					}
					p.logger.Errorf("pi: no event for %s with no tool call in flight (pid=%d) -- treating as wedged and killing it now rather than waiting out the %s ceiling; the turn fails and the caller can retry (PLAT-153)",
						silence.Round(time.Second), pid, piMaxTurnDuration())
				}
				cancelTurn()
				continue
			}

			if !stall.shouldReport(terminal, silence, time.Now()) {
				continue
			}
			if p.logger != nil {
				// Log the pid and whether pi's terminal event was seen, because
				// those two facts are what actually distinguish the cases -- and
				// the previous version of this message, which named both
				// possibilities without saying which, cost a live investigation
				// on 2026-08-19: it read "may still be running, or something is
				// holding stdout/stderr open", the pid was checked with a wrong
				// `ps` pattern (pi runs as COMM=pi, not as a node process), and
				// the wrong branch was concluded.
				//
				//   terminal_event_seen=true  -> pi finished its work and is not
				//     exiting. Its MCP child keeps Node's event loop alive while
				//     waiting on a stdin pi never closes. The agent_settled
				//     teardown should already be signalling it; if this line
				//     keeps printing, that teardown is not firing.
				//   terminal_event_seen=false -> pi is most likely still doing
				//     real work (a long tool call; this workflow allows 90m).
				//     Confirm with `ps -p <pid>` before assuming a hang.
				state := fmt.Sprintf("no event on pi's stream for %s -- pi has NOT reported finishing, so it may still be mid-tool-call (this workflow allows 90m); confirm with `ps -p %d`",
					silence.Round(time.Second), func() int {
						if cmd.Process != nil {
							return cmd.Process.Pid
						}
						return -1
					}())
				if terminal {
					state = "pi ALREADY emitted agent_settled -- its work is DONE and it is failing to EXIT. This does not resolve on its own: the agent_settled teardown should have signalled it, so that teardown is not working. Killing the pid unblocks the held run."
				}
				pid := -1
				if cmd.Process != nil {
					pid = cmd.Process.Pid
				}
				p.logger.Errorf("pi: cmd.Wait() has not returned after %s (pid=%d, terminal_event_seen=%t, stream_silent_for=%s): %s",
					time.Since(waitStart).Round(time.Second), pid, terminal, silence.Round(time.Second), state)
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
