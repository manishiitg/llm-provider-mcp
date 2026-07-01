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

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
)

// pendingToolCall tracks a tool call that has started but hasn't received its result yet
type pendingToolCall struct {
	toolName  string
	toolID    string
	toolArgs  string
	startTime time.Time
}

// Constants for custom metadata keys
const (
	MetadataKeyMCPConfig                  = "mcp_config"
	MetadataKeyDangerouslySkipPermissions = "dangerously_skip_permissions"
	MetadataKeyTools                      = "claude_code_tools"
	MetadataKeyAllowedTools               = "claude_code_allowed_tools"
	MetadataKeySettings                   = "claude_code_settings"
	MetadataKeyMaxTurns                   = "claude_code_max_turns"
	MetadataKeyResumeSessionID            = "claude_code_resume_session_id"
	MetadataKeyEffort                     = "claude_code_effort"
	MetadataKeyInteractiveSessionID       = "claude_code_interactive_session_id"
	MetadataKeyPersistentInteractive      = "claude_code_persistent_interactive"
	MetadataKeyWorkingDir                 = "claude_code_working_dir"
	// MetadataKeyWriteProjectInstructionFile is the OFF-by-default feature
	// flag for writing the per-session system prompt to .claude/rules/
	// as a markdown file the CLI auto-loads. Default off because the
	// adapter already injects via --system-prompt-file <tmp>, and
	// duplicating into a workspace file is belt-and-suspenders only useful
	// when the operator wants the prompt visible inside the cwd.
	MetadataKeyWriteProjectInstructionFile = "claude_code_write_project_instruction_file"
	// MetadataKeyRestoreProjectFiles is the OFF-by-default feature flag
	// controlling whether projected workspace artifacts (CLAUDE.md,
	// .mcp.json, and any other byte-restore writer) preserve an operator's
	// pre-existing content across the session. Default off: every run
	// writes a fresh artifact from the latest orchestrator output and
	// deletes it on cleanup, never restoring whatever was there before.
	// Pass WithRestoreProjectFiles(true) to opt back into the legacy
	// byte-restore behavior for repos with operator-owned files worth
	// preserving.
	MetadataKeyRestoreProjectFiles = "claude_code_restore_project_files"
	// MetadataKeyProjectInstructionOnly is the OFF-by-default feature flag
	// that makes the adapter inject the per-session system prompt SOLELY via
	// <workingDir>/CLAUDE.md and skip the --system-prompt-file flag. Default
	// off: the prompt goes through --system-prompt-file as usual (and is also
	// projected to CLAUDE.md when WithWriteProjectInstructionFile is on).
	// With it on, the prompt is written only to CLAUDE.md — which Claude Code
	// auto-loads as project instructions — eliminating the doubled system
	// prompt that otherwise results from passing the same bytes both ways.
	// Pass WithProjectInstructionOnly(true) to enable. If the CLAUDE.md
	// projection is disabled or fails, the adapter falls back to
	// --system-prompt-file so the prompt is never lost.
	MetadataKeyProjectInstructionOnly = "claude_code_project_instruction_only"
	claudeCodeDisableAutoMemoryEnv    = "CLAUDE_CODE_DISABLE_AUTO_MEMORY"
)

// ClaudeCodeAdapter implements the LLM interface for the Claude Code CLI.
type ClaudeCodeAdapter struct {
	modelID            string
	apiKey             string
	logger             interfaces.Logger
	providerName       string
	envOverrides       map[string]string
	modelFlagSentinels map[string]struct{}
}

// NewClaudeCodeAdapter creates a new instance of the ClaudeCodeAdapter.
func NewClaudeCodeAdapter(apiKey string, modelID string, logger interfaces.Logger) *ClaudeCodeAdapter {
	return NewProviderClaudeCodeAdapter(apiKey, modelID, "claude-code", nil, logger)
}

// NewProviderClaudeCodeAdapter creates a Claude Code adapter with provider-specific
// environment overrides. This lets Claude-compatible providers reuse the same CLI path.
func NewProviderClaudeCodeAdapter(apiKey string, modelID string, providerName string, envOverrides map[string]string, logger interfaces.Logger) *ClaudeCodeAdapter {
	sentinels := map[string]struct{}{
		"claude-code": {},
	}

	return &ClaudeCodeAdapter{
		modelID:            modelID,
		apiKey:             strings.TrimSpace(apiKey),
		logger:             logger,
		providerName:       providerName,
		envOverrides:       cloneEnvOverrides(envOverrides),
		modelFlagSentinels: sentinels,
	}
}

func cloneEnvOverrides(envOverrides map[string]string) map[string]string {
	if len(envOverrides) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(envOverrides))
	for key, value := range envOverrides {
		cloned[key] = value
	}
	return cloned
}

func (c *ClaudeCodeAdapter) shouldPassModelFlag() bool {
	modelID := strings.TrimSpace(c.modelID)
	if modelID == "" {
		return false
	}
	_, isSentinel := c.modelFlagSentinels[modelID]
	return !isSentinel
}

func (c *ClaudeCodeAdapter) buildCommandEnv() []string {
	overrideKeys := map[string]struct{}{
		"CLAUDECODE":                   {},
		"ANTHROPIC_API_KEY":            {},
		"ANTHROPIC_BASE_URL":           {},
		claudeCodeDisableAutoMemoryEnv: {},
	}
	for key := range c.envOverrides {
		overrideKeys[key] = struct{}{}
	}

	filteredEnv := make([]string, 0, len(os.Environ())+len(c.envOverrides)+1)
	for _, env := range os.Environ() {
		skip := false
		for key := range overrideKeys {
			if strings.HasPrefix(env, key+"=") {
				skip = true
				break
			}
		}
		if !skip {
			filteredEnv = append(filteredEnv, env)
		}
	}

	for key, value := range c.envOverrides {
		if strings.TrimSpace(value) == "" {
			continue
		}
		filteredEnv = append(filteredEnv, key+"="+value)
	}
	if _, hasAPIKeyOverride := c.envOverrides["ANTHROPIC_API_KEY"]; !hasAPIKeyOverride && c.apiKey != "" {
		filteredEnv = append(filteredEnv, "ANTHROPIC_API_KEY="+c.apiKey)
	}
	filteredEnv = append(filteredEnv, claudeCodeDisableAutoMemoryEnv+"=1")

	return filteredEnv
}

