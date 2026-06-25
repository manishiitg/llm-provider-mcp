package picli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	MetadataKeyWorkingDir            = "pi_working_dir"
	MetadataKeyInteractiveSessionID  = "pi_interactive_session_id"
	MetadataKeyPersistentInteractive = "pi_persistent_interactive"
	MetadataKeyResumeSessionID       = "pi_resume_session_id"
	MetadataKeyProvider              = "pi_provider"
	MetadataKeyMCPConfig             = "pi_mcp_config"
	MetadataKeyBridgeOnlyTools       = "pi_bridge_only_tools"
	MetadataKeyMCPExtension          = "pi_mcp_extension"
	MetadataKeyStatuslineExtension   = "pi_statusline_extension"

	defaultPiMCPExtension        = "npm:pi-mcp-adapter"
	defaultPiStatuslineExtension = "npm:@narumitw/pi-statusline@0.8.0"
)

// WithWorkingDir sets the Pi CLI workspace/cwd for tmux launch.
func WithWorkingDir(dir string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyWorkingDir] = dir
	}
}

// WithInteractiveSessionID links a Pi tmux run to the owning application
// session so follow-up input can be sent directly to tmux.
func WithInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyInteractiveSessionID] = sessionID
	}
}

// WithPersistentInteractiveSession keeps the tmux-backed Pi session alive
// across completed chat turns.
func WithPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyPersistentInteractive] = enabled
	}
}

// WithResumeSessionID resumes a Pi native session created with --session-id.
func WithResumeSessionID(sessionID string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyResumeSessionID] = sessionID
	}
}

// WithProvider overrides the Pi provider portion of provider/model routing.
// Model IDs can also use provider/model form, for example
// google/gemini-3.5-flash.
func WithProvider(provider string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyProvider] = provider
	}
}

// WithMCPConfig records a Pi MCP config. The tmux adapter writes it to the
// Pi project override file .pi/mcp.json before launching Pi with the MCP
// adapter extension.
func WithMCPConfig(configJSON string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPConfig] = configJSON
	}
}

// WithBridgeOnlyTools disables Pi's built-in tools while leaving explicit
// extension/custom tools, including the MCP adapter, enabled.
func WithBridgeOnlyTools(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyBridgeOnlyTools] = enabled
	}
}

// WithMCPExtension overrides the Pi extension source used for MCP support.
// The default is npm:pi-mcp-adapter.
func WithMCPExtension(source string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMCPExtension] = source
	}
}

// WithStatuslineExtension overrides the Pi statusline extension source.
// Pass "off", "false", "0", or "none" to disable the adapter-managed
// statusline extension for a call.
func WithStatuslineExtension(source string) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyStatuslineExtension] = source
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

func piWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if dir, ok := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); ok {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			return filepath.Clean(trimmed)
		}
	}
	return ""
}

func piInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyInteractiveSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func piPersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyPersistentInteractive].(bool)
	return ok && enabled
}

func piResumeSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func piProviderFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if provider, ok := opts.Metadata.Custom[MetadataKeyProvider].(string); ok {
		return strings.TrimSpace(provider)
	}
	return ""
}

func piMCPConfigFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if configJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok {
		return strings.TrimSpace(configJSON)
	}
	return ""
}

func piBridgeOnlyToolsFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyBridgeOnlyTools].(bool)
	return ok && enabled
}

func piMCPExtensionFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return defaultPiMCPExtension
	}
	if source, ok := opts.Metadata.Custom[MetadataKeyMCPExtension].(string); ok {
		if trimmed := strings.TrimSpace(source); trimmed != "" {
			return trimmed
		}
	}
	return defaultPiMCPExtension
}

func piStatuslineExtensionFromOptions(opts *llmtypes.CallOptions) string {
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if source, ok := opts.Metadata.Custom[MetadataKeyStatuslineExtension].(string); ok {
			return normalizePiOptionalExtensionSource(source, defaultPiStatuslineExtension)
		}
	}
	return normalizePiOptionalExtensionSource(os.Getenv(EnvPiStatuslineExtension), defaultPiStatuslineExtension)
}

func normalizePiOptionalExtensionSource(source, fallback string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return fallback
	}
	switch strings.ToLower(trimmed) {
	case "0", "false", "no", "off", "none", "disabled", "disable":
		return ""
	default:
		return trimmed
	}
}
