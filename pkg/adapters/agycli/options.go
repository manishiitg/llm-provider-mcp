package agycli

import (
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Metadata keys for agy CallOptions.
const (
	MetadataKeyWorkingDir            = "agy_working_dir"
	MetadataKeyInteractiveSessionID  = "agy_interactive_session_id"
	MetadataKeyPersistentInteractive = "agy_persistent_interactive"
	MetadataKeyResumeSessionID       = "agy_resume_session_id"
	MetadataKeyMCPConfig             = "agy_mcp_config"
	MetadataKeyNativeToolsMode       = "agy_native_tools_mode"
)

func ensureMetadata(opts *llmtypes.CallOptions) {
	if opts.Metadata == nil {
		opts.Metadata = &llmtypes.Metadata{Custom: make(map[string]interface{})}
	}
	if opts.Metadata.Custom == nil {
		opts.Metadata.Custom = make(map[string]interface{})
	}
}

// WithWorkingDir sets the agy workspace/cwd for launch.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWorkingDir] = dir
	}
}

// WithInteractiveSessionID links an agy run to the owning application
// session so follow-up input can be sent directly to tmux.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession keeps the agy session alive across
// completed chat turns.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		if enabled {
			opts.Metadata.Custom[MetadataKeyPersistentInteractive] = "true"
		} else {
			opts.Metadata.Custom[MetadataKeyPersistentInteractive] = "false"
		}
	}
}

// WithResumeSessionID resumes an agy native conversation created by an
// earlier turn (surfaced as conversation_id).
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithMCPConfig mounts the document's stdio mcpServers for one turn via
// `agy mcp add` (global user config: agy offers no scoped mount) and removes
// them afterwards. Mounted exec turns use --dangerously-skip-permissions;
// WithNativeToolsMode installs a PreToolUse gate when the caller needs
// MCP-only or native read/search mode. Mounted turns serialize different
// tool surfaces process-wide because mounts are global user configuration.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = configJSON
	}
}

// WithNativeToolsMode applies AgentWorks' coding-agent tool setting. The
// bridge remains available in both modes. "hybrid" admits AGY's native
// read/search tools; "mcp_only" denies native tools at PreToolUse.
func WithNativeToolsMode(mode string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyNativeToolsMode] = mode
	}
}