// WithMCPConfig sets the MCP configuration JSON string for the session.
func WithMCPConfig(config string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = config
	}
}

// WithClaudeCodeSettings sets the --settings flag to a JSON string or file path.
func WithClaudeCodeSettings(settings string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeySettings] = settings
	}
}

// WithDangerouslySkipPermissions enables the --dangerously-skip-permissions flag.
// CAUTION: This allows the agent to execute any tool without user confirmation.
func WithDangerouslySkipPermissions() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions] = true
	}
}

// WithClaudeCodeTools sets the --tools flag to whitelist specific tools.
// Note: Core tools (Bash, Read, Write, etc.) may persist even if not listed.
// Use "" to disable optional tools (like WebSearch) if desired.
func WithClaudeCodeTools(tools string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyTools] = tools
	}
}

// WithAllowedTools sets the --allowed-tools flag to whitelist specific tools
// from requiring permission confirmation.
// Example: "mcp__mcpbridge__*" to allow all tools from the mcpbridge server.
func WithAllowedTools(tools string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAllowedTools] = tools
	}
}

// WithMaxTurns sets the --max-turns flag to limit the number of agentic turns.
// Claude Code exits with an error when the limit is reached.
func WithMaxTurns(maxTurns int) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMaxTurns] = maxTurns
	}
}

// WithEffort sets the --effort flag for the Claude Code CLI session.
// Values: "low", "medium", "high", "max"
func WithEffort(level string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyEffort] = level
	}
}

// WithResumeSessionID sets the --resume flag with a session ID so the Claude Code CLI
// resumes an existing session instead of starting a new one.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithInteractiveSessionID links an interactive Claude Code run to the owning
// application session so follow-up user input can be sent directly to the TUI.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithWorkingDir sets the directory used to launch the Claude Code CLI process.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWorkingDir] = dir
	}
}

// WithPersistentInteractiveSession keeps the tmux-backed Claude Code TUI alive
// across completed turns. It should only be used for interactive chat sessions;
// deterministic workflow runs should keep the default per-turn lifecycle.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithWriteProjectInstructionFile controls whether the adapter ALSO writes
// the per-session system prompt to <workingDir>/CLAUDE.md (Claude Code's
// project-instructions convention), in addition to the --system-prompt-file
// flag injection. ON by default; pass false to opt out.
//
// Useful because (a) the prompt is visible inside the workspace for
// debugging and transparency, and (b) downstream tooling (other agents,
// IDE plugins) that reads CLAUDE.md sees the same instructions. The
// adapter byte-restores any pre-existing operator CLAUDE.md on session
// teardown.
//
// When this flag is on AND WithMCPConfig was also set, the adapter
// ALSO projects the MCP servers JSON into <workingDir>/.mcp.json
// (Claude Code's project-scoped MCP convention), also with byte-restore.
//
// Risk caveat: CLAUDE.md and .mcp.json are single-file conventions. If
// the orchestrator process crashes between write and cleanup, the
// operator's prior content is destroyed. Pass false to disable for repos
// where this trade-off is unacceptable.
func WithWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile] = enabled
	}
}

// WithRestoreProjectFiles controls whether projected workspace artifacts
// (CLAUDE.md, .mcp.json) preserve the operator's pre-existing content
// across a session. OFF by default: each run writes a fresh artifact and
// removes it on cleanup, never restoring whatever was there before — so
// the workspace always reflects the latest orchestrator output. Pass true
// to opt back into the legacy behavior where any pre-existing operator
// file is byte-restored on session teardown.
func WithRestoreProjectFiles(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyRestoreProjectFiles] = enabled
	}
}

// WithProjectInstructionOnly makes the adapter inject the per-session system
// prompt SOLELY via <workingDir>/CLAUDE.md and skip the --system-prompt-file
// flag. OFF by default. Claude Code auto-loads CLAUDE.md as project
// instructions, so the prompt is still applied — but only once, avoiding the
// doubled system prompt that results from passing the same bytes through both
// --system-prompt-file and CLAUDE.md.
//
// Requires the CLAUDE.md projection to be active (it is, by default; see
// WithWriteProjectInstructionFile) and a non-empty working dir. If the
// projection is disabled or the write fails, the adapter falls back to
// --system-prompt-file so the prompt is never silently dropped.
func WithProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectInstructionOnly] = enabled
	}
}

func ensureMetadata(opts *llmtypes.CallOptions) {
	if opts.Metadata == nil {
		opts.Metadata = &llmtypes.Metadata{Custom: make(map[string]interface{})}
	}
	if opts.Metadata.Custom == nil {
		opts.Metadata.Custom = make(map[string]interface{})
	}
}

// GenerateContent generates content using the Claude Code CLI.
func (c *ClaudeCodeAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	// 0. Check for 'claude' binary
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claude cli not found in PATH. Please install it first (npm install -g @anthropics/claude-code)")
	}

	// Parse options
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}
	workingDir := claudeWorkingDirFromOptions(opts)

	toolCount := 0
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if tools, ok := opts.Metadata.Custom[MetadataKeyTools].(string); ok && tools != "" {
			toolCount = strings.Count(tools, ",") + 1
		}
	}

	return llmtypes.WithObservability(ctx, llmtypes.ObservabilityConfig{
		Provider:     c.providerName,
		Model:        c.modelID,
		Opts:         opts,
		MessageCount: len(messages),
		Messages:     messages,
		HeaderLine:   fmt.Sprintf("claude --print --model %s (msgs=%d)", c.modelID, len(messages)),
		RequestMetaExtra: map[string]interface{}{
			"transport":   "structured_cli",
			"working_dir": workingDir,
			"tool_count":  toolCount,
		},
	}, func(sink *llmtypes.StreamSink) (*llmtypes.ContentResponse, error) {
		return c.generateContentInner(ctx, opts, messages, workingDir, sink.Term, sink.Inspector)
	})
}

