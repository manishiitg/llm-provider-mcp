package claudecode

import (
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
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/nativeinput"
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
	SDKContinues bool `json:"sdk_continues,omitempty"`
	// SDKParked: this final result ends the turn while Claude's own background
	// tasks still run, so the bridge kept the session open (PLAT-872).
	SDKParked bool `json:"sdk_parked,omitempty"`
	// SDKParkedTurn: output of a model turn the SDK ran on its own in a kept
	// session; never part of the host's turn.
	SDKParkedTurn bool                `json:"sdk_parked_turn,omitempty"`
	Background    []claudeSDKWireTask `json:"background,omitempty"`
	SDKHold       *struct {
		Unsettled  int `json:"unsettled"`
		MovedAside int `json:"moved_aside"`
		Queued     int `json:"queued"`
		Background int `json:"background"`
	} `json:"sdk_hold,omitempty"`
	InputNotSent bool            `json:"input_not_sent,omitempty"`
	ControlID    string          `json:"id,omitempty"`
	Errors       []string        `json:"errors,omitempty"`
	RequestID    string          `json:"request_id,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Event        *struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event,omitempty"`

	ParentToolUseID string               `json:"parent_tool_use_id,omitempty"`
	Type            string               `json:"type"`
	Subtype         string               `json:"subtype,omitempty"`
	SessionID       string               `json:"session_id,omitempty"`
	Message         *claudeStreamMessage `json:"message,omitempty"`
	Result          string               `json:"result,omitempty"`
	IsError         bool                 `json:"is_error,omitempty"`
	Usage           *claudeStreamUsage   `json:"usage,omitempty"`
	// system/status: "compacting" while compaction runs, null afterwards
	// with compact_result. system/compact_boundary: compact_metadata.
	Status          *string                `json:"status,omitempty"`
	CompactResult   string                 `json:"compact_result,omitempty"`
	CompactMetadata *claudeCompactMetadata `json:"compact_metadata,omitempty"`
	// rate_limit_event: plan window usage.
	RateLimitInfo *claudeRateLimitInfo `json:"rate_limit_info,omitempty"`
	Model         string               `json:"model,omitempty"`
}

