package codexcli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Constants for custom metadata keys
const (
	MetadataKeyCodexModel            = "codex_model"
	MetadataKeyResumeSessionID       = "codex_resume_session_id"
	MetadataKeyApprovalMode          = "codex_approval_mode"
	MetadataKeySandbox               = "codex_sandbox"
	MetadataKeyFullAuto              = "codex_full_auto"
	MetadataKeyProjectDirID          = "codex_project_dir_id"
	MetadataKeyConfigProfile         = "codex_config_profile"
	MetadataKeyAdditionalDirs        = "codex_additional_dirs"
	MetadataKeyDisableFeatures       = "codex_disable_features"
	MetadataKeyEnableFeatures        = "codex_enable_features"
	MetadataKeyReasoningEffort       = "codex_reasoning_effort"
	MetadataKeyReasoningSummary      = "codex_reasoning_summary"
	MetadataKeyDisableShellTool      = "codex_disable_shell_tool"
	MetadataKeyMCPServers            = "codex_mcp_servers"
	MetadataKeyConfigOverrides       = "codex_config_overrides"
	MetadataKeyApprovalPolicy        = "codex_approval_policy"
	MetadataKeyInteractiveSessionID  = "codex_interactive_session_id"
	MetadataKeyPersistentInteractive = "codex_persistent_interactive"
	// MetadataKeyWriteProjectInstructionFile is the OFF-by-default feature
	// flag for ALSO writing the per-session system prompt to
	// <workingDir>/AGENTS.md (Codex's project-instructions convention,
	// https://github.com/openai/codex/blob/main/AGENTS.md). Default off;
	// codex already injects the prompt via -c model_instructions_file.
	// When enabled, the adapter byte-restores any pre-existing AGENTS.md
	// on session teardown so operator-owned content is preserved across
	// successful runs.
	MetadataKeyWriteProjectInstructionFile = "codex_write_project_instruction_file"
	// MetadataKeyRestoreProjectFiles is the OFF-by-default feature flag
	// controlling whether projected workspace artifacts (AGENTS.md,
	// .codex/config.toml) preserve an operator's pre-existing content
	// across the session. Default off: every run writes a fresh artifact
	// and deletes it on cleanup, never restoring whatever was there
	// before. Pass WithRestoreProjectFiles(true) to opt back into the
	// legacy byte-restore behavior.
	MetadataKeyRestoreProjectFiles = "codex_restore_project_files"
	// MetadataKeyProjectInstructionOnly is the OFF-by-default feature
	// flag that carries the per-session system prompt SOLELY via the
	// projected <workingDir>/AGENTS.md file and SKIPS the CLI-side
	// injection (-c model_instructions_file in the tmux path). Default off;
	// the CLI injection is the primary path. When enabled, the CLI
	// injection is skipped only if the AGENTS.md projection actually
	// succeeded for that path — otherwise the adapter falls back to the
	// CLI injection so the prompt is never silently dropped. For large
	// prompts this avoids paying the system-prompt token cost twice.
	MetadataKeyProjectInstructionOnly = "codex_project_instruction_only"
)

func appendCodexDisableUpdateArgs(args []string) []string {
	return append(args, "-c", "check_for_update_on_startup=false")
}

var codexBridgeOnlyDisabledFeatures = []string{
	"shell_tool",
	"unified_exec",
	"tool_search",
	"multi_agent",
	"apps",
	"browser_use",
	"browser_use_external",
	"computer_use",
	"workspace_dependencies",
	"hooks",
	"plugins",
	"unavailable_dummy_tools",
}

func appendCodexDisabledFeatureArgs(args []string, seen map[string]bool, features ...string) []string {
	for _, feature := range features {
		feature = strings.TrimSpace(feature)
		if feature == "" || seen[feature] {
			continue
		}
		args = append(args, "--disable", feature)
		seen[feature] = true
	}
	return args
}

func appendCodexFeatureCSV(args []string, flag string, features string) []string {
	for _, feature := range strings.Split(features, ",") {
		feature = strings.TrimSpace(feature)
		if feature != "" {
			args = append(args, flag, feature)
		}
	}
	return args
}

// WithCodexModel sets the --model flag for the Codex CLI.
func WithCodexModel(model string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyCodexModel] = model
	}
}

// WithResumeSessionID sets the session ID to resume via `codex exec resume <id>`.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithApprovalMode sets the --ask-for-approval flag for the Codex CLI.
// Values: "untrusted", "on-request", "never"
func WithApprovalMode(mode string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyApprovalMode] = mode
	}
}

// WithSandbox sets the --sandbox flag for the Codex CLI.
// Values: "read-only", "workspace-write", "danger-full-access"
func WithSandbox(sandbox string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeySandbox] = sandbox
	}
}

// WithFullAuto is kept for older callers. The tmux transport ignores this
// metadata; use WithApprovalPolicy("never") and sandbox/config overrides for
// current Codex CLI runs.
func WithFullAuto() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyFullAuto] = true
	}
}

// WithProjectDirID sets a working directory for the Codex CLI via --cd flag.
func WithProjectDirID(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProjectDirID] = dir
	}
}

