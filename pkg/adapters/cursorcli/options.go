package cursorcli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

const (
	MetadataKeyCursorModel           = "cursor_model"
	MetadataKeyResumeSessionID       = "cursor_resume_session_id"
	MetadataKeyForce                 = "cursor_force"
	MetadataKeySandbox               = "cursor_sandbox"
	MetadataKeyMode                  = "cursor_mode"
	MetadataKeyWorkingDir            = "cursor_working_dir"
	MetadataKeyProjectConfig         = "cursor_project_config"
	MetadataKeyMCPConfig             = "cursor_mcp_config"
	MetadataKeyApproveMCPs           = "cursor_approve_mcps"
	MetadataKeyHeaders               = "cursor_headers"
	MetadataKeyPluginDirs            = "cursor_plugin_dirs"
	MetadataKeyAutoApproveWebSearch  = "cursor_auto_approve_web_search"
	MetadataKeyInteractiveSessionID  = "cursor_interactive_session_id"
	MetadataKeyPersistentInteractive = "cursor_persistent_interactive"
	MetadataKeyDenyBuiltinTools      = "cursor_deny_builtin_tools"
)

// WithCursorModel sets the Cursor Agent CLI --model flag. Use "cursor-cli" or
// "auto" to let Cursor use its configured default without passing --model.
func WithCursorModel(model string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyCursorModel] = model
	}
}

// WithResumeSessionID resumes a native Cursor chat with --resume <sessionID>.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithForce enables Cursor Agent's --force flag.
func WithForce() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyForce] = true
	}
}

// WithSandbox sets Cursor Agent's --sandbox mode. Supported values are
// "enabled" and "disabled".
func WithSandbox(mode string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeySandbox] = mode
	}
}

// WithMode sets Cursor Agent's --mode flag. Supported values are "plan" and
// "ask"; leave empty for normal agent mode.
//
// DEPRECATED FOR "ask" — prefer WithDenyBuiltinTools(true) instead.
// Ask mode is a conversational stance that hard-refuses natural-language
// write requests with "Switch to Agent mode and ask…", which makes it
// unsuitable for any chat surface that needs writes. The orchestrator no
// longer uses ask mode anywhere; the bridge-only-tools intent is now
// achieved via cursor hooks (https://cursor.com/docs/hooks) installed by
// WithDenyBuiltinTools. WithMode is retained because callers may still
// want "plan" mode, and "ask" remains a valid cursor CLI flag.
func WithMode(mode string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMode] = mode
	}
}

// WithWorkingDir sets the Cursor Agent workspace/cwd for tmux launch.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWorkingDir] = dir
	}
}

// WithProjectConfig writes a temporary/restored .cursor/cli.json in the
// workspace before launching Cursor Agent.
func WithProjectConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectConfig] = configJSON
	}
}

// WithMCPConfig writes a temporary/restored .cursor/mcp.json in the workspace
// before launching Cursor Agent.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = configJSON
	}
}

// WithApproveMCPs enables Cursor Agent's --approve-mcps flag, which auto-
// accepts the "approve this MCP server?" TUI consent dialog so bridge tool
// calls do not stall waiting for a human operator in the cursor TUI.
//
// Always pair this with WithMCPConfig — without an MCP config there is
// nothing to approve, and the flag is a no-op.
func WithApproveMCPs() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyApproveMCPs] = true
	}
}

// WithHeaders appends Cursor Agent -H/--header values. Each entry must use the
// Cursor CLI format "Name: Value".
func WithHeaders(headers []string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyHeaders] = headers
	}
}

// WithPluginDirs appends Cursor Agent --plugin-dir values.
func WithPluginDirs(dirs []string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPluginDirs] = dirs
	}
}

// WithAutoApproveWebSearch allows the Cursor TUI's web-search approval prompt
// for a call that is already scoped to SearchWeb. It does not enable --force.
func WithAutoApproveWebSearch() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAutoApproveWebSearch] = true
	}
}

// WithInteractiveSessionID links a Cursor Agent tmux run to the owning
// application session so follow-up input can be sent directly to tmux.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession keeps the tmux-backed Cursor Agent alive
// across completed chat turns.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithDenyBuiltinTools installs a per-session .cursor/hooks.json that
// denies cursor's built-in Shell and Read tools via the beforeShellExecution
// and beforeReadFile events. Cursor will then route those actions through
// the MCP bridge (api-bridge.execute_shell_command / api-bridge.read_file)
// instead — provided the bridge MCP config is also installed via
// WithMCPConfig. Cleanup restores any pre-existing hooks.json on session
// teardown so the operator's own hooks aren't disturbed.
//
// This is the "hard lever" for bridge-only tool usage. The "soft lever" is
// to coach the model via system prompt; that has slow-failing edges where
// cursor falls back to built-in tools. The hook denies the call before it
// runs, so the model has no choice but to use the MCP bridge.
func WithDenyBuiltinTools(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyDenyBuiltinTools] = enabled
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
