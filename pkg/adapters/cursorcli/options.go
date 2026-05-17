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

// WithApproveMCPs enables Cursor Agent's --approve-mcps flag.
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

func ensureMetadata(opts *llmtypes.CallOptions) {
	if opts.Metadata == nil {
		opts.Metadata = &llmtypes.Metadata{Custom: make(map[string]interface{})}
	}
	if opts.Metadata.Custom == nil {
		opts.Metadata.Custom = make(map[string]interface{})
	}
}
