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
	MetadataKeyOutputSchema          = "codex_output_schema"
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
	"image_generation",
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

// WithFullAuto enables low-friction local work. Newer Codex CLI versions map
// this to --dangerously-bypass-approvals-and-sandbox instead of deprecated
// --full-auto.
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

// WithOutputSchema sets the --output-schema flag for structured output.
func WithOutputSchema(schemaPath string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyOutputSchema] = schemaPath
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
// Values: "none", "minimal", "low", "medium", "high", "xhigh"
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
// completed chat turns. Workflow runs should use the default exec-json path.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithWriteProjectInstructionFile is an OFF-by-default feature flag that
// asks the adapter to ALSO write the per-session system prompt to
// <workingDir>/AGENTS.md (Codex's project-instructions convention), in
// addition to the existing -c model_instructions_file injection. Useful
// when the operator wants the prompt visible inside the workspace for
// debugging or when downstream tooling reads AGENTS.md. Cleanup at
// session teardown byte-restores any pre-existing AGENTS.md so
// operator-owned content survives successful runs.
//
// Risk caveat (vs cursor's .cursor/rules/ multi-file): Codex's
// convention is a single AGENTS.md file. If the orchestrator process
// crashes between write and cleanup, the operator's pre-existing
// AGENTS.md is destroyed. Off-by-default keeps the blast radius bounded
// to callers that explicitly accept the trade-off.
func WithWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile] = enabled
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
