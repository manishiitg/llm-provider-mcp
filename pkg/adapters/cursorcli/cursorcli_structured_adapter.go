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
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/toolclock"
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
	// Present on every streamed fragment (assistant + thinking deltas), absent
	// on the assembled per-span "assistant" repeat -- the only field that tells
	// the two apart since neither carries a subtype (cursor-agent 2026.09.02).
	TimestampMS *int64 `json:"timestamp_ms,omitempty"`

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

type cursorStructuredToolCall struct {
	Name string
	Args string
	// Result is only present on the "completed" event. Cursor sends the call's
	// arguments on "started" and its result on "completed", in separate
	// payloads — so a completed envelope generally carries a result and no args.
	Result string
}

// cursorStructuredToolCallDetails translates Cursor's stream-json tool_call
// envelope into the provider-neutral stream fields used by the event/UI layer.
// Cursor puts the actual call under a typed key (for example shellToolCall or
// mcpToolCall), rather than exposing a top-level name/arguments pair.
func cursorStructuredToolCallDetails(raw json.RawMessage) cursorStructuredToolCall {
	if len(raw) == 0 {
		return cursorStructuredToolCall{}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return cursorStructuredToolCall{}
	}

	for kind, payload := range envelope {
		if !strings.HasSuffix(kind, "ToolCall") {
			continue
		}
		var call map[string]json.RawMessage
		if err := json.Unmarshal(payload, &call); err != nil {
			continue
		}
		var args map[string]json.RawMessage
		if rawArgs, ok := call["args"]; ok {
			_ = json.Unmarshal(rawArgs, &args)
		}

		name := strings.TrimSuffix(kind, "ToolCall")
		server := cursorStructuredToolString(args, "server", "serverName", "namespace", "providerIdentifier", "serverIdentifier")
		tool := cursorStructuredToolString(args, "toolName", "tool_name", "name")
		isMCPCall := kind == "mcpToolCall" || (server != "" && tool != "")
		if isMCPCall {
			// MCP calls carry the target in their argument envelope. Preserve the
			// standard mcp__server__tool spelling so existing UI renderers show
			// the actual bridge tool, not Cursor's generic CallMcpTool wrapper.
			if server != "" && tool != "" {
				name = "mcp__" + server + "__" + tool
			} else if tool != "" {
				name = tool
			} else {
				name = "CallMcpTool"
			}
		}
		if name == "" {
			continue
		}

		// For MCP calls the useful arguments are the target-tool arguments, not
		// Cursor's wrapper fields (server/toolName/description). For native
		// Cursor tools, preserve the complete args object.
		argValue := json.RawMessage(nil)
		if isMCPCall && args != nil {
			argValue = firstCursorStructuredToolRaw(args, "arguments", "input", "args")
		}
		if len(argValue) == 0 && args != nil {
			argValue, _ = json.Marshal(args)
		}
		return cursorStructuredToolCall{
			Name:   name,
			Args:   compactCursorStructuredJSON(argValue),
			Result: cursorStructuredToolResult(call["result"]),
		}
	}
	return cursorStructuredToolCall{}
}

// cursorStructuredToolResult renders a completed tool call's result for the
// ToolResult stream field the UI shows under "Developer details". Cursor wraps
// it differently per tool ({"content":"..."} for MCP calls, tool-shaped objects
// for native ones), so unwrap the common single-string shapes and fall back to
// compact JSON rather than guessing at every tool's schema.
func cursorStructuredToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return strings.TrimSpace(direct)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		if unwrapped := cursorStructuredToolString(object, "content", "output", "text", "stdout", "result"); unwrapped != "" {
			return unwrapped
		}
	}
	return compactCursorStructuredJSON(raw)
}

func cursorStructuredToolString(values map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstCursorStructuredToolRaw(values map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw, ok := values[key]; ok && len(raw) > 0 && string(raw) != "null" {
			return raw
		}
	}
	return nil
}

func compactCursorStructuredJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		return compact.String()
	}
	return strings.TrimSpace(string(raw))
}