// generateContentInner is the body of GenerateContent — kept as a
// separate method so the public entry point is just the
// WithObservability wrapper. All per-stream emissions live here.
func (c *ClaudeCodeAdapter) generateContentInner(ctx context.Context, opts *llmtypes.CallOptions, messages []llmtypes.MessageContent, workingDir string, term *llmtypes.SyntheticTerminal, inspector *llmtypes.InspectorEmitter) (*llmtypes.ContentResponse, error) {
	_ = inspector // reserved for future per-event emissions

	// Project attached skills into .claude/skills/ before launching
	// the CLI so Claude Code discovers them natively at startup.
	// Best-effort: the listing is also in the system prompt via
	// mcpagent.ensureSystemPrompt, so a projection failure degrades
	// gracefully (agent loses the on-disk full body but still sees the
	// listing).
	if workingDir != "" {
		if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
			_ = c.ProjectSkills(workingDir, skills)
		}
	}

	// 1. Prepare Command Arguments
	// Newer Claude Code CLIs (>=2.x) require --verbose when --print is combined
	// with --output-format=stream-json; without it the CLI exits 1 with
	// "When using --print, --output-format=stream-json requires --verbose".
	args := []string{"-p", "--verbose", "--output-format", "stream-json", "--input-format", "stream-json", "--include-partial-messages"}

	// Pass --model only when the configured model is a real Claude CLI model ID.
	if c.shouldPassModelFlag() {
		args = append(args, "--model", c.modelID)
	}

	// Extract system prompt
	var systemPrompts []string
	var convoMessages []llmtypes.MessageContent

	for _, msg := range messages {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			// Extract text from system message parts
			for _, part := range msg.Parts {
				if textPart, ok := part.(llmtypes.TextContent); ok {
					systemPrompts = append(systemPrompts, textPart.Text)
				}
			}
		} else {
			convoMessages = append(convoMessages, msg)
		}
	}

	joinedSystemPrompt := strings.Join(systemPrompts, "\n\n")

	// Project the system prompt into <workingDir>/CLAUDE.md (Claude
	// Code's project-instructions convention) with byte-restore on
	// cleanup. ON by default; mirrors the interactive adapter behavior
	// so structured (non-interactive) generateContent calls also drop
	// the per-session system prompt into the workspace. Best-effort:
	// write failures must not block GenerateContent.
	//
	// projectedToClaudeMd records whether the write succeeded so
	// project-instruction-only mode can skip the --append-system-prompt
	// injection below and avoid a doubled system prompt. On failure it stays
	// false and the --append-system-prompt fallback fires.
	projectedToClaudeMd := false
	if writeProjectInstructionFromOptions(opts) && workingDir != "" && strings.TrimSpace(joinedSystemPrompt) != "" {
		if rulePath, werr := writeClaudeCodeProjectInstructionFile(workingDir, joinedSystemPrompt, restoreProjectFilesFromOptions(opts)); werr != nil {
			c.logger.Errorf("claude code: project CLAUDE.md write failed (best-effort): %v", werr)
		} else if rulePath != "" {
			defer removeFiles([]string{rulePath})
			projectedToClaudeMd = true
		}
	}

	// Inject the system prompt via --append-system-prompt UNLESS the caller
	// opted into project-instruction-only mode AND the CLAUDE.md projection
	// succeeded — in which case CLAUDE.md is the sole carrier (no doubled
	// prompt). If the projection was skipped or failed, projectedToClaudeMd is
	// false and we still pass --append-system-prompt so the prompt is applied.
	if len(systemPrompts) > 0 && !(projectInstructionOnlyFromOptions(opts) && projectedToClaudeMd) {
		args = append(args, "--append-system-prompt", joinedSystemPrompt)
	}

	// Handle JSON Schema for structured output
	if opts.JSONSchema != nil && opts.JSONSchema.Schema != nil {
		schemaBytes, err := json.Marshal(opts.JSONSchema.Schema)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON schema: %w", err)
		}
		args = append(args, "--json-schema", string(schemaBytes))
	}

	// Handle Custom Options
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mcpConfig, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && mcpConfig != "" {
			args = append(args, "--mcp-config", mcpConfig, "--strict-mcp-config")
		}
		if settings, ok := opts.Metadata.Custom[MetadataKeySettings].(string); ok && settings != "" {
			args = append(args, "--settings", settings)
		}
		if skip, ok := opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions].(bool); ok && skip {
			args = append(args, "--dangerously-skip-permissions")
		}
		if tools, ok := opts.Metadata.Custom[MetadataKeyTools].(string); ok {
			args = append(args, "--tools", tools)
		}
		if allowedTools, ok := opts.Metadata.Custom[MetadataKeyAllowedTools].(string); ok && allowedTools != "" {
			args = append(args, "--allowed-tools", allowedTools)
		}
		if maxTurns, ok := opts.Metadata.Custom[MetadataKeyMaxTurns].(int); ok && maxTurns > 0 {
			args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
		}
		if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && resumeID != "" {
			args = append(args, "--resume", resumeID)
		}
		if effort, ok := opts.Metadata.Custom[MetadataKeyEffort].(string); ok && effort != "" {
			args = append(args, "--effort", effort)
		}
	}

	// StreamChan will be closed manually before return (not via defer)
	// to allow the retry logic to stream additional chunks if needed

	// Check if we're resuming an existing session
	resumeID := ""
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if rid, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
			resumeID = rid
		}
	}

	// 2. Build Stream-JSON Input
	var inputStream bytes.Buffer
	encoder := json.NewEncoder(&inputStream)

	if resumeID != "" {
		// Resuming: only send the last user message (CLI has full history internally)
		for i := len(convoMessages) - 1; i >= 0; i-- {
			if convoMessages[i].Role == llmtypes.ChatMessageTypeHuman {
				jsonMsg, err := convertMessageToStreamJSON(convoMessages[i])
				if err != nil {
					c.logger.Errorf("Failed to convert message to stream-json: %v", err)
					return nil, fmt.Errorf("failed to convert message: %w", err)
				}
				if err := encoder.Encode(jsonMsg); err != nil {
					return nil, fmt.Errorf("failed to encode message json: %w", err)
				}
				break
			}
		}
	} else {
		// First turn: send full history as before
		for _, msg := range convoMessages {
			jsonMsg, err := convertMessageToStreamJSON(msg)
			if err != nil {
				c.logger.Errorf("Failed to convert message to stream-json: %v", err)
				return nil, fmt.Errorf("failed to convert message: %w", err)
			}
			if err := encoder.Encode(jsonMsg); err != nil {
				return nil, fmt.Errorf("failed to encode message json: %w", err)
			}
		}
	}

	// 3. Execute Command
	c.logger.Infof("Executing Claude Code CLI: claude %v", args)
	c.logger.Debugf("Input stream: %s", inputStream.String())
	cmd := exec.CommandContext(ctx, "claude", args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Stdin = &inputStream
	// Run in its own process group so we can kill the entire tree (CLI + children) on cancel
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Env = c.buildCommandEnv()

	// Use Pipe for stdout to parse as a stream
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr so we can log it (helps debug permission prompts / errors)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start claude cli: %w", err)
	}

	// 4. Parse Streamed Output
	var finalResponse *llmtypes.ContentResponse
	var maxTurnsSessionID string
	decoder := json.NewDecoder(stdoutPipe)

	// Count AI messages in history to skip them during playback streaming
	// When resuming, we only sent the new user message so there's no history to skip
	aiHistoryCount := 0
	if resumeID == "" {
		for _, msg := range convoMessages {
			if msg.Role == llmtypes.ChatMessageTypeAI {
				aiHistoryCount++
			}
		}
	}
	aiSeenCount := 0

	// Closed by the decode goroutine when stdout reaches EOF (process exited
	// AND scanner has drained). Used both by the main goroutine's lifecycle
	// select below and by procshutdown.Graceful so the shutdown helper can
	// observe that the CLI has actually gone away.
	decodeDone := make(chan struct{})

	var currentToolName string
	var currentToolID string
	var currentToolInput strings.Builder
	var inToolBlock bool
	hasStreamEvents := false
	var resultIsError bool     // tracks is_error from the CLI result event
	var resultErrorText string // the error text from the result when is_error=true
	// Buffer pending tool calls to match with tool_result for complete events
	pendingTools := make(map[string]*pendingToolCall)

	var capturedStructuredOutput string
	go func() {
		c.logger.Infof("Starting stream decode loop...")
		for decoder.More() {
			var raw map[string]interface{}
			if err := decoder.Decode(&raw); err != nil {
				c.logger.Errorf("Failed to decode stream-json object: %v", err)
				break
			}
			// c.logger.Infof("Decoded raw stream object of type: %v, raw: %+v", raw["type"], raw)

			msgType, _ := raw["type"].(string)
			switch msgType {
			case "stream_event":
				hasStreamEvents = true
				event, _ := raw["event"].(map[string]interface{})
				if event == nil {
					continue
				}
				eventType, _ := event["type"].(string)

				switch eventType {
				case "content_block_start":
					cb, _ := event["content_block"].(map[string]interface{})
					if cb == nil {
						break
					}
					cbType, _ := cb["type"].(string)
					if cbType == "tool_use" {
						currentToolName, _ = cb["name"].(string)
						currentToolID, _ = cb["id"].(string)
						currentToolInput.Reset()
						inToolBlock = true

						// Track start time for duration calculation
						pendingTools[currentToolID] = &pendingToolCall{
							toolName:  currentToolName,
							toolID:    currentToolID,
							startTime: time.Now(),
						}
					}

				case "content_block_delta":
					delta, _ := event["delta"].(map[string]interface{})
					if delta == nil {
						break
					}
					deltaType, _ := delta["type"].(string)
					if deltaType == "text_delta" {
						if txt, ok := delta["text"].(string); ok && txt != "" && !inToolBlock {
							if opts.StreamChan != nil {
								opts.StreamChan <- llmtypes.StreamChunk{
									Type:    llmtypes.StreamChunkTypeContent,
									Content: txt,
								}
							}
							term.AssistantText(txt)
						}
					} else if deltaType == "input_json_delta" {
						if partialJSON, ok := delta["partial_json"].(string); ok {
							currentToolInput.WriteString(partialJSON)
						}
					}

				case "content_block_stop":
					if inToolBlock {
						toolArgs := currentToolInput.String()
						// If this is a StructuredOutput tool call, capture its arguments
						if currentToolName == "StructuredOutput" {
							c.logger.Infof("Captured StructuredOutput tool call: %s", toolArgs)
							capturedStructuredOutput = toolArgs
						}

						// Emit ToolCallStart now that we have the full arguments
						if opts.StreamChan != nil {
							opts.StreamChan <- llmtypes.StreamChunk{
								Type:       llmtypes.StreamChunkTypeToolCallStart,
								ToolName:   currentToolName,
								ToolCallID: currentToolID,
								ToolArgs:   toolArgs,
							}
						}
						term.ToolStart(currentToolName, toolArgs)

						// Save args to pending tool (don't emit ToolCallEnd yet — wait for tool_result)
						if pt, ok := pendingTools[currentToolID]; ok {
							pt.toolArgs = toolArgs
						}
						inToolBlock = false
						currentToolName = ""
						currentToolID = ""
						currentToolInput.Reset()
					}
				}

			case "user":
				// Parse tool_result messages to complete pending tool calls
				if msg, ok := raw["message"].(map[string]interface{}); ok {
					if content, ok := msg["content"].([]interface{}); ok {
						for _, cPart := range content {
							cp, ok := cPart.(map[string]interface{})
							if !ok {
								continue
							}
							if cp["type"] != "tool_result" {
								continue
							}
							toolUseID, _ := cp["tool_use_id"].(string)
							if toolUseID == "" {
								continue
							}
							// content can be a plain string OR an array of content blocks
							// e.g. [{"type":"text","text":"..."}]
							var resultContent string
							switch v := cp["content"].(type) {
							case string:
								resultContent = v
							case []interface{}:
								// Extract text from content blocks
								var parts []string
								for _, block := range v {
									if bm, ok := block.(map[string]interface{}); ok {
										if txt, ok := bm["text"].(string); ok {
											parts = append(parts, txt)
										}
									}
								}
								resultContent = strings.Join(parts, "")
							}

							if pt, ok := pendingTools[toolUseID]; ok {
								duration := time.Since(pt.startTime)
								if opts.StreamChan != nil {
									opts.StreamChan <- llmtypes.StreamChunk{
										Type:         llmtypes.StreamChunkTypeToolCallEnd,
										ToolName:     pt.toolName,
										ToolCallID:   pt.toolID,
										ToolArgs:     pt.toolArgs,
										ToolResult:   resultContent,
										ToolDuration: duration,
									}
								}
								term.ToolEnd(pt.toolName, resultContent, duration)
								delete(pendingTools, toolUseID)
							}
						}
					}
				}

			case "assistant":
				aiSeenCount++
				// Only stream tokens if we've passed all historical AI messages
				if aiSeenCount <= aiHistoryCount {
					continue
				}

				// If we are getting stream_events, we don't need to parse the consolidated assistant message for text streaming
				if hasStreamEvents {
					continue
				}

				// Handle assistant message (could be a chunk or a complete message)
				if msg, ok := raw["message"].(map[string]interface{}); ok {
					if content, ok := msg["content"].([]interface{}); ok {
						for _, cPart := range content {
							if cp, ok := cPart.(map[string]interface{}); ok {
								if txt, ok := cp["text"].(string); ok && txt != "" {
									// If user requested streaming, send chunk
									if opts.StreamChan != nil {
										opts.StreamChan <- llmtypes.StreamChunk{
											Type:    llmtypes.StreamChunkTypeContent,
											Content: txt,
										}
									}
								}
							}
						}
					}
				}
			case "result":
				// End-of-turn teardown per the structured-CLI shutdown contract
				// (docs/coding_sdk_structured_contract.md §9): SIGTERM → 5s
				// grace for ~/.claude session flush → SIGKILL. Runs as a
				// goroutine so this decode loop keeps draining stdout while
				// shutdown is in progress.
				go procshutdown.Graceful(cmd, decodeDone, c.logger)
				// Flush any remaining pending tool calls that never got a tool_result
				for _, pt := range pendingTools {
					if opts.StreamChan != nil {
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:         llmtypes.StreamChunkTypeToolCallEnd,
							ToolName:     pt.toolName,
							ToolCallID:   pt.toolID,
							ToolArgs:     pt.toolArgs,
							ToolDuration: time.Since(pt.startTime),
						}
					}
				}
				pendingTools = make(map[string]*pendingToolCall)

				// Parse the final result summary
				var claudeResp ClaudeCodeResponse
				jsonBytes, _ := json.Marshal(raw)
				if err := json.Unmarshal(jsonBytes, &claudeResp); err == nil {
					// When --json-schema is used, the result is in a tool call (StructuredOutput)
					// but the result event summary might have it empty or containing only a generic message.
					// If resp.Result is empty but we captured structured output, use it.
					if claudeResp.Result == "" && capturedStructuredOutput != "" {
						claudeResp.Result = capturedStructuredOutput
					}

					finalResponse, _ = c.mapResponseToContentResponse(&claudeResp)
					// Detect max turns error: subtype indicates limit was hit and result is empty
					if claudeResp.Subtype == "error_max_turns" && claudeResp.Result == "" {
						maxTurnsSessionID = claudeResp.SessionID
						c.logger.Infof("Detected error_max_turns with empty result, sessionID=%s", maxTurnsSessionID)
					}
					// Detect CLI-reported errors (e.g., API 500, auth failures)
					if claudeResp.IsError {
						resultIsError = true
						resultErrorText = claudeResp.Result
						c.logger.Errorf("Claude Code CLI reported is_error=true, subtype=%q, result=%q", claudeResp.Subtype, claudeResp.Result)
					}
				}
			}
		}
		close(decodeDone)
	}()

	// Wait for command completion or context cancellation
	c.logger.Infof("[CLI_LIFECYCLE] Waiting for CLI process to complete (pid=%d)", cmd.Process.Pid)
	var cmdErr error
	select {
	case <-ctx.Done():
		c.logger.Errorf("[CLI_LIFECYCLE] Context cancelled/timed out: %v (pid=%d)", ctx.Err(), cmd.Process.Pid)
		killProcessGroup(cmd)
		cmdErr = ctx.Err()
	case <-decodeDone:
		c.logger.Infof("[CLI_LIFECYCLE] CLI stdout closed, waiting for process exit (pid=%d)", cmd.Process.Pid)
		cmdErr = cmd.Wait()
		c.logger.Infof("[CLI_LIFECYCLE] CLI process exited (pid=%d, err=%v)", cmd.Process.Pid, cmdErr)
	}

	// Log stderr output from Claude CLI (captures permission prompts, errors, debug info)
	if stderrOutput := stderrBuf.String(); stderrOutput != "" {
		c.logger.Infof("Claude Code CLI stderr:\n%s", stderrOutput)
	}

	if cmdErr != nil {
		c.logger.Errorf("Claude Code CLI failed with error: %v. stderr: %s", cmdErr, stderrBuf.String())
		// If we already have a final response (sometimes CLI errors out after finishing), we might still want to return it
		if finalResponse == nil {
			// opts.StreamChan close is owned by WithObservability.
			return nil, fmt.Errorf("claude cli execution failed: %w", cmdErr)
		}
	}

	if finalResponse == nil {
		// opts.StreamChan close is owned by WithObservability.
		return nil, fmt.Errorf("failed to receive final result from claude cli")
	}

	// Surface CLI-reported errors as Go errors so upstream retry logic can handle them.
	// This catches API errors (500, 502, 503, etc.) that the CLI reports via is_error=true.
	if resultIsError && resultErrorText != "" {
		// opts.StreamChan close is owned by WithObservability.
		return nil, fmt.Errorf("claude cli error: %s", resultErrorText)
	}

	// If max turns was hit with an empty result, retry with a finalization prompt
	if maxTurnsSessionID != "" {
		c.logger.Infof("Max turns reached, retrying with finalization prompt (sessionID=%s)", maxTurnsSessionID)
		retryResp, retryErr := c.retryForFinalAnswer(ctx, maxTurnsSessionID, opts)
		if retryErr != nil {
			c.logger.Errorf("Retry for final answer failed: %v", retryErr)
		} else if retryResp != nil && len(retryResp.Choices) > 0 && retryResp.Choices[0].Content != "" {
			c.logger.Infof("Retry produced final answer (%d chars)", len(retryResp.Choices[0].Content))
			finalResponse = retryResp
		} else {
			c.logger.Infof("Retry produced empty result, using original response")
		}
	}

	// opts.StreamChan close is owned by WithObservability.

	return finalResponse, nil
}

