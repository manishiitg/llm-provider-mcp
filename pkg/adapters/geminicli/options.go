package geminicli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

// Constants for custom metadata keys
const (
	MetadataKeyGeminiModel      = "gemini_model"
	MetadataKeyResumeSessionID  = "gemini_resume_session_id"
	MetadataKeyApprovalMode     = "gemini_approval_mode"
	MetadataKeySystemPromptFile = "gemini_system_prompt_file"
	MetadataKeyAllowedTools     = "gemini_allowed_tools"
	// MetadataKeyProjectSettings holds a JSON string to write to .gemini/settings.json
	// in a temp working directory. This controls tools.core (tool restriction),
	// mcpServers (MCP bridge), and other Gemini CLI project settings.
	MetadataKeyProjectSettings = "gemini_project_settings"
	MetadataKeyPolicyPath      = "gemini_policy_path"
	MetadataKeyAdminPolicyPath = "gemini_admin_policy_path"
	MetadataKeyWorkingDir      = "gemini_working_dir"

	// MetadataKeyProjectDirID controls which per-invocation project directory to use.
	// When set, the adapter uses /tmp/gemini-cli-project-{id} instead of generating a new one.
	// This is used to ensure resume calls use the same directory as the original invocation.
	MetadataKeyProjectDirID = "gemini_project_dir_id"

	// MetadataKeyProjectDirAbsolute overrides the default /tmp project directory with
	// an absolute path. When set, the adapter uses this path directly as GEMINI_PROJECT_DIR
	// and as the tmux cwd, bypassing the os.TempDir() join. Intended for the workflow
	// main_agent case where the project dir should live inside the workflow folder
	// (e.g. /data/docs/Workflow/<name>/.gemini-main) so it survives /tmp wipes and the
	// status line shows a meaningful workspace. Steps/sub-agents should leave this unset
	// to keep the per-invocation /tmp isolation that prevents settings.json clobber.
	MetadataKeyProjectDirAbsolute = "gemini_project_dir_absolute"

	MetadataKeyInteractiveSessionID  = "gemini_interactive_session_id"
	MetadataKeyPersistentInteractive = "gemini_persistent_interactive"

	// MetadataKeyWriteProjectInstructionFile is the OFF-by-default feature
	// flag for ALSO writing the per-session system prompt to
	// <workingDir>/GEMINI.md (Gemini CLI's project-context convention).
	// Default off; the existing GEMINI_SYSTEM_MD path covers the
	// non-workspace-touching case. When enabled, the adapter byte-restores
	// any pre-existing GEMINI.md on session teardown so operator-owned
	// content is preserved across successful runs.
	MetadataKeyWriteProjectInstructionFile = "gemini_write_project_instruction_file"
	// MetadataKeyRestoreProjectFiles is the OFF-by-default feature flag
	// controlling whether projected workspace artifacts (GEMINI.md,
	// .gemini/settings.json, deny script) preserve an operator's
	// pre-existing content across the session. Default off: every run
	// writes a fresh artifact and deletes it on cleanup, never restoring
	// whatever was there before. Pass WithRestoreProjectFiles(true) to opt
	// back into the legacy byte-restore behavior.
	MetadataKeyRestoreProjectFiles = "gemini_restore_project_files"

	// MetadataKeyProjectInstructionOnly is the OFF-by-default feature flag
	// that makes the adapter carry the per-session system prompt SOLELY via
	// the projected <workingDir>/GEMINI.md and SKIP the GEMINI_SYSTEM_MD env
	// injection. Default off: the prompt is carried by BOTH GEMINI_SYSTEM_MD
	// and (when projection is enabled) GEMINI.md, which doubles the
	// system-prompt token cost for large prompts. When enabled AND the
	// GEMINI.md projection actually succeeds, the env injection is skipped so
	// the prompt is carried once. If the projection is disabled (flag off /
	// empty working dir) or its write fails, the env injection still fires so
	// the prompt is never silently dropped. Pass
	// WithProjectInstructionOnly(true) to enable.
	MetadataKeyProjectInstructionOnly = "gemini_project_instruction_only"
)

// WithGeminiModel sets the --model flag for the Gemini CLI.
func WithGeminiModel(model string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyGeminiModel] = model
	}
}

// WithResumeSessionID sets the --resume flag with a session ID so the Gemini CLI
// resumes an existing session instead of starting a new one.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithApprovalMode sets the --approval-mode flag for the Gemini CLI.
// Use "yolo" to skip all permission prompts.
func WithApprovalMode(mode string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyApprovalMode] = mode
	}
}

// WithSystemPromptFile sets the GEMINI_SYSTEM_MD environment variable
// to a file path containing the system prompt.
func WithSystemPromptFile(path string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeySystemPromptFile] = path
	}
}