// cursorResultErrorMessage decides whether a completed structured run
// represents a genuine error, mirroring claudecode's identical, live-verified
// fix: a "result" event with is_error:true is a semantic failure the CLI can
// report with exit code 0, and must not be returned as a successful
// StopReason:"stop" response.
//
// Unlike the Claude fix (reproduced live with a bad --model id, which claude
// reports as a real is_error:true stream-json result), three attempts to
// force a genuine is_error:true out of real cursor-agent (bad --model, bad
// --workspace, a broken MCP server config) each either got pre-flight
// plain-text-rejected before any JSON streamed, or was silently tolerated —
// none reached this code path live. cursorEvent.IsError was already parsed
// (just never read) before this fix, presumably because someone observed it
// in real output at some point; this fix is correct by the same contract
// Claude's is, but is NOT live-proven against real cursor-agent the way
// Claude's is — flagged honestly rather than claimed. See
// TestCursorResultErrorMessage for the (deterministic, logic-only) coverage
// that does exist.
func cursorResultErrorMessage(isError bool, content, stderrText string) (msg string, isErr bool) {
	if !isError {
		return "", false
	}
	msg = strings.TrimSpace(content)
	if msg == "" {
		msg = strings.TrimSpace(stderrText)
	}
	return msg, true
}

func (c *CursorCLIAdapter) generateContentStructured(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions, sink *llmtypes.StreamSink) (*llmtypes.ContentResponse, error) {
	// Structured contract §7: "close the stream channel after process exit or
	// error." Every return path below runs either before the event-parsing
	// goroutine starts (early validation/launch errors — nothing has written
	// to the channel yet) or after <-scannerDone (that goroutine, and every
	// emitChunk call within it, has already finished) — so closing here on
	// return is safe from every path and exactly once. Without this, a caller
	// doing `for chunk := range opts.StreamChan` blocks forever even after a
	// clean process exit (a real bug found and fixed live tonight).
	if opts != nil && opts.StreamChan != nil {
		defer close(opts.StreamChan)
	}
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

	// Decide the argv-affecting values here; the SHAPE is assembled by the
	// unit-tested builder below (buildCursorStructuredArgs). Disk side-effects
	// (.cursor/mcp.json, skill projection) stay in this function.
	workingDir := cursorWorkingDirFromOptions(opts)
	modelToUse := cursorCLIModelForLaunch(c.modelID)
	mode := ""
	sandbox := ""
	approveMCPs := false
	denyBuiltins := false
	force := true // preserve the historical direct structured-launch default.
	autoReview := false
	resumeID := ""
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyCursorModel].(string); ok && strings.TrimSpace(model) != "" {
			modelToUse = resolveCursorCLIModelID(model)
		}
		if m, ok := opts.Metadata.Custom[MetadataKeyMode].(string); ok {
			mode = strings.TrimSpace(m)
		}
		// Deny-builtins is honoured here the same way tmux honours it: by writing
		// .cursor/hooks.json before launch (see denyBuiltins below).
		//
		// This used to resolve to "--mode ask" instead, on the premise that a
		// one-shot --print launch had no hook mechanism available. That premise
		// was wrong — hooks are files in .cursor/, and this path already writes
		// .cursor/mcp.json into the same directory before launching. The cost was
		// severe: "ask" is a read-only stance, so every structured step that had
		// to write $STEP_OUTPUT_DIR or drive a browser refused its own task with
		// "Ask mode blocks browser automation ... switch to Agent mode", which
		// reads like a model excuse and is in fact an accurate report.
		if deny, ok := opts.Metadata.Custom[MetadataKeyDenyBuiltinTools].(bool); ok && deny {
			denyBuiltins = true
		}
		if s, ok := opts.Metadata.Custom[MetadataKeySandbox].(string); ok && strings.TrimSpace(s) != "" {
			sandbox = strings.TrimSpace(s)
		}
		if approve, ok := opts.Metadata.Custom[MetadataKeyApproveMCPs].(bool); ok && approve {
			approveMCPs = true
		}
		if value, ok := opts.Metadata.Custom[MetadataKeyForce].(bool); ok {
			force = value
		}
		if value, ok := opts.Metadata.Custom[MetadataKeyAutoReview].(bool); ok && value {
			autoReview = true
			force = false
		}
		if rid, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && strings.TrimSpace(rid) != "" {
			resumeID = strings.TrimSpace(rid)
		}
	}

	var configCleanups []func()
	defer func() {
		for _, fn := range configCleanups {
			fn()
		}
	}()
	hooksInstalled := false
	if workingDir != "" && opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		// cursor-agent discovers .cursor/mcp.json by walking up from workingDir
		// to the nearest git repo root, not by reading workingDir literally. A
		// Video Studio project folder is a plain subdirectory (not its own git
		// repo), so without this marker cursor-agent walks up to the enclosing
		// monorepo root, finds no .cursor/mcp.json there, and silently never
		// spawns the api-bridge MCP server — the bridge tool then either fails
		// to appear at all or reports connection errors the model faithfully
		// relays as "not connected". Same fix as the tmux/interactive adapter's
		// prepareCursorProjectFiles; see its comment for the original diagnosis.
		if !cursorWorkingDirIsGitRoot(workingDir) {
			if mkErr := initCursorWorkspaceGitMarker(workingDir); mkErr == nil {
				configCleanups = append(configCleanups, func() {
					_ = os.RemoveAll(filepath.Join(workingDir, ".git"))
				})
			}
		}
		cursorDir := filepath.Join(workingDir, ".cursor")
		if mcpJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpJSON) != "" {
			cleanup, werr := writeCursorRestoredFile(filepath.Join(cursorDir, "mcp.json"), []byte(mcpJSON), true)
			if werr != nil {
				return nil, fmt.Errorf("cursor MCP config: %w", werr)
			}
			configCleanups = append(configCleanups, cleanup)
			// Pre-approve the injected servers' tools, exactly as the tmux path
			// does. Without this, a headless --print launch that withholds
			// --force (it must, or the deny-builtin hooks are inert) has nobody
			// to answer cursor's per-tool prompt for a non-read-only MCP tool,
			// and cursor-agent fails the call with
			// {"rejected":{"reason":"User rejected MCP: api-bridge-execute_shell_command"}}
			// -- every bridge write/shell step was dead on arrival (RTS, 2026-09-03).
			projectCfg, _ := opts.Metadata.Custom[MetadataKeyProjectConfig].(string)
			cliJSON, ok, cerr := cursorStructuredCLIConfig(mcpJSON, projectCfg)
			if cerr != nil {
				return nil, fmt.Errorf("cursor CLI permissions config: %w", cerr)
			}
			if ok {
				cleanup, werr := writeCursorRestoredFile(filepath.Join(cursorDir, "cli.json"), []byte(cliJSON), true)
				if werr != nil {
					return nil, fmt.Errorf("cursor CLI permissions config: %w", werr)
				}
				configCleanups = append(configCleanups, cleanup)
			}
		}
		if denyBuiltins {
			// Same hooks the tmux path installs, in the same place. They deny
			// cursor's built-in shell/file tools so the agent routes through the
			// MCP bridge, while leaving it in agent mode and able to act.
			cleanup, werr := writeCursorDenyBuiltinHooks(cursorDir, true)
			if werr != nil {
				return nil, fmt.Errorf("cursor deny-builtin hooks: %w", werr)
			}
			configCleanups = append(configCleanups, cleanup)
			hooksInstalled = true
		}
	}
	if denyBuiltins && !hooksInstalled {
		// No working directory means nowhere to put .cursor/hooks.json, so the
		// containment the caller asked for cannot be applied. Fall back to
		// cursor's read-only stance rather than silently running unconstrained —
		// but only here, where there is genuinely no alternative.
		if mode == "" {
			mode = "ask"
		}
	}
	if workingDir != "" {
		// Was completely unwired until now. No --skill flag for cursor (unlike
		// pi) — cursor's own Agent Skills loader auto-discovers .cursor/skills/
		// at session start, same as the tmux path (cursorcli_interactive_adapter.go),
		// so projecting to disk before launch is the entire fix.
		if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
			_ = c.ProjectSkills(workingDir, skills)
		}
	}

	args := buildCursorStructuredArgs(workingDir, modelToUse, mode, sandbox, approveMCPs, hooksInstalled, force, autoReview, resumeID, prompt)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = llmtypes.MergeCodingAgentSecretEnvironment(buildCursorStructuredEnv(c.apiKey), opts)

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
	// True once this span emitted token-level deltas, so the assembled repeat
	// that follows can be dropped instead of duplicating the text.
	var streamedDeltasThisSpan bool
	// segments accumulates one completed text block per assistant "reply span"
	// — a run of text bounded by tool calls. Cursor's own end-of-turn "result"
	// field re-joins those spans WITHOUT a separator when a tool call sits
	// between two of them (confirmed against a live run: result:"Checking the
	// file now.Done reading it." — no space). segments lets us reconstruct the
	// turn's text ourselves, correctly spaced, instead of trusting that field.
	var segments []string
	var totalUsage llmtypes.Usage
	var cacheWriteTokens int
	var sessionID string
	var modelName string
	var resultText string
	// resultIsError mirrors claudecode's identical fix: a "result" event with
	// is_error:true is a genuine semantic failure the CLI can report with
	// exit code 0. The field was already parsed into cursorEvent.IsError but
	// never read, so an is_error result with non-empty Result text (the error
	// description) sailed through as a "successful" StopReason:"stop"
	// response — the same bug class fixed live for Claude this session.
	var resultIsError bool
	spanFragments := "" // concatenation of the current span's streamed fragments

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	// scannerDone closes when the scanner loop returns — i.e. stdout reached
	// EOF, which means the cursor-agent process has actually exited. Used by
	// procshutdown.Graceful to observe end-of-life (see structured shutdown
	// contract §9). Writes to finalContent/totalUsage/sessionID/modelName
	// inside the loop are made visible to the main goroutine by the
	// happens-before relationship from close → receive.
	// StreamChunk.ToolDuration existed but was never set by any
	// structured adapter, so the whole chain downstream reported zero:
	// ToolCallEndEvent.Duration, ToolCallEntry.Duration, and the persisted
	// timing summary's total_duration_ms. A turn then looked entirely
	// generation-bound even when real tool time was part of its wall clock.
	toolStartedAt := map[string]time.Time{}
	toolCalls := map[string]cursorStructuredToolCall{}
	scannerDone := make(chan struct{})
	go func() {
		defer close(scannerDone)
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
						// Cursor exposes a user-safe progress/thinking stream separately
						// from assistant content. Preserve that distinction so product
						// UIs render it in their Thinking surface instead of prepending
						// it to the final answer. These are token-level fragments of one
						// thinking span ("Reading" / " soul.md" / " first."), so they
						// carry the same delta marker the assistant fragments do -- a
						// consumer that treated each as its own block rendered a
						// column of one-line "Thinking" cards (RTS, 2026-09-03).
						Type:     llmtypes.StreamChunkTypeReasoning,
						Content:  event.Text,
						Metadata: map[string]interface{}{llmtypes.ContentDeltaMetadataKey: true},
					})
				}

			case "assistant":
				if event.Message != nil {
					text := cursorEventMessageText(event.Message)
					if text != "" {
						// Cursor streams a span TWICE: first as token-level
						// fragments (subtype "delta", which can split mid-word —
						// "Reading" / " the" / " build"), then once more as the
						// assembled span. Emitting both duplicated every sentence
						// in the output, and emitting the fragments WITHOUT the
						// delta marker made a reassembler "\n"-join them, so a
						// streamed markdown table arrived as a column of single
						// pipes. Verified on real cursor-agent output.
						//
						// So: forward the fragments marked as deltas (the
						// progressive signal a UI wants), and drop the assembled
						// repeat — finalContent still tracks it for the return
						// value, which is what the non-streaming path uses.
						// Evidence + the full provider/transport matrix this came
						// from: coding-agent-loop docs/design/
						// product_api_transport_for_coding_agents.md ("Measured matrix").
						//
						// Verified against real cursor-agent output: under
						// --stream-partial-output every "assistant" event carries a
						// FRAGMENT and has NO subtype at all (7 events for a single
						// sentence: "Reading" / " the" / " build" ...). The assembled
						// text arrives separately on the "result" event, so these are
						// unconditionally token-level deltas. Checking for
						// subtype=="delta" here matched nothing and left them
						// unmarked, which is what made a reassembler "\n"-join them
						// and render a streamed table as a column of bare pipes.
						isDelta := event.Subtype == "" || event.Subtype == "delta"
						// cursor-agent 2026.09.02 sends the assembled span as an
						// "assistant" event with NO subtype either, so the subtype
						// check alone let it through as one more delta and every
						// sentence rendered twice (RTS, 2026-09-03). Fragments carry
						// timestamp_ms and the assembled repeat does not; as a
						// belt-and-braces check, an event whose text equals what the
						// fragments already added up to is that repeat as well.
						assembledRepeat := streamedDeltasThisSpan &&
							(!isDelta || event.TimestampMS == nil || text == spanFragments)
						if assembledRepeat {
							finalContent = text
							streamedDeltasThisSpan = false
							spanFragments = ""
							continue
						}
						finalContent = text
						if isDelta {
							spanFragments += text
						}
						chunk := llmtypes.StreamChunk{
							Type:    llmtypes.StreamChunkTypeContent,
							Content: text,
						}
						if isDelta {
							streamedDeltasThisSpan = true
							chunk.Metadata = map[string]interface{}{llmtypes.ContentDeltaMetadataKey: true}
						}
						emitChunk(chunk)
					}
				}

			case "tool_call":
				switch event.Subtype {
				case "started":
					// A tool call closes off whatever text span was building.
					// Cursor starts a fresh span after the tool result comes
					// back, and re-joining those spans is exactly where its
					// own "result" field loses the separating space — bank
					// this span now, correctly trimmed, before it's lost.
					if trimmed := strings.TrimSpace(finalContent); trimmed != "" {
						segments = append(segments, trimmed)
						finalContent = ""
					}
					// The span is over; the next assistant text starts a new one.
					streamedDeltasThisSpan = false
					spanFragments = ""
					toolStartedAt[event.CallID] = time.Now()
					details := cursorStructuredToolCallDetails(event.ToolCall)
					toolCalls[event.CallID] = details
					emitChunk(llmtypes.StreamChunk{
						Type:       llmtypes.StreamChunkTypeToolCallStart,
						Content:    fmt.Sprintf("tool_call(%s)", event.CallID),
						ToolName:   details.Name,
						ToolCallID: event.CallID,
						ToolArgs:   details.Args,
					})
				case "completed":
					// Name and args come from the "started" payload; the completed
					// one carries the result and usually repeats neither, so parse
					// it separately instead of overwriting the cached details.
					completed := cursorStructuredToolCallDetails(event.ToolCall)
					details := toolCalls[event.CallID]
					if details.Name == "" {
						details = completed
					}
					emitChunk(llmtypes.StreamChunk{
						Type:         llmtypes.StreamChunkTypeToolCallEnd,
						Content:      event.CallID,
						ToolName:     details.Name,
						ToolCallID:   event.CallID,
						ToolArgs:     details.Args,
						ToolResult:   completed.Result,
						ToolDuration: toolclock.Elapsed(toolStartedAt, event.CallID),
					})
				}

			case "result":
				// End-of-turn teardown per the structured-CLI shutdown contract
				// (docs/coding_sdk_structured_contract.md §9): SIGTERM → 5s
				// grace for ~/.cursor state flush → SIGKILL.
				go procshutdown.GracefulAfterNaturalExit(cmd, scannerDone, 3*time.Second, c.logger)
				resultIsError = event.IsError
				resultText = event.Result
				if event.Usage != nil {
					totalUsage.InputTokens += event.Usage.InputTokens
					totalUsage.OutputTokens += event.Usage.OutputTokens
					totalUsage.TotalTokens += event.Usage.InputTokens + event.Usage.OutputTokens
					if event.Usage.CacheReadTokens > 0 {
						cacheRead := event.Usage.CacheReadTokens
						totalUsage.CacheTokens = &cacheRead
					}
					cacheWriteTokens += event.Usage.CacheWriteTokens
				}
				if event.SessionID != "" {
					sessionID = event.SessionID
				}
			}
		}
	}()
	<-scannerDone

	waitErr := cmd.Wait()

	// Bank whatever text span was still building when the stream ended.
	if trimmed := strings.TrimSpace(finalContent); trimmed != "" {
		segments = append(segments, trimmed)
	}
	content := joinTextWithSpacing(segments)
	if resultIsError {
		// Cursor's own error text (e.g. an auth failure) never streams as
		// assistant-text segments, so there's nothing to reconstruct from —
		// use it directly.
		content = strings.TrimSpace(resultText)
	} else if content == "" {
		// Defensive fallback: reconstruction found nothing (an unexpected
		// event shape), but Cursor's own end-of-turn summary did.
		content = strings.TrimSpace(resultText)
	}

	if errMsg, isErr := cursorResultErrorMessage(resultIsError, content, stderr.String()); isErr {
		return nil, fmt.Errorf("cursor run reported an error result: %s", errMsg)
	}

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
	if cacheWriteTokens > 0 {
		additional["cache_creation_input_tokens"] = cacheWriteTokens
	}
	if (totalUsage.CacheTokens != nil && *totalUsage.CacheTokens > 0) || cacheWriteTokens > 0 {
		// Cursor reports fresh input separately from its cache buckets.
		additional["prompt_tokens_include_cache"] = false
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
	if strings.TrimSpace(sessionID) != "" {
		// The structured CLI owns a fresh process for each turn, so its native
		// session ID is the only durable continuity mechanism. Attach the typed
		// handle as well as the legacy cursor_session_id metadata; mcpagent and
		// product runtimes persist this handle and pass it back as --resume after
		// a server restart.
		llmtypes.AttachCodingProviderSessionHandle(genInfo, llmtypes.CodingProviderSessionHandle{
			Provider:        "cursor-cli",
			Transport:       llmtypes.CodingProviderTransportStructured,
			NativeSessionID: sessionID,
			WorkingDir:      workingDir,
			Model:           costLookupModel,
			Status:          llmtypes.CodingProviderSessionStatusIdle,
		})
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

// cursorEventMessageText joins the text blocks of a single assistant message.
// Cursor's stream sends distinct text blocks for separate checkpoints/paragraphs
// (e.g. "...building the scenes." then "I'll build the eight scenes..." as two
// blocks), each already trimmed of surrounding whitespace. A bare join glues
// them into one run-on word ("scenes.I'll"), so a block boundary gets a space
// unless one side already supplies it or the next block opens with punctuation
// that attaches to what precedes it (e.g. "world" + "!" must stay "world!").
func cursorEventMessageText(msg *cursorEventMessage) string {
	parts := make([]string, 0, len(msg.Content))
	for _, c := range msg.Content {
		if c.Type != "text" || c.Text == "" {
			continue
		}
		parts = append(parts, c.Text)
	}
	return joinTextWithSpacing(parts)
}

// joinTextWithSpacing concatenates already-trimmed text pieces, inserting a
// space at a boundary only when neither side already supplies one and the
// next piece isn't attaching punctuation (see attachingPunctuation).
func joinTextWithSpacing(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if b.Len() > 0 && !endsWithSpace(b.String()) && !startsWithSpaceOrAttachingPunct(p) {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	return b.String()
}

func endsWithSpace(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r != utf8.RuneError && unicode.IsSpace(r)
}

// attachingPunctuation is trailing punctuation that hugs the text before it
// rather than starting a new clause/word, so it must never gain a space of
// its own.
const attachingPunctuation = ".,!?;:)]}"

func startsWithSpaceOrAttachingPunct(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return false
	}
	return unicode.IsSpace(r) || strings.ContainsRune(attachingPunctuation, r)
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
