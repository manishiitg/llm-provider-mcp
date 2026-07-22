package claudecode

import (
	"context"
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

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
	// MetadataKeyMCPReadyFile names a filesystem path that the session's MCP
	// server (e.g. the mcpagent bridge) creates once it has answered the CLI's
	// tools/list handshake — i.e. the moment the MCP tools are actually
	// connected and callable. On a FRESHLY created interactive session the
	// adapter waits (bounded) for this file to appear before sending the first
	// prompt, closing the cold-turn race where the model's first turn runs
	// before its tools have connected (Claude then fabricates tool calls as
	// text; Codex reports "tools unavailable"). The wait is skipped on reused
	// persistent sessions (already connected) and when the path is empty, and it
	// degrades to "proceed anyway" on timeout so it can never hang a turn. The
	// path MUST be unique per session (the writer allocates a fresh one each
	// launch) so a stale file from a prior session in the same workspace cannot
	// falsely satisfy the gate.
	MetadataKeyMCPReadyFile = "mcp_ready_file"
)

// ClaudeCodeAdapter implements the LLM interface for the Claude Code CLI.
type ClaudeCodeAdapter struct {
	modelID      string
	logger       interfaces.Logger
	providerName string
}

// NewClaudeCodeAdapter creates a new instance of the ClaudeCodeAdapter.
func NewClaudeCodeAdapter(apiKey string, modelID string, logger interfaces.Logger) *ClaudeCodeAdapter {
	return NewProviderClaudeCodeAdapter(apiKey, modelID, "claude-code", nil, logger)
}

// NewProviderClaudeCodeAdapter creates a Claude Code adapter with provider-specific
// environment overrides. This lets Claude-compatible providers reuse the same CLI path.
func NewProviderClaudeCodeAdapter(apiKey string, modelID string, providerName string, envOverrides map[string]string, logger interfaces.Logger) *ClaudeCodeAdapter {
	_ = apiKey
	_ = envOverrides
	return &ClaudeCodeAdapter{
		modelID:      modelID,
		logger:       logger,
		providerName: providerName,
	}
}

// WithMCPConfig sets the MCP configuration JSON string for the session.
func WithMCPConfig(config string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = config
	}
}

// WithMCPReadyFile sets the path the session's MCP server creates once it has
// answered the CLI's tools/list handshake. On a freshly created interactive
// session the adapter holds the first prompt until this file appears (bounded,
// degrades to proceed-anyway on timeout), so the model never runs its opening
// turn before its MCP tools have connected. See MetadataKeyMCPReadyFile.
func WithMCPReadyFile(path string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPReadyFile] = path
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

// GenerateContent generates content using Claude Code through the tmux
// transport. The old `claude -p` stream-json path is no longer selected by the
// public adapter; direct callers keep this type for API compatibility while
// execution is delegated to ClaudeCodeInteractiveAdapter.
func (c *ClaudeCodeAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	return NewClaudeCodeInteractiveAdapter(c.modelID, c.logger).GenerateContent(ctx, messages, options...)
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
	case "claude-opus-4-8":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              providerName,
			ModelName:             "Claude Opus 4.8",
			ContextWindow:         200000,
			InputCostPer1MTokens:  5.00,
			OutputCostPer1MTokens: 25.00,
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