// SearchWeb uses Claude Code's native WebSearch tool and returns the final text response.
func (c *ClaudeCodeAdapter) SearchWeb(ctx context.Context, query string, options ...llmtypes.CallOption) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	searchPrompt := "Use WebSearch to answer the following query.\n\n" + query
	searchOptions := append([]llmtypes.CallOption{}, options...)
	searchOptions = append(searchOptions, WithClaudeCodeTools("WebSearch"))
	searchOptions = append(searchOptions, WithAllowedTools("WebSearch"))

	resp, err := c.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: searchPrompt},
			},
		},
	}, searchOptions...)
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("claude code web search returned no response")
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		return "", fmt.Errorf("claude code web search returned empty response")
	}
	return content, nil
}

// killProcessGroup kills the entire process group of cmd to ensure all child
// processes (HTTP workers, shell children, etc.) are terminated, not just the
// direct subprocess.  Falls back to cmd.Process.Kill() if the process group
// cannot be determined.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		// Negative pgid kills all processes in the group
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	} else {
		_ = cmd.Process.Kill()
	}
}

// GetModelID returns the model ID.
func (c *ClaudeCodeAdapter) GetModelID() string {
	return c.modelID
}

// GetModelMetadata returns metadata for the model.
func (c *ClaudeCodeAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = c.modelID
	}

	providerName := c.providerName
	if providerName == "" {
		providerName = "claude-code"
	}

	// Default context window for Claude models used via Claude Code CLI.
	// The actual context window is reported per-call in modelUsage and used
	// to update the agent's context window tracking dynamically.
	switch modelID {
	case "claude-fable-5":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              providerName,
			ModelName:             "Claude Fable 5",
			ContextWindow:         1000000,
			InputCostPer1MTokens:  10.00,
			OutputCostPer1MTokens: 50.00,
		}, nil
	case "claude-opus-4-7":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              providerName,
			ModelName:             "Claude Opus 4.7",
			ContextWindow:         200000,
			InputCostPer1MTokens:  5.00,
			OutputCostPer1MTokens: 25.00,
		}, nil
	case "claude-opus-4-6":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              providerName,
			ModelName:             "Claude Opus 4.6",
			ContextWindow:         200000,
			InputCostPer1MTokens:  5.00,
			OutputCostPer1MTokens: 25.00,
		}, nil
	case "claude-sonnet-5":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              providerName,
			ModelName:             "Claude Sonnet 5",
			ContextWindow:         200000,
			InputCostPer1MTokens:  3.00,
			OutputCostPer1MTokens: 15.00,
		}, nil
	case "claude-sonnet-4-6":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              providerName,
			ModelName:             "Claude Sonnet 4.6",
			ContextWindow:         200000,
			InputCostPer1MTokens:  3.00,
			OutputCostPer1MTokens: 15.00,
		}, nil
	case "claude-haiku-4-5-20251001":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              providerName,
			ModelName:             "Claude Haiku 4.5",
			ContextWindow:         200000,
			InputCostPer1MTokens:  1.00,
			OutputCostPer1MTokens: 5.00,
		}, nil
	default:
		return &llmtypes.ModelMetadata{
			ModelID:       modelID,
			Provider:      providerName,
			ModelName:     "Claude Code CLI",
			ContextWindow: 200000, // Default for Claude Sonnet/Opus models
		}, nil
	}
}

