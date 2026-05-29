package opencodecli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

const (
	MetadataKeyOpenCodeModel              = "opencode_model"
	MetadataKeyResumeSessionID            = "opencode_resume_session_id"
	MetadataKeyAgent                      = "opencode_agent"
	MetadataKeyWorkingDir                 = "opencode_working_dir"
	MetadataKeyProjectConfig              = "opencode_project_config"
	MetadataKeyMCPConfig                  = "opencode_mcp_config"
	MetadataKeyAutoApproveWebSearch       = "opencode_auto_approve_web_search"
	MetadataKeyInteractiveSessionID       = "opencode_interactive_session_id"
	MetadataKeyPersistentInteractive      = "opencode_persistent_interactive"
	MetadataKeyContinueLastSession        = "opencode_continue_last_session"
	MetadataKeyDangerouslySkipPermissions = "opencode_dangerously_skip_permissions"

	// MetadataKeySubProviderID identifies which OpenCodeSubProvider tile
	// owns this call (e.g. "opencode-cli-kimi"). The adapter uses it to
	// resolve tier shortcuts inside that sub-provider's namespace and to
	// pick the right env var name when reading per-provider keys.
	MetadataKeySubProviderID = "opencode_sub_provider_id"

	// MetadataKeySubProviderAPIKeys carries per-sub-provider credentials
	// as a map[string]string keyed by env var name (e.g.
	// {"KIMI_API_KEY": "sk-kimi-..."}). The adapter injects only the
	// entries relevant to the active sub-provider into the launched
	// `opencode run` env.
	MetadataKeySubProviderAPIKeys = "opencode_sub_provider_api_keys"

	// MetadataKeyWriteProjectInstructionFile is the OFF-by-default
	// feature flag for ALSO writing the per-session system prompt to
	// <workingDir>/AGENTS.md (OpenCode's project-instructions
	// convention, same file as codex). Default off; OpenCode's
	// system-prompt path covers the non-workspace-touching case. When
	// enabled, the adapter byte-restores any pre-existing AGENTS.md on
	// session teardown so operator-owned content is preserved across
	// successful runs.
	MetadataKeyWriteProjectInstructionFile = "opencode_write_project_instruction_file"
	// MetadataKeyRestoreProjectFiles is the OFF-by-default feature flag
	// controlling whether projected workspace artifacts (opencode.jsonc,
	// AGENTS.md) preserve an operator's pre-existing content across the
	// session. Default off: every run writes a fresh artifact and deletes
	// it on cleanup, never restoring whatever was there before. Pass
	// WithRestoreProjectFiles(true) to opt back into the legacy
	// byte-restore behavior.
	MetadataKeyRestoreProjectFiles = "opencode_restore_project_files"
)

// WithOpenCodeModel sets the OpenCode CLI --model flag. Use "opencode-cli" or
// "auto" to let OpenCode use its configured default without passing --model.
func WithOpenCodeModel(model string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyOpenCodeModel] = model
	}
}

// WithResumeSessionID resumes a native OpenCode chat with --session <sessionID>.
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

// WithWorkingDir sets the OpenCode workspace/cwd.
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

// WithRestoreProjectFiles controls whether projected workspace artifacts
// (opencode.jsonc, AGENTS.md) preserve the operator's pre-existing content
// across a session. OFF by default: each run writes a fresh artifact and
// removes it on cleanup, never restoring whatever was there before. Pass
// true to opt back into the legacy byte-restore behavior.
func WithRestoreProjectFiles(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyRestoreProjectFiles] = enabled
	}
}

// opencodeRestoreProjectFilesFromOptions reads the OFF-by-default restore
// flag. Returns false when unset: the default writes fresh and deletes on
// cleanup, never restoring pre-existing content.
func opencodeRestoreProjectFilesFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyRestoreProjectFiles].(bool)
	return enabled
}

// WithMCPConfig writes a temporary/restored .opencode/mcp.json in the workspace
// before launching OpenCode.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = configJSON
	}
}

// WithAutoApproveWebSearch allows OpenCode's web-search approval for a call
// that is already scoped to SearchWeb. It does not enable --force.
func WithAutoApproveWebSearch() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAutoApproveWebSearch] = true
	}
}

// WithInteractiveSessionID is retained for API compatibility. OpenCode CLI uses
// structured JSON invocations; use WithResumeSessionID for continuation.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession is retained for API compatibility. OpenCode
// CLI uses structured JSON invocations instead of live tmux sessions.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithWriteProjectInstructionFile is an OFF-by-default feature flag that
// asks the adapter to ALSO project opencode's project-convention files
// into the working dir at session start (and byte-restore them at
// teardown):
//
//   - <workingDir>/AGENTS.md
//     Per-session system prompt; opencode reads this as project
//     instructions (same convention as codex).
//
//   - <workingDir>/.opencode/plugins/deny-builtin.js
//     ES-module opencode plugin that throws in tool.execute.before
//     for built-in tool names (read, write, edit, bash, grep, glob,
//     list, patch, webfetch, task), forcing the model to route
//     through MCP servers (whose tool names are provider-prefixed:
//     mcp__<server>__<tool> and are not in the deny set). Per
//     opencode.ai/docs/plugins this is the ONLY way to declare hooks
//     in opencode — config-only hooks are not supported, hence the
//     plugin file vs codex/gemini's hooks.json/settings.json
//     approach.
//
// The MCP server list itself lands at <workingDir>/opencode.jsonc
// unconditionally via the existing WithMCPConfig wiring (separate
// from this flag).
//
// Risk caveat: AGENTS.md and deny-builtin.js are single-file
// conventions. If the orchestrator process crashes between write and
// cleanup, the operator's pre-existing copies are destroyed.
// Off-by-default keeps the blast radius bounded to callers that
// explicitly accept the trade-off.
func WithWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile] = enabled
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

// WithPermissionsEnforced prevents passing --dangerously-skip-permissions,
// allowing OpenCode's permission system to restrict tool access.
func WithPermissionsEnforced() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions] = false
	}
}

// WithOpenCodeSubProvider scopes the call to a specific OpenCode sub-provider
// tile (one of the OpenCodeSubProviders()). It controls how tier labels
// ("high"/"medium"/"low") resolve to concrete models and which API-key env
// var is exported to the launched `opencode` process.
func WithOpenCodeSubProvider(id string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeySubProviderID] = id
	}
}

// WithOpenCodeSubProviderAPIKey attaches a credential for a single
// OpenCode-backed sub-provider, keyed by the env var the OpenCode bundled
// SDK reads (e.g. KIMI_API_KEY, DEEPSEEK_API_KEY, DASHSCOPE_API_KEY,
// MINIMAX_API_KEY, ZHIPU_API_KEY). Multiple calls accumulate — only the
// env var matching the active sub-provider is exported at launch.
func WithOpenCodeSubProviderAPIKey(envVar, apiKey string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		raw, _ := opts.Metadata.Custom[MetadataKeySubProviderAPIKeys].(map[string]string)
		if raw == nil {
			raw = make(map[string]string)
		}
		raw[envVar] = apiKey
		opts.Metadata.Custom[MetadataKeySubProviderAPIKeys] = raw
	}
}

// WithOpenCodeSubProviderAPIKeys replaces the whole per-sub-provider key
// map in one call. Useful for the server-side handler that already loads
// the full credential set from storage.
func WithOpenCodeSubProviderAPIKeys(keys map[string]string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		copied := make(map[string]string, len(keys))
		for k, v := range keys {
			copied[k] = v
		}
		opts.Metadata.Custom[MetadataKeySubProviderAPIKeys] = copied
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