// WithConfigProfile sets the --profile flag to load a configuration profile.
func WithConfigProfile(profile string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyConfigProfile] = profile
	}
}

// WithAdditionalDirs sets the --add-dir flag to grant additional directory write access.
func WithAdditionalDirs(dirs string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyAdditionalDirs] = dirs
	}
}

// WithReasoningEffort sets the model_reasoning_effort config override.
// Values depend on the selected model. Current Codex models may also support
// "max" and "ultra" in addition to the legacy effort levels.
func WithReasoningEffort(effort string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyReasoningEffort] = effort
	}
}

// WithReasoningSummary sets the model_reasoning_summary config override.
// Values: "auto", "concise", "detailed", "none"
func WithReasoningSummary(summary string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyReasoningSummary] = summary
	}
}

// WithDisableShellTool disables the built-in shell tool so only MCP tools are available.
func WithDisableShellTool() llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyDisableShellTool] = true
	}
}

// WithDisableFeatures disables one or more Codex CLI features (comma-separated).
// Each feature translates to --disable <feature> on the CLI.
func WithDisableFeatures(features string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyDisableFeatures] = features
	}
}

// WithEnableFeatures enables one or more Codex CLI features (comma-separated).
// Each feature translates to --enable <feature> on the CLI.
func WithEnableFeatures(features string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyEnableFeatures] = features
	}
}

// WithMCPServers passes MCP server configuration as a JSON string.
// This is written to a temp config.toml that is loaded via --config overrides.
// Example JSON: {"api-bridge":{"command":"/path/to/mcpbridge","env":{"MCP_API_URL":"..."}}}
func WithMCPServers(mcpJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPServers] = mcpJSON
	}
}

// WithConfigOverrides passes arbitrary config overrides as key=value pairs.
// Each entry is passed as a separate -c flag.
// Example: []string{"model_reasoning_effort=high", "features.shell_tool=false"}
func WithConfigOverrides(overrides []string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyConfigOverrides] = overrides
	}
}

// WithApprovalPolicy sets the approval_policy config override.
// Values: "never" (auto-approve all), "on-request" (model decides), "untrusted" (most restrictive)
func WithApprovalPolicy(policy string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyApprovalPolicy] = policy
	}
}

// WithInteractiveSessionID links an interactive Codex TUI run to the owning
// application session so follow-up user input can be sent directly to tmux.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession keeps the tmux-backed Codex TUI alive across
// completed chat turns. Direct callers without this get a bounded tmux session.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithWriteProjectInstructionFile is an OFF-by-default feature flag that
// asks the adapter to ALSO project codex's project-convention files into
// the working dir at session start (and byte-restore them at teardown):
//
//   - <workingDir>/AGENTS.md
//     The per-session system prompt, mirroring what -c
//     model_instructions_file already injects.
//
//   - <workingDir>/.codex/config.toml
//     AgentWorks session defaults that neutralize user-level /fast, update,
//     TUI-notification, connector, remote-plugin, and automatic skill-MCP
//     choices, plus optional [mcp_servers.NAME] tables synthesized from
//     WithMCPServers JSON. Explicit per-invocation -c overrides still take
//     precedence.
//
//   - <workingDir>/.codex/hooks.json
//
//   - <workingDir>/.codex/hooks/deny-builtin.sh
//     The PreToolUse deny hook (matcher ^(Bash|apply_patch)$) and its
//     exit-2 deny script. Forces the model to route through MCP servers
//     by blocking codex's built-in shell + edit tools. Per
//     developers.openai.com/codex/hooks, exit 2 == "System Block".
//
// Risk caveat: AGENTS.md, .codex/config.toml, and .codex/hooks.json are
// all single-file conventions. If the orchestrator process crashes
// between write and cleanup, the operator's pre-existing copies are
// destroyed. Off-by-default keeps the blast radius bounded to callers
// that explicitly accept the trade-off.
func WithWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile] = enabled
	}
}

// WithRestoreProjectFiles controls whether projected workspace artifacts
// (AGENTS.md, .codex/config.toml) preserve the operator's pre-existing
// content across a session. OFF by default: each run writes a fresh
// artifact and removes it on cleanup, never restoring whatever was there
// before. Pass true to opt back into the legacy byte-restore behavior.
func WithRestoreProjectFiles(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyRestoreProjectFiles] = enabled
	}
}

// WithProjectInstructionOnly makes the adapter carry the per-session system
// prompt SOLELY via the projected <workingDir>/AGENTS.md file and SKIP the
// CLI-side injection (-c model_instructions_file in the tmux path). OFF by
// default. Codex
// auto-loads AGENTS.md as project instructions, so the prompt is still applied
// — but only once, avoiding the doubled system prompt (and doubled token cost)
// that results from passing the same bytes through both the CLI flag and
// AGENTS.md.
//
// Requires the AGENTS.md projection to be active (it is, by default; see
// WithWriteProjectInstructionFile) and a non-empty working dir. If the
// projection is disabled or its write fails, the adapter falls back to the CLI
// injection so the prompt is never silently dropped.
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