// --- Helper Functions & Structs ---

type StreamJSONMessage struct {
	Type    string          `json:"type"`
	Message InternalMessage `json:"message"`
}

type InternalMessage struct {
	Role    string        `json:"role"`
	Content []interface{} `json:"content"`
}

type TextContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ImageContentBlock struct {
	Type   string           `json:"type"`
	Source ImageSourceBlock `json:"source"`
}

type ImageSourceBlock struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

func convertMessageToStreamJSON(msg llmtypes.MessageContent) (*StreamJSONMessage, error) {
	role := "user"
	if msg.Role == llmtypes.ChatMessageTypeAI {
		role = "assistant"
	}

	var content []interface{}
	for _, part := range msg.Parts {
		switch p := part.(type) {
		case llmtypes.TextContent:
			content = append(content, TextContentBlock{
				Type: "text",
				Text: p.Text,
			})
		case llmtypes.ImageContent:
			block := ImageContentBlock{Type: "image"}
			if p.SourceType == "url" {
				block.Source = ImageSourceBlock{
					Type: "url",
					URL:  p.Data,
				}
			} else {
				block.Source = ImageSourceBlock{
					Type:      "base64",
					MediaType: p.MediaType,
					Data:      p.Data,
				}
			}
			content = append(content, block)
		}
	}

	return &StreamJSONMessage{
		Type: role,
		Message: InternalMessage{
			Role:    role,
			Content: content,
		},
	}, nil
}

