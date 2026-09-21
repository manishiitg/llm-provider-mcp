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
// them afterwards. Mounting is the explicit request for tool-capable
// execution, so a mounted turn runs with --dangerously-skip-permissions —
// natives approved alongside the bridge, per the contract gaps. Mounted
// turns serialize process-wide: parallel mounts would expose each turn's
// bridge to the others.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = configJSON
	}
}
