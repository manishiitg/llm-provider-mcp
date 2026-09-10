package musecli

import (
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// MetadataKeyMuseResumeSessionID carries a native muse session id (the
// stream.id surfaced in GenerationInfo) back into the next exec lane turn
// as `--session-id`.
const MetadataKeyMuseResumeSessionID = "muse_resume_session_id"

func ensureMetadata(opts *llmtypes.CallOptions) {
	if opts.Metadata == nil {
		opts.Metadata = &llmtypes.Metadata{Custom: make(map[string]interface{})}
	}
	if opts.Metadata.Custom == nil {
		opts.Metadata.Custom = make(map[string]interface{})
	}
}

// WithResumeSessionID resumes a native muse session created by an earlier
// turn (exec --session-id). Empty ids are stored as-is and ignored by the
// lane; the adapter never invents a session.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseResumeSessionID] = sessionID
	}
}

func museResumeSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil {
		return ""
	}
	id, _ := opts.Metadata.Custom[MetadataKeyMuseResumeSessionID].(string)
	return id
}

// Metadata keys for the tmux lane (consumed when the interactive adapter
// lands; stored now so the option registries resolve).
const (
	MetadataKeyMuseInteractiveSessionID  = "muse_interactive_session_id"
	MetadataKeyMusePersistentInteractive = "muse_persistent_interactive"
	MetadataKeyMuseWorkingDir            = "muse_working_dir"
)

// MetadataKeyMuseMCPConfig carries a bridge MCP config document (a JSON
// object with an "mcpServers" map, same shape as cursor's MetadataKeyMCPConfig)
// for the exec lane to merge into the user's muse settings.json.
const MetadataKeyMuseMCPConfig = "muse_mcp_config"

// WithMCPConfig mounts MCP servers for one exec run by merging the document's
// "mcpServers" entries into $XDG_CONFIG_HOME/muse/settings.json (merge, don't
// clobber) for the duration of the run; the previous settings are restored
// afterwards. Empty or whitespace-only documents are stored as-is and ignored
// by the lane.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseMCPConfig] = configJSON
	}
}

func museMCPConfigFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil {
		return ""
	}
	cfg, _ := opts.Metadata.Custom[MetadataKeyMuseMCPConfig].(string)
	return cfg
}

// WithMuseInteractiveSessionID associates the tmux session with an owner
// session id (mirrors WithCodexInteractiveSessionID).
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseInteractiveSessionID] = sessionID
	}
}

// WithMusePersistentInteractiveSession keeps the tmux session alive after
// turn completion.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMusePersistentInteractive] = enabled
	}
}

// WithMuseWorkingDir pins the tmux session working directory.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseWorkingDir] = dir
	}
}