// ClaudeCodeResponse mirrors the JSON output from `claude -p --output-format json`
type ClaudeCodeResponse struct {
	Type              string                     `json:"type"`
	Subtype           string                     `json:"subtype,omitempty"`
	IsError           bool                       `json:"is_error,omitempty"`
	SessionID         string                     `json:"session_id"`
	Result            string                     `json:"result"`
	Usage             ClaudeUsage                `json:"usage"`
	TotalCostUSD      float64                    `json:"total_cost_usd"`
	DurationMs        float64                    `json:"duration_ms"`
	DurationAPIMs     float64                    `json:"duration_api_ms"`
	NumTurns          int                        `json:"num_turns"`
	ModelUsage        map[string]ModelUsageEntry `json:"modelUsage,omitempty"`
	PermissionDenials []PermissionDenial         `json:"permission_denials,omitempty"`
}

type ClaudeUsage struct {
	InputTokens              int            `json:"input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens"`
	ServiceTier              string         `json:"service_tier,omitempty"`
	ServerToolUse            *ServerToolUse `json:"server_tool_use,omitempty"`
}

type ServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests"`
	WebFetchRequests  int `json:"web_fetch_requests"`
}

type ModelUsageEntry struct {
	InputTokens          int     `json:"inputTokens"`
	OutputTokens         int     `json:"outputTokens"`
	CacheReadInputTokens int     `json:"cacheReadInputTokens"`
	CacheCreationTokens  int     `json:"cacheCreationInputTokens"`
	WebSearchRequests    int     `json:"webSearchRequests"`
	CostUSD              float64 `json:"costUSD"`
	ContextWindow        int     `json:"contextWindow"`
	MaxOutputTokens      int     `json:"maxOutputTokens"`
}

