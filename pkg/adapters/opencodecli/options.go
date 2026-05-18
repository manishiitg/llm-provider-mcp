package opencodecli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

const (
	MetadataKeyOpenCodeModel               = "opencode_model"
	MetadataKeyResumeSessionID             = "opencode_resume_session_id"
	MetadataKeyAgent                       = "opencode_agent"
	MetadataKeyWorkingDir                  = "opencode_working_dir"
	MetadataKeyProjectConfig               = "opencode_project_config"
	MetadataKeyMCPConfig                   = "opencode_mcp_config"
	MetadataKeyAutoApproveWebSearch        = "opencode_auto_approve_web_search"
	MetadataKeyInteractiveSessionID        = "opencode_interactive_session_id"
	MetadataKeyPersistentInteractive       = "opencode_persistent_interactive"
	MetadataKeyContinueLastSession         = "opencode_continue_last_session"
	MetadataKeyDangerouslySkipPermissions  = "opencode_dangerously_skip_permissions"
)

// WithOpenCodeModel sets the OpenCode CLI --model flag. Use "opencode-cli" or
// "auto" to let OpenCode use its configured default without passing --model.
func WithOpenCodeModel(model string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyOpenCodeModel] = model
	}
}

// WithResumeSessionID resumes a native OpenCode chat with --resume <sessionID>.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithAgent sets the OpenCode --agent flag.
func WithAgent(agent string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAgent] = agent
	}
}

// WithWorkingDir sets the OpenCode workspace/cwd for tmux launch.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWorkingDir] = dir
	}
}

// WithProjectConfig writes a temporary/restored .opencode/cli.json in the
// workspace before launching OpenCode.
func WithProjectConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectConfig] = configJSON
	}
}

// WithMCPConfig writes a temporary/restored .opencode/mcp.json in the workspace
// before launching OpenCode.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = configJSON
	}
}

// WithAutoApproveWebSearch allows the OpenCode TUI's web-search approval prompt
// for a call that is already scoped to SearchWeb. It does not enable --force.
func WithAutoApproveWebSearch() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAutoApproveWebSearch] = true
	}
}

// WithInteractiveSessionID links a OpenCode tmux run to the owning
// application session so follow-up input can be sent directly to tmux.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession keeps the tmux-backed OpenCode alive
// across completed chat turns.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithContinueLastSession resumes the most recent OpenCode session (--continue).
func WithContinueLastSession() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyContinueLastSession] = true
	}
}

// WithDangerouslySkipPermissions passes --dangerously-skip-permissions to OpenCode.
func WithDangerouslySkipPermissions() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions] = true
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
