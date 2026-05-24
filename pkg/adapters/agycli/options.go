package agycli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

const (
	MetadataKeyWorkingDir            = "agy_working_dir"
	MetadataKeyResumeSessionID       = "agy_resume_session_id"
	MetadataKeyMCPConfig             = "agy_mcp_config"
	MetadataKeyBridgeOnlyTools       = "agy_bridge_only_tools"
	MetadataKeyAutoApproveWebSearch  = "agy_auto_approve_web_search"
	MetadataKeyInteractiveSessionID  = "agy_interactive_session_id"
	MetadataKeyPersistentInteractive = "agy_persistent_interactive"
)

// WithSandbox sets Antigravity CLI's --sandbox value.
func WithSandbox(mode string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom["agy_sandbox"] = mode
	}
}

// WithDangerouslySkipPermissions enables agy's --dangerously-skip-permissions
// launch flag. The adapter places it before --prompt-interactive because agy
// treats args after --prompt-interactive as prompt text.
func WithDangerouslySkipPermissions(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom["agy_dangerously_skip_permissions"] = enabled
	}
}

// WithWorkingDir sets the Agy Agent workspace/cwd for tmux launch.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWorkingDir] = dir
	}
}

// WithResumeSessionID resumes an Antigravity CLI conversation by id via
// `agy --conversation <id>`.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithMCPConfig records a workspace-scoped Antigravity MCP config. The tmux
// adapter writes it to .agents/mcp_config.json before launching agy.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = configJSON
	}
}

// WithBridgeOnlyTools writes an Antigravity workspace hook that denies built-in
// mutation/execution tools so required actions must route through MCP bridge
// tools instead.
func WithBridgeOnlyTools(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyBridgeOnlyTools] = enabled
	}
}

// WithAutoApproveWebSearch allows the Agy TUI's web-search approval prompt
// for a call that is already scoped to SearchWeb. It does not enable --force.
func WithAutoApproveWebSearch() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAutoApproveWebSearch] = true
	}
}

// WithInteractiveSessionID links a Agy Agent tmux run to the owning
// application session so follow-up input can be sent directly to tmux.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession keeps the tmux-backed Agy Agent alive
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