type PermissionDenial struct {
	ToolName  string      `json:"tool_name"`
	ToolUseID string      `json:"tool_use_id"`
	ToolInput interface{} `json:"tool_input"`
}

func (c *ClaudeCodeAdapter) mapResponseToContentResponse(resp *ClaudeCodeResponse) (*llmtypes.ContentResponse, error) {
	// Claude Code CLI reports CUMULATIVE usage across all internal turns.
	// input_tokens = cumulative non-cached tokens, cache_read = cumulative cached tokens.
	// For context window tracking, use input_tokens + cache_read as the total prompt size
	// (this is cumulative, but allows meaningful percentage tracking).
	cacheReadTokens := resp.Usage.CacheReadInputTokens
	cacheCreationTokens := resp.Usage.CacheCreationInputTokens
	totalInputTokens := resp.Usage.InputTokens + cacheReadTokens + cacheCreationTokens
	totalTokens := totalInputTokens + resp.Usage.OutputTokens

	// Map Usage
	usage := &llmtypes.Usage{
		InputTokens:  totalInputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		TotalTokens:  totalTokens,
		CacheTokens:  &resp.Usage.CacheReadInputTokens,
	}

	// Map GenerationInfo
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:         &totalInputTokens,
		OutputTokens:        &resp.Usage.OutputTokens,
		TotalTokens:         &totalTokens,
		CachedContentTokens: &cacheReadTokens,
		Additional: map[string]interface{}{
			"cost_usd":               resp.TotalCostUSD,
			"claude_code_session_id": resp.SessionID,
			"RawInputTokens":         resp.Usage.InputTokens,
		},
	}
	if cacheReadTokens > 0 {
		genInfo.Additional["CacheReadInputTokens"] = cacheReadTokens
		// Also under the raw Anthropic key the cost ledger reads.
		// Old PascalCase key is kept for callers that already consume it.
		genInfo.Additional["cache_read_input_tokens"] = cacheReadTokens
	}
	if cacheCreationTokens > 0 {
		genInfo.Additional["CacheCreationInputTokens"] = cacheCreationTokens
		genInfo.Additional["cache_creation_input_tokens"] = cacheCreationTokens
	}

	// Add duration and turn count from result event
	if resp.DurationMs > 0 {
		genInfo.Additional["claude_code_duration_ms"] = resp.DurationMs
	}
	if resp.DurationAPIMs > 0 {
		genInfo.Additional["claude_code_duration_api_ms"] = resp.DurationAPIMs
	}
	if resp.NumTurns > 0 {
		genInfo.Additional["claude_code_num_turns"] = resp.NumTurns
	}

	// Add per-model usage breakdown (includes resolved model name, context window, cost)
	if len(resp.ModelUsage) > 0 {
		genInfo.Additional["claude_code_model_usage"] = resp.ModelUsage
		// Extract the resolved model name and context window from modelUsage
		for modelName, modelEntry := range resp.ModelUsage {
			genInfo.Additional["claude_code_model"] = modelName
			if modelEntry.ContextWindow > 0 {
				genInfo.Additional["model_context_window"] = modelEntry.ContextWindow
			}
			break
		}
	}

	// Add service tier
	if resp.Usage.ServiceTier != "" {
		genInfo.Additional["claude_code_service_tier"] = resp.Usage.ServiceTier
	}

	// Add server tool use counts (web search, web fetch)
	if resp.Usage.ServerToolUse != nil {
		if resp.Usage.ServerToolUse.WebSearchRequests > 0 {
			genInfo.Additional["claude_code_web_search_requests"] = resp.Usage.ServerToolUse.WebSearchRequests
		}
		if resp.Usage.ServerToolUse.WebFetchRequests > 0 {
			genInfo.Additional["claude_code_web_fetch_requests"] = resp.Usage.ServerToolUse.WebFetchRequests
		}
	}

	// Handle Permission Denials
	if len(resp.PermissionDenials) > 0 {
		genInfo.Additional["permission_denials"] = resp.PermissionDenials
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        resp.Result,
				GenerationInfo: genInfo,
			},
		},
		Usage: usage,
	}, nil
}

