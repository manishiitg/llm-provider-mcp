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
	MetadataKeyMuseStructuredTransport   = "muse_structured_transport"
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

func museInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil {
		return ""
	}
	id, _ := opts.Metadata.Custom[MetadataKeyMuseInteractiveSessionID].(string)
	return id
}

func musePersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyMusePersistentInteractive].(bool)
	return enabled
}

func museWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil {
		return ""
	}
	dir, _ := opts.Metadata.Custom[MetadataKeyMuseWorkingDir].(string)
	return dir
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

// WithMuseStructuredTransport selects the exec --json lane (per-turn
// process, no live pane). Default is the tmux lane, like every other
// coding provider: structured must be asked for explicitly, and the
// orchestrator asks exactly when it wants structured (wantsStructured).
func WithMuseStructuredTransport(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseStructuredTransport] = enabled
	}
}

func museStructuredTransportRequested(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyMuseStructuredTransport].(bool)
	return enabled
}

// MetadataKeyMuseProjectInstructionOnly carries the per-session system
// prompt SOLELY via the projected <workingDir>/AGENTS.md file and skips
// typing it inline. Muse has no --system-prompt flag, so without this the
// whole preamble is typed into the TUI on every turn. Default off; inline
// concatenation stays the primary path. When enabled, inline typing is
// skipped only if the AGENTS.md projection actually succeeded — otherwise
// the adapter falls back to inline so the prompt is never silently dropped.
// Same shape as codex's MetadataKeyProjectInstructionOnly.
const MetadataKeyMuseProjectInstructionOnly = "muse_project_instruction_only"

// MetadataKeyMuseRestoreProjectFiles controls whether the projected
// AGENTS.md preserves an operator's pre-existing content across the
// session. Default off: the run writes a fresh artifact and deletes it on
// cleanup. Pass WithRestoreProjectFiles(true) to byte-restore instead.
// Same shape as codex's MetadataKeyRestoreProjectFiles.
const MetadataKeyMuseRestoreProjectFiles = "muse_restore_project_files"

// WithProjectInstructionOnly makes the adapter carry the per-session system
// prompt solely via <workingDir>/AGENTS.md (which muse auto-loads as
// project instructions in a trusted workspace).
func WithProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseProjectInstructionOnly] = enabled
	}
}

// WithRestoreProjectFiles opts back into byte-restoring a pre-existing
// AGENTS.md on session teardown instead of deleting the projected file.
func WithRestoreProjectFiles(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseRestoreProjectFiles] = enabled
	}
}

func museProjectInstructionOnlyFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyMuseProjectInstructionOnly].(bool)
	return enabled
}

func museRestoreProjectFilesFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyMuseRestoreProjectFiles].(bool)
	return enabled
}

// MetadataKeyMuseStreamTranscript opts into streaming structured content
// (assistant text, tool starts/ends, reasoning) from the native
// session.jsonl while a tmux turn runs. OFF by default — the tmux lane
// stays silent unless the orchestrator asks (enableStreaming), same as
// cursor's MetadataKeyStreamTranscript.
const MetadataKeyMuseStreamTranscript = "muse_stream_transcript"

// WithStreamTranscript opts into transcript streaming for tmux turns.
func WithStreamTranscript(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseStreamTranscript] = enabled
	}
}

func museInteractiveStreamTranscriptEnabled(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyMuseStreamTranscript].(bool)
	return enabled
}