type claudeStreamMessage struct {
	Model   string                     `json:"model,omitempty"`
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
		if b.Len() > 0 {
			return b.String()
		}
		// ToolSearch and other deferred tools return typed blocks such as
		// tool_reference rather than text. An empty string loses the entire
		// successful result and makes the normalized receipt indistinguishable
		// from a provider that never supplied one. Preserve the structured value
		// when no text block exists.
		var compact bytes.Buffer
		if json.Compact(&compact, raw) == nil {
			return compact.String()
		}
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
	if c.sdkTransport {
		ctx = llmtypes.ScopeCodingAgentQuestions(ctx, "claude-code", claudeInteractiveSessionIDFromOptions(opts))
	}
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
	if claudeResumeSessionIDFromStructuredOptions(opts) != "" {
		conversationMessages = llmtypes.PendingHumanMessages(conversationMessages)
	}
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

	if c.sdkTransport && permissionMode == "" {
		permissionMode = "dontAsk"
		if llmtypes.CodingAgentQuestionsAvailable(ctx) {
			permissionMode = "default"
		}
		if opts != nil && opts.Metadata != nil && opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions] == true {
			permissionMode = "bypassPermissions"
		}
	}
	var tempFiles []string
	defer func() {
		// removeFiles, not a bare os.Remove loop: writeClaudeCodeProjectInstructionFile
		// may register an operator's prior CLAUDE.md bytes for byte-restore, and
		// deleting the path instead of restoring it would destroy their content.
		removeFiles(tempFiles)
	}()
	// Files the launched process reads for its whole life (MCP config, settings).
	// A kept SDK session owns them until it exits; otherwise they go with the turn.
	var launchFiles []string
	launchFilesHanded := false
	defer func() {
		if !launchFilesHanded {
			removeFiles(launchFiles)
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
		launchFiles = append(launchFiles, configPath)
		mcpConfigPath = configPath
	}

	// Full CLI mode keeps native Bash on, but only the bridge's shell has the
	// platform credentials: refuse a platform call there and name the bridge
	// shell tool. Settings go in a per-launch file, so nothing needs restoring.
	var redirectSettingsPath string
	if mcpConfigJSON != "" {
		settingsMap := map[string]any{}
		if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
			if v, ok := opts.Metadata.Custom[MetadataKeySettings].(string); ok {
				settingsMap = claudeLoadSettingsMap(v)
			}
		}
		if added, hookErr := claudeAddShellRedirectHook(settingsMap, mcpConfigJSON); hookErr != nil {
			return nil, fmt.Errorf("claude structured shell redirect: %w", hookErr)
		} else if added {
			raw, mErr := json.Marshal(settingsMap)
			if mErr == nil {
				if p, wErr := writeTempJSONConfig("claude-code-structured-settings-*.json", string(raw)); wErr == nil {
					launchFiles = append(launchFiles, p)
					redirectSettingsPath = p
				}
			}
		}
	}

	projectedPrompt := false
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
			projectedPrompt = true
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
	launchPrompt := systemPrompt + llmtypes.CLIPrivateSkillInstructions(opts, ".claude/skills")
	if c.sdkTransport && projectedPrompt && projectInstructionOnlyFromOptions(opts) {
		launchPrompt = ""
	}
	args, sessionID := buildClaudeStructuredArgs(c.modelID, launchPrompt, tools, allowedTools, permissionMode, mcpConfigPath, resumeSessionID, freshSessionID, workingDir)
	if c.sdkTransport && opts != nil && opts.Metadata != nil {
		if effort, ok := opts.Metadata.Custom[MetadataKeyEffort].(string); ok && effort != "" {
			args = append(args, "--effort", effort)
		}
		if _, supplied := opts.Metadata.Custom[MetadataKeyTools]; supplied && tools == "" {
			args = append(args, "--tools", "")
		}
	}
	if c.sdkTransport && redirectSettingsPath == "" && opts != nil && opts.Metadata != nil {
		if settings, ok := opts.Metadata.Custom[MetadataKeySettings].(string); ok && settings != "" {
			raw, err := json.Marshal(claudeLoadSettingsMap(settings))
			if err != nil {
				return nil, err
			}
			path, err := writeTempJSONConfig("claude-code-sdk-settings-*.json", string(raw))
			if err != nil {
				return nil, err
			}
			launchFiles = append(launchFiles, path)
			redirectSettingsPath = path
		}
	}
	if redirectSettingsPath != "" {
		args = append(args, "--settings", redirectSettingsPath)
	}
	args = append(args, claudeSeatbeltAddDirs(opts, workingDir)...)

	if workingDir != "" {
		if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
			// Project skills to <workdir>/.claude/skills — claude discovers them
			// natively from the working dir, same as the tmux path. (Disk
			// side-effect; --add-dir is added by the builder.)
			_ = c.ProjectSkills(llmtypes.CLIArtifactDirectory(opts, workingDir), skills)
		}
	}

	owner := claudeInteractiveSessionIDFromOptions(opts)
	environment := append(claudeStructuredProcessEnv(os.Environ(), opts, c.oauthToken), claudeSkillScopeEnv()...)
	var cmd *exec.Cmd
	var lines <-chan string
	var reader *claudeLineReader
	var stderr *claudeLockedBuffer
	var sess *claudeSDKSession
	var sdkProcess *claudeSDKProcess
	// A chat's SDK session may be kept after the turn while Claude's own
	// background tasks run (PLAT-872); see claudecode_sdk_session.go.
	keepAllowed := false
	if c.sdkTransport {
		fingerprint, fingerprintOK := claudeSDKFingerprint(c.modelID, systemPrompt+"\x00"+launchPrompt, permissionMode, environment, opts, llmtypes.CodingAgentQuestionsAvailable(ctx))
		keepAllowed = owner != "" && claudePersistentInteractiveFromOptions(opts)
		if owner != "" {
			sess = takeKeptClaudeSDKSession(owner, fingerprint, fingerprintOK, resumeSessionID)
		}
		if sess != nil {
			cmd = sess.cmd
			if c.logger != nil {
				c.logger.Infof("Continuing kept Claude Agent SDK session: background tasks=%d", sess.taskCount())
			}
		} else {
			var setupStream chan<- llmtypes.StreamChunk
			if opts != nil {
				setupStream = opts.StreamChan
			}
			if err := llmtypes.PrepareCodingSDK(ctx, "claude-code", setupStream); err != nil {
				return nil, err
			}
			// The process outlives this turn when it is kept, so its life is not the turn's context;
			// a cancelled turn still ends it (below).
			life, kill := context.WithCancel(context.Background())
			var cleanupBridge func()
			cmd, cleanupBridge, err = claudeSDKCommand(life, binPath, args)
			if err != nil {
				kill()
				return nil, err
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				kill()
				cleanupBridge()
				return nil, err
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			procshutdown.KillGroupOnCancel(cmd)
			if workingDir != "" {
				cmd.Dir = workingDir
			}
			cmd.Env = environment
			cleanupSandbox, err := claudeSDKLandlock(opts, cmd, workingDir)
			if err != nil {
				kill()
				cleanupBridge()
				return nil, fmt.Errorf("confine Claude Code: %w", err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				kill()
				cleanupSandbox()
				cleanupBridge()
				return nil, fmt.Errorf("claude stdout pipe: %w", err)
			}
			errBuf := &claudeLockedBuffer{}
			cmd.Stderr = errBuf
			if c.logger != nil {
				c.logger.Infof("Executing Claude Agent SDK: streaming input/output, native resume=%t", resumeSessionID != "")
			}
			files := launchFiles
			sess, err = startClaudeSDKSession(owner, fingerprint, cmd, stdin, stdout, errBuf, kill, func() {
				cleanupSandbox()
				cleanupBridge()
				removeFiles(files)
			}, c.logger)
			if err != nil {
				kill()
				cleanupSandbox()
				cleanupBridge()
				return nil, fmt.Errorf("claude start: %w", err)
			}
			launchFilesHanded = true
			sess.setNativeID(sessionID)
		}
		turnDone := make(chan struct{})
		defer close(turnDone)
		session := sess
		sdkProcess = &claudeSDKProcess{session: sess, cancel: sess.kill, done: turnDone}
		go func() {
			select {
			case <-ctx.Done():
				session.kill() // a cancelled turn ends its session, as before
			case <-turnDone:
			}
		}()
		reader, stderr = sess.reader, sess.stderr
		lines = reader.lines
		if owner != "" {
			claudeSDKProcesses.Set(owner, sdkProcess)
			defer claudeSDKProcesses.DeleteIf(owner, sdkProcess)
		}
		if err := sdkProcess.write(map[string]any{"type": "prompt", "text": prompt}); err != nil {
			sess.kill()
			<-sess.exited
			return nil, fmt.Errorf("Claude SDK input: %w", err)
		}
	} else {
		runCtx, cancelRun := context.WithCancel(ctx)
		defer cancelRun()
		cmd = exec.CommandContext(runCtx, binPath, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		procshutdown.KillGroupOnCancel(cmd)
		if workingDir != "" {
			cmd.Dir = workingDir
		}
		cmd.Env = environment
		cmd.Stdin = strings.NewReader(prompt) // one-shot structured input
		cleanupSandbox, err := claudeLandlockCmd(opts, cmd, workingDir)
		if err != nil {
			return nil, fmt.Errorf("confine Claude Code: %w", err)
		}
		defer cleanupSandbox()
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("claude stdout pipe: %w", err)
		}
		stderr = &claudeLockedBuffer{}
		cmd.Stderr = stderr
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
		reader = startClaudeLineReader(stdout)
		lines = reader.lines
	}
	var questions nativeinput.Requests
	defer questions.Close()
	var scanErr error
	var text claudeStructuredText
	var resultText string
	var previousResultContinues bool
	parkedTurn := false
	sawNativeSubagent := false
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
	var compaction claudeStructuredCompaction
	liveUsage := &claudeLiveUsage{}
	usageThrottle := &llmtypes.LiveUsageThrottle{}
	emitLiveUsage := func(force bool) {
		usageThrottle.Offer(liveUsage.statusLine())
		if status := usageThrottle.Take(time.Now(), force); status != nil {
			emitChunk(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeStatusLine, StatusLine: status})
		}
	}

	go func() {
		defer close(scannerDone)
		for raw := range lines {
			line := strings.TrimSpace(raw)
			if line == "" || line[0] != '{' {
				continue
			}
			var ev claudeStreamEvent
			if jErr := json.Unmarshal([]byte(line), &ev); jErr != nil {
				continue
			}
			if ev.SDKParkedTurn {
				// Output of a model turn the SDK ran on its own in a kept session, not this turn's.
				if ev.Type == "result" && c.logger != nil {
					c.logger.Infof("[SDK_BACKGROUND] dropped a model turn that was not this chat turn session=%s", owner)
				}
				continue
			}
			if ev.ParentToolUseID != "" {
				continue
			}
			if ev.SessionID != "" {
				sessionID = ev.SessionID
				if sess != nil {
					sess.setNativeID(sessionID)
				}
			}
			text.observe(ev, emitChunk)
			switch ev.Type {
			case "sdk_permission":
				if sdkProcess != nil {
					request := ev
					questions.Start(ctx, request.RequestID, func(questionCtx context.Context) {
						response := claudeSDKPermission(questionCtx, request.ToolName, request.Input)
						_ = sdkProcess.write(map[string]any{"type": "permission", "id": request.RequestID, "response": response})
					})
				}
			case "sdk_control_result":
				if sdkProcess != nil {
					sdkProcess.acknowledgeControl(ev.ControlID, ev.IsError, ev.InputNotSent)
				}
			case "sdk_permission_cancel":
				questions.Cancel(ev.RequestID)
			case "sdk_background":
				if sess != nil {
					sess.setTasks(claudeSDKTasksFromWire(ev.Background))
				}

			case "system":
				if ev.Subtype == "init" && ev.Model != "" {
					liveUsage.model = ev.Model
				}
				for _, c := range compaction.observe(ev, time.Now()) {
					emitChunk(llmtypes.ContextCompactionChunk(c))
				}
			case "rate_limit_event":
				if liveUsage.observeRateLimit(ev.RateLimitInfo) {
					emitLiveUsage(false)
				}
			case "assistant":
				if ev.Message != nil && liveUsage.observeUsage(ev.Message.Usage) {
					if ev.Message.Model != "" {
						liveUsage.model = ev.Message.Model
					}
					emitLiveUsage(false)
				}
				if ev.Message != nil {
					for _, block := range ev.Message.Content {
						if block.Type == "tool_use" && block.Name != "" {
							if block.Name == "Agent" || block.Name == "Task" {
								sawNativeSubagent = true
							}
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
						if pending.name == "Agent" || pending.name == "Task" {
							children := readClaudeNativeSubagents(sessionID, workingDir, llmtypes.CLIHomeEnvironment(opts)["HOME"])
							if len(children) > 0 {
								emitChunk(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeNativeSubagentTranscript, Metadata: map[string]interface{}{"native_subagents": children}})
							}
						}
					}
				}
			case "result":
				// The turn ends only when sdk_continues is false; log why it stays open.
				if ev.SDKHold != nil {
					c.logger.Infof("[CLAUDE_SDK] result session=%s continues=%t unsettled=%d moved_aside=%d queued=%d background=%d", sessionID, ev.SDKContinues, ev.SDKHold.Unsettled, ev.SDKHold.MovedAside, ev.SDKHold.Queued, ev.SDKHold.Background)
				} else {
					c.logger.Infof("[CLAUDE_SDK] result session=%s continues=%t", sessionID, ev.SDKContinues)
				}
				emitLiveUsage(true)
				sawResult = true
				resultIsError = ev.IsError
				for _, line := range append([]string{ev.Result}, ev.Errors...) {
					if isClaudeDiagnosticResult(line) {
						c.logger.Infof("[CLAUDE_SDK] dropped diagnostic result: %s", strings.TrimSpace(line))
					}
				}
				resultText = claudeContinuedResultText(resultText, ev, previousResultContinues)
				previousResultContinues = ev.SDKContinues
				if ev.Usage != nil {
					accumulateClaudeUsage(&totalUsage, ev.Usage)
					totalCacheReadTokens += ev.Usage.CacheReadInputTokens
					totalCacheWriteTokens += ev.Usage.CacheCreationInputTokens
				}
				// Claude emits the result before its resumable transcript is
				// guaranteed to be flushed. Let it exit naturally first; the
				// signal sequence remains a backstop for a stuck CLI.
				if !ev.SDKContinues {
					if ev.SDKParked && sess != nil {
						if keepAllowed {
							// The answer ends the turn; the session stays for the running tasks.
							parkedTurn = true
							return
						}
						// Not a chat that keeps sessions: end it as before; its tasks stop with it.
						c.logger.Infof("[SDK_BACKGROUND] stopped session=%s tasks=%d reason=not_kept", owner, sess.taskCount())
						_ = sess.input.write(map[string]any{"type": "stop_background", "id": randomHex(12)})
					}
					go procshutdown.GracefulAfterNaturalExit(cmd, reader.done, claudeStructuredNaturalExitGrace, c.logger)
				}
			}
		}
	}()

	<-scannerDone
	var waitErr error
	if parkedTurn {
		sess.park()
	} else {
		if sess != nil {
			<-sess.exited
			waitErr = sess.waitErr
		} else {
			<-reader.done
			waitErr = cmd.Wait()
		}
		<-reader.done
		scanErr = reader.err
	}
	if ctx.Err() != nil {
		if parkedTurn {
			sess.closeIfParked("stopped", "stopped", "turn_cancelled")
		}
		return nil, ctx.Err()
	}
	if scanErr != nil {
		return nil, fmt.Errorf("Claude stream: %w", scanErr)
	}
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
	if resultIsError && sdkProcess != nil && sdkProcess.interrupted.Load() {
		// A turn the person stopped is a stop, not a failure (PLAT-872-65).
		return nil, fmt.Errorf("Claude turn stopped by the person: %w", context.Canceled)
	}
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
		content = strings.TrimSpace(text.completed.String())
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
	transport := llmtypes.CodingProviderTransportStructured
	if c.sdkTransport {
		additional["claude_code_mode"] = "sdk"
		transport = llmtypes.CodingProviderTransportSDK
	}
	// Same contract as the interactive adapter: every assistant text block of
	// the turn, for surfaces that show the conversation rather than a result.
	if turnText := strings.TrimSpace(text.completed.String()); turnText != "" && turnText != content {
		additional["assistant_turn_text"] = turnText
	}
	if sawNativeSubagent {
		if children := readClaudeNativeSubagents(sessionID, workingDir, llmtypes.CLIHomeEnvironment(opts)["HOME"]); len(children) > 0 {
			additional["native_subagents"] = children
		}
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
		Transport:       transport,
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

// claudeStructuredProcessEnv applies the workflow-scoped Claude setup token at
// the final process boundary used by `claude -p`. Workflow steps use this
// structured transport rather than the retained tmux transport, so consulting
// only CallOptions here silently discarded the token stored on the adapter.
//
// When a workflow token is present, remove every competing ambient Anthropic
// credential before setting it. This prevents the server process's saved/default
// Claude account from winning account selection on multi-account machines.
func claudeStructuredProcessEnv(base []string, opts *llmtypes.CallOptions, oauthToken string) []string {
	environment := withoutClaudeParentSessionEnv(llmtypes.MergeCodingAgentSecretEnvironment(base, opts))
	oauthToken = strings.TrimSpace(oauthToken)
	if oauthToken == "" {
		return environment
	}

	blocked := map[string]struct{}{
		"ANTHROPIC_API_KEY":       {},
		"ANTHROPIC_AUTH_TOKEN":    {},
		"CLAUDE_CODE_OAUTH_TOKEN": {},
	}
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, discard := blocked[key]; discard {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
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

func isClaudeDiagnosticResult(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "[ede_diagnostic]")
}

// PLAT-872-47: one host turn may contain several native results after steering.
// Keep each answer the person saw, including the answer before background work.
func claudeContinuedResultText(previous string, event claudeStreamEvent, continued bool) string {
	text := event.Result
	// A segment that ended on a steered message has no answer; Claude Code
	// fills its result with an internal diagnostic line, not words for people.
	if isClaudeDiagnosticResult(text) {
		text = ""
	}
	if event.IsError && text == "" {
		// An error result after a steered message carries the same diagnostic
		// line in errors[] instead of result.
		var errs []string
		for _, line := range event.Errors {
			if !isClaudeDiagnosticResult(line) {
				errs = append(errs, line)
			}
		}
		text = strings.Join(errs, "; ")
	}
	if strings.TrimSpace(text) == "" {
		return previous
	}
	if continued && strings.TrimSpace(previous) != "" {
		return previous + "\n\n" + text
	}
	return text
}