// retryForFinalAnswer resumes a Claude Code session that hit max turns
// and asks it to provide a final summary in a single turn.
func (c *ClaudeCodeAdapter) retryForFinalAnswer(
	ctx context.Context,
	sessionID string,
	opts *llmtypes.CallOptions,
) (*llmtypes.ContentResponse, error) {
	// Build minimal arg list: resume the session with 1 turn
	args := []string{
		"-p",
		"--verbose", // required by Claude Code >=2.x with --print + stream-json
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--resume", sessionID,
		"--max-turns", "1",
	}

	// Carry over model override from original invocation.
	if c.shouldPassModelFlag() {
		args = append(args, "--model", c.modelID)
	}

	// Carry over MCP config, settings, and permissions from original opts
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mcpConfig, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && mcpConfig != "" {
			args = append(args, "--mcp-config", mcpConfig, "--strict-mcp-config")
		}
		if settings, ok := opts.Metadata.Custom[MetadataKeySettings].(string); ok && settings != "" {
			args = append(args, "--settings", settings)
		}
		if skip, ok := opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions].(bool); ok && skip {
			args = append(args, "--dangerously-skip-permissions")
		}
	}
	// Note: --append-system-prompt is NOT passed — the session already has it

	// Prepare the finalization prompt as stdin
	var inputStream bytes.Buffer
	encoder := json.NewEncoder(&inputStream)
	finalizationMsg := StreamJSONMessage{
		Type: "user",
		Message: InternalMessage{
			Role: "user",
			Content: []interface{}{
				TextContentBlock{
					Type: "text",
					Text: "You have run out of turns. Please provide your final answer now based on what you have accomplished so far. Summarize results, findings, and any remaining work.",
				},
			},
		},
	}
	if err := encoder.Encode(finalizationMsg); err != nil {
		return nil, fmt.Errorf("failed to encode finalization message: %w", err)
	}

	c.logger.Infof("Retry: executing Claude Code CLI: claude %v", args)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = &inputStream
	// Run in its own process group so we can kill the entire tree on cancel
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Env = c.buildCommandEnv()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("retry: failed to create stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("retry: failed to start claude cli: %w", err)
	}

	// Simplified decode loop: only care about result event and text streaming
	var retryResponse *llmtypes.ContentResponse
	decoder := json.NewDecoder(stdoutPipe)
	// See main path: closed (not sent) so procshutdown.Graceful and the main
	// goroutine can both observe scanner EOF.
	decodeDone := make(chan struct{})

	go func() {
		for decoder.More() {
			var raw map[string]interface{}
			if err := decoder.Decode(&raw); err != nil {
				c.logger.Errorf("Retry: failed to decode stream-json: %v", err)
				break
			}

			msgType, _ := raw["type"].(string)
			switch msgType {
			case "stream_event":
				// Stream text chunks to StreamChan if still open
				event, _ := raw["event"].(map[string]interface{})
				if event == nil {
					continue
				}
				eventType, _ := event["type"].(string)
				if eventType == "content_block_delta" {
					delta, _ := event["delta"].(map[string]interface{})
					if delta == nil {
						continue
					}
					if deltaType, _ := delta["type"].(string); deltaType == "text_delta" {
						if txt, ok := delta["text"].(string); ok && txt != "" {
							if opts.StreamChan != nil {
								opts.StreamChan <- llmtypes.StreamChunk{
									Type:    llmtypes.StreamChunkTypeContent,
									Content: txt,
								}
							}
						}
					}
				}

			case "assistant":
				// Fallback streaming for non-stream_event mode
				if msg, ok := raw["message"].(map[string]interface{}); ok {
					if content, ok := msg["content"].([]interface{}); ok {
						for _, cPart := range content {
							if cp, ok := cPart.(map[string]interface{}); ok {
								if txt, ok := cp["text"].(string); ok && txt != "" {
									if opts.StreamChan != nil {
										opts.StreamChan <- llmtypes.StreamChunk{
											Type:    llmtypes.StreamChunkTypeContent,
											Content: txt,
										}
									}
								}
							}
						}
					}
				}

			case "result":
				// Retry path: same shutdown contract as the primary decode loop.
				go procshutdown.Graceful(cmd, decodeDone, c.logger)
				var claudeResp ClaudeCodeResponse
				jsonBytes, _ := json.Marshal(raw)
				if err := json.Unmarshal(jsonBytes, &claudeResp); err == nil {
					retryResponse, _ = c.mapResponseToContentResponse(&claudeResp)
				}
			}
		}
		close(decodeDone)
	}()

	// Wait for completion
	var cmdErr error
	select {
	case <-ctx.Done():
		killProcessGroup(cmd)
		cmdErr = ctx.Err()
	case <-decodeDone:
		cmdErr = cmd.Wait()
	}

	if stderrOutput := stderrBuf.String(); stderrOutput != "" {
		c.logger.Infof("Retry: Claude Code CLI stderr:\n%s", stderrOutput)
	}

	if cmdErr != nil {
		c.logger.Errorf("Retry: Claude Code CLI failed: %v", cmdErr)
		if retryResponse == nil {
			return nil, fmt.Errorf("retry: claude cli execution failed: %w", cmdErr)
		}
	}

	return retryResponse, nil
}