// WithProjectSettings sets a JSON string to be written as .gemini/settings.json
// in a temporary working directory. The Gemini CLI is then run from that directory.
// This is the primary mechanism for:
//   - Restricting built-in tools via tools.core allowlist
//   - Configuring MCP servers (bridge) via mcpServers
//   - Any other project-level settings
//
// Example JSON:
//
//	{"tools":{"core":["google_web_search"]},"mcpServers":{"api-bridge":{"command":"/path/to/mcpbridge","env":{"MCP_API_URL":"..."}}}}
func WithProjectSettings(settingsJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectSettings] = settingsJSON
	}
}

// WithPolicyPath adds a Gemini CLI --policy file or directory.
func WithPolicyPath(path string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPolicyPath] = path
	}
}

// WithAdminPolicyPath adds a Gemini CLI --admin-policy file or directory.
func WithAdminPolicyPath(path string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAdminPolicyPath] = path
	}
}

// WithWorkingDir sets the caller workspace for Gemini. When project settings
// are present, the CLI process starts from the isolated settings directory and
// this path is added with --include-directories so parallel agents cannot
// overwrite each other's MCP bridge/session configuration.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWorkingDir] = dir
	}
}

// WithProjectDirID sets an explicit project directory ID so the Gemini CLI uses
// /tmp/gemini-cli-project-{id}. This ensures resume calls and retries use the
// same isolated project directory as the original invocation.
func WithProjectDirID(id string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectDirID] = id
	}
}

// WithProjectDirAbsolute overrides the default /tmp project directory with an
// absolute path. Takes precedence over WithProjectDirID. Intended for the
// workflow main_agent case so GEMINI_PROJECT_DIR points at a workflow-rooted
// dir (e.g. <workflow>/.gemini-main) rather than /tmp. Sub-step agents should
// leave this unset to keep per-invocation /tmp isolation.
func WithProjectDirAbsolute(absPath string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectDirAbsolute] = absPath
	}
}

// WithInteractiveSessionID links an interactive Gemini CLI TUI run to the
// owning application session so follow-up input can be sent directly to tmux.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession keeps the tmux-backed Gemini CLI TUI alive
// across completed chat turns. Workflow runs should keep stream-json mode.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithWriteProjectInstructionFile is an OFF-by-default feature flag that
// asks the adapter to ALSO project gemini's project-convention files
// into the working dir at session start (and byte-restore them at
// teardown):
//
//   - <workingDir>/GEMINI.md
//     Per-session system prompt, mirroring GEMINI_SYSTEM_MD env
//     injection.
//
//   - <workingDir>/.gemini/settings.json
//     The operator-supplied projectSettingsJSON (from
//     WithProjectSettings) MERGED with a synthesized
//     hooks.BeforeTool deny entry. mcpServers is preserved verbatim;
//     hooks.BeforeTool is appended, not replaced.
//
//   - <workingDir>/.gemini/hooks/deny-builtin.sh
//     POSIX deny script that exits 2 (Gemini's "System Block" per
//     geminicli.com/docs/hooks) on built-in tool calls
//     (read_file, write_file, shell, edit, grep, search_file_content,
//     web_fetch). MCP server tools are NOT in the matcher.
//
// Risk caveat: GEMINI.md and .gemini/settings.json are single-file
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

// WithRestoreProjectFiles controls whether projected workspace artifacts
// (GEMINI.md, .gemini/settings.json, deny script) preserve the operator's
// pre-existing content across a session. OFF by default: each run writes a
// fresh artifact and removes it on cleanup, never restoring whatever was
// there before. Pass true to opt back into the legacy byte-restore
// behavior.
func WithRestoreProjectFiles(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyRestoreProjectFiles] = enabled
	}
}

// WithProjectInstructionOnly is an OFF-by-default feature flag that makes the
// adapter carry the per-session system prompt SOLELY via the projected
// <workingDir>/GEMINI.md and SKIP the GEMINI_SYSTEM_MD environment-variable
// injection. For large prompts this avoids doubling the system-prompt token
// cost (the prompt would otherwise be sent both via GEMINI_SYSTEM_MD and via
// GEMINI.md).
//
// The skip only applies when the GEMINI.md projection actually succeeds. If
// the projection is disabled (this flag off, projection flag off, or empty
// working dir) or its write fails, the GEMINI_SYSTEM_MD env injection still
// fires, so the system prompt is never silently dropped.
func WithProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectInstructionOnly] = enabled
	}
}

// WithAllowedTools sets the deprecated --allowed-tools flag for the Gemini CLI.
// Prefer WithProjectSettings plus Policy Engine rules instead.
func WithAllowedTools(tools string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAllowedTools] = tools
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
