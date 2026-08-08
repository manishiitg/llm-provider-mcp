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
		return cursorStructuredToolCall{Name: name, Args: compactCursorStructuredJSON(argValue)}
	}
	return cursorStructuredToolCall{}
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
		cursorDir := filepath.Join(workingDir, ".cursor")
		if mcpJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpJSON) != "" {
			cleanup, werr := writeCursorRestoredFile(filepath.Join(cursorDir, "mcp.json"), []byte(mcpJSON), true)
			if werr != nil {
				return nil, fmt.Errorf("cursor MCP config: %w", werr)
			}
			configCleanups = append(configCleanups, cleanup)
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
	var totalUsage llmtypes.Usage
	var cacheWriteTokens int
	var sessionID string
	var modelName string
	// resultIsError mirrors claudecode's identical fix: a "result" event with
	// is_error:true is a genuine semantic failure the CLI can report with
	// exit code 0. The field was already parsed into cursorEvent.IsError but
	// never read, so an is_error result with non-empty Result text (the error
	// description) sailed through as a "successful" StopReason:"stop"
	// response — the same bug class fixed live for Claude this session.
	var resultIsError bool

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
						// it to the final answer.
						Type:    llmtypes.StreamChunkTypeReasoning,
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
					details := toolCalls[event.CallID]
					if details.Name == "" {
						details = cursorStructuredToolCallDetails(event.ToolCall)
					}
					emitChunk(llmtypes.StreamChunk{
						Type:         llmtypes.StreamChunkTypeToolCallEnd,
						Content:      event.CallID,
						ToolName:     details.Name,
						ToolCallID:   event.CallID,
						ToolArgs:     details.Args,
						ToolDuration: toolclock.Elapsed(toolStartedAt, event.CallID),
					})
				}

			case "result":
				// End-of-turn teardown per the structured-CLI shutdown contract
				// (docs/coding_sdk_structured_contract.md §9): SIGTERM → 5s
				// grace for ~/.cursor state flush → SIGKILL.
				go procshutdown.GracefulAfterNaturalExit(cmd, scannerDone, 3*time.Second, c.logger)
				resultIsError = event.IsError
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

	content := strings.TrimSpace(finalContent)

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
