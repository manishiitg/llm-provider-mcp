package llmproviders

import (
	"sort"
	"strings"
)

// CodingAgentTransport describes the process transport used by a coding-agent
// provider. New coding providers should prefer tmux unless the CLI cannot
// support same-session chat, live input, interrupt, and terminal snapshots yet.
type CodingAgentTransport string

const (
	CodingAgentTransportTmux       CodingAgentTransport = "tmux"
	CodingAgentTransportStructured CodingAgentTransport = "structured"
)

// CodingAgentProviderContract is the provider-level contract that host apps can
// rely on when treating a provider as a coding agent. Adapter-specific code
// still owns exact CLI flags, but this struct is the shared capability gate used
// by tests and orchestration code.
type CodingAgentProviderContract struct {
	Provider Provider
	// ModelID is set only when a provider becomes a coding agent for a specific
	// model/transport. Prefer provider-level CLI entries for new coding agents.
	ModelID string

	DisplayName string
	CLIName     string
	Transport   CodingAgentTransport

	// Deprecated marks coding-agent providers that remain runnable for old
	// sessions/configs but should not be offered for new user setup.
	Deprecated          bool
	DeprecationReason   string
	ReplacementProvider Provider

	RequiresWorkingDir     bool
	RequiresOwnerSessionID bool
	UsesPersistentSession  bool
	SupportsLiveInput      bool
	SupportsInterrupt      bool
	SupportsTerminalStream bool
	// SupportsStatusLine reports whether the adapter implements the
	// llmtypes.StatusLineProvider interface and emits a StreamChunkTypeStatusLine
	// chunk (canonical provider name, owning tmux_session in Metadata, no
	// placeholder model). Consumers surface its real-time token/cost telemetry in
	// the terminal pane. Certified by CertStatusLine.
	SupportsStatusLine      bool
	SupportsFinalExtraction bool
	SupportsNativeResume    bool
	UsesMCPBridge           bool
	// RequiresMCPBridgeConfig means host apps must fail before launch if they
	// cannot build and pass the provider's MCP bridge config. Coding agents
	// should never silently fall back to tool-less or unrestricted built-in tool
	// mode when the bridge is unavailable.
	RequiresMCPBridgeConfig bool
	SupportsBridgeOnlyTools bool
	UsesNativeSystemPrompt  bool
	LaunchesViaLoginShell   bool
	ProcessScopedCleanup    bool
	HandlesTmuxSessionLoss  bool
	StructuredFallback      bool
	ImageInputInteractive   bool

	// SurfacesTokenUsage reports whether the adapter consumes input/output/cache
	// token counts from the CLI's output (stream-json events, transcript files,
	// etc.) and reports them through GenerationInfo. False means the orchestrator
	// gets no usage data and the cost ledger writes a bare row.
	SurfacesTokenUsage bool

	// TokenUsageSource describes WHERE the adapter obtains usage data so cost
	// auditing knows the fidelity floor. One of:
	//   "stream-json"     — exact, parsed from the CLI's structured output.
	//   "transcript-file" — exact, parsed from a CLI-written transcript on disk.
	//   "estimated"       — approximate; the adapter heuristically guesses.
	// "estimated" callers (e.g. cursor's tmux mode at ~4 chars/token) MUST mark
	// the source so cost reports can be flagged as approximate.
	TokenUsageSource string

	// AdapterReadsTranscript reports whether the adapter has code that reads
	// the CLI's on-disk conversation transcript directly — used for sidecar
	// features (token extraction for tmux mode, replay, forensic audit). This
	// is independent of SupportsNativeResume: a CLI can fully support --resume
	// without us ever reading its files. Only flip true if a transcript
	// reader function is registered in transcriptReaderRegistry.
	AdapterReadsTranscript bool

	// TranscriptPathTemplate is a human-readable hint of where the CLI writes
	// its transcript (e.g. "~/.cursor/chats/<md5(cwd)>/<id>/store.db"). Empty
	// if the adapter doesn't read transcripts. Documentation only — adapters
	// should resolve the actual path via package-local helpers, never by
	// string-formatting this value.
	TranscriptPathTemplate string

	// SupportsStructuredStreaming reports whether the adapter emits STRUCTURED
	// assistant-text (Content) and tool-call (ToolCallStart) stream chunks live
	// during a turn — the design-first, no-terminal streaming path a UI can render
	// without the raw tmux pane. The SOURCE is provider-specific and does not
	// matter to this flag: claude/codex/cursor tail the CLI's on-disk transcript
	// (tagged "<provider>_stream_source":"transcript"), while pi consumes an
	// injected marker stream. It is STRONGER than AdapterReadsTranscript: reading
	// a transcript once for a final-answer/token summary is not streaming. When
	// true, the provider MUST register a CertStructuredStreaming certification and
	// is held to it as a P0 requirement (see RequiredP0CodingAgentCertificationIDs).
	// Flip true only once the live structured stream + its real E2E exist.
	SupportsStructuredStreaming bool

	// RequiresWorkspaceTrust reports whether the CLI can block startup on a
	// trust/auth TUI prompt before any user prompt can be submitted. Some CLIs
	// show a "do you trust the files in this folder?" dialog, or a startup
	// login picker, that the adapter must detect and surface instead of
	// parsing as ready/output. Structured-only CLIs do not render these TUI
	// prompts.
	//
	// Pairs with CertTrustAuthPrompts in codingAgentCapabilityCertifications:
	// when RequiresWorkspaceTrust=true, the provider must have a registered
	// cert (or known-gap allowance) proving the trust handler works in a
	// fresh workspace.
	RequiresWorkspaceTrust bool

	// RestoreAsksInteractivePrompts reports whether the CLI shows an
	// interactive menu (e.g. "Resume from summary? Compact? Discard?")
	// when launched with --resume, which the adapter must navigate before
	// the new prompt can flow. Today only Claude Code does this (its
	// resume-summary menu, dismissed via isClaudeResumeSummaryMenu); other
	// coding CLIs resume silently.
	//
	// Pairs with CertResumeCompactionStartup so adapters that genuinely
	// face this dialog must prove they navigate it. The pairing also lets
	// CLIs that DON'T show the dialog skip an irrelevant cert instead of
	// dragging it into knownCertificationGaps.
	RestoreAsksInteractivePrompts bool

	// APIKeyEnvVars lists the environment variables the CLI accepts to
	// authenticate. Empty means the CLI relies on its own native auth
	// flow (stored credentials, OAuth, etc.) and no env-based shortcut
	// works — e.g. cursor-agent uses cursor.sh's logged-in identity.
	//
	// For multi-provider CLIs, list the canonical primary vars only;
	// provider routing lives in adapter-specific configuration.
	APIKeyEnvVars []string

	// HandlesCtrlCCleanExit reports whether sending Ctrl+C (0x03 keystroke
	// in tmux mode, SIGINT in structured mode) leaves the CLI's persisted
	// chat state intact and resumable. Distinct from SupportsInterrupt,
	// which only proves the interrupt is RECEIVED — this stronger claim
	// is that --resume <id> on the next launch still loads the prior
	// conversation without corruption.
	//
	// Pairs with CertCtrlCStatePreserved. Default is false on every
	// provider today: we haven't yet written the e2e tests that prove
	// state survives a cancel for any CLI. Flip true together with
	// landing the test entry — the drift test in
	// TestAllCodingAgentCapabilityClaimsHaveRegisteredCertification will
	// fail otherwise.
	HandlesCtrlCCleanExit bool

	// WorkingDirInstructionFile names the file the CLI auto-loads as
	// project-level guidance when present in the working directory (e.g.
	// "CLAUDE.md", "AGENTS.md", "GEMINI.md", or for cursor the directory
	// "<workingDir>/.cursor/rules"). Workflow authors can drop this file
	// in their step folders and the CLI follows it without going through
	// the orchestrator's per-session system-prompt injection.
	//
	// DOCUMENTATION ONLY — the orchestrator does NOT write to this path.
	// Per-session system prompts go through each CLI's runtime mechanism
	// (Claude --system-prompt-file, Codex -c model_instructions_file,
	// Cursor .cursor/rules/mlp-system-*.mdc). Confusing the two paths
	// would either leak ephemeral session content into persistent files
	// or block authors from contributing their own static guidance.
	WorkingDirInstructionFile string

	// UserInstructionFile names the home-directory file the CLI auto-loads
	// for the operator's persistent style preferences (e.g.
	// "~/.claude/CLAUDE.md", "~/.codex/AGENTS.md"). DOCUMENTATION ONLY —
	// the orchestrator must never write here. The operator owns this
	// file because it applies to every invocation across every workspace.
	UserInstructionFile string

	// WorkingDirMCPConfigFile names the file/directory the CLI auto-loads
	// for project-scoped MCP server configuration (e.g. ".cursor/mcp.json",
	// ".gemini/settings.json"). The orchestrator writes to these paths today
	// as part of bridge injection — adapters resolve
	// them via package-local helpers (WithCursorMCPConfig, etc.), this
	// field is purely documentation so workflow authors know which files
	// in their step folder are conventionally owned by the orchestrator.
	WorkingDirMCPConfigFile string

	// UserMCPConfigFile names the home-directory file where the operator
	// can register MCP servers that should be visible to every invocation
	// of this CLI (e.g. "~/.codex/config.toml", "~/.gemini/settings.json").
	// DOCUMENTATION ONLY — the orchestrator does not write here.
	UserMCPConfigFile string
}

var codingAgentProviderContracts = map[Provider]CodingAgentProviderContract{
	ProviderClaudeCode: {
		Provider:                      ProviderClaudeCode,
		DisplayName:                   "Claude Code",
		CLIName:                       "claude",
		Transport:                     CodingAgentTransportTmux,
		RequiresWorkingDir:            true,
		RequiresOwnerSessionID:        true,
		UsesPersistentSession:         true,
		SupportsLiveInput:             true,
		SupportsInterrupt:             true,
		SupportsTerminalStream:        true,
		SupportsStatusLine:            true,
		SupportsFinalExtraction:       true,
		SupportsNativeResume:          true,
		UsesMCPBridge:                 true,
		RequiresMCPBridgeConfig:       true,
		SupportsBridgeOnlyTools:       true,
		UsesNativeSystemPrompt:        true,
		LaunchesViaLoginShell:         true,
		ProcessScopedCleanup:          true,
		HandlesTmuxSessionLoss:        true,
		StructuredFallback:            false,
		ImageInputInteractive:         true,
		SurfacesTokenUsage:            true,
		TokenUsageSource:              "transcript-file",
		AdapterReadsTranscript:        true,
		TranscriptPathTemplate:        "~/.claude/projects/*/<session-id>.jsonl",
		SupportsStructuredStreaming:   true,
		RequiresWorkspaceTrust:        true,
		RestoreAsksInteractivePrompts: true,
		// Claude Code authentication is its saved login or a process-scoped
		// workflow OAuth token. Ambient API-key variables are explicitly ignored.
		APIKeyEnvVars:             []string{},
		WorkingDirInstructionFile: "CLAUDE.md",
		UserInstructionFile:       "~/.claude/CLAUDE.md",
		WorkingDirMCPConfigFile:   ".mcp.json",
		UserMCPConfigFile:         "~/.claude/settings.json",
	},
	ProviderCodexCLI: {
		Provider:                ProviderCodexCLI,
		DisplayName:             "Codex CLI",
		CLIName:                 "codex",
		Transport:               CodingAgentTransportTmux,
		RequiresWorkingDir:      true,
		RequiresOwnerSessionID:  true,
		UsesPersistentSession:   true,
		SupportsLiveInput:       true,
		SupportsInterrupt:       true,
		SupportsTerminalStream:  true,
		SupportsStatusLine:      true,
		SupportsFinalExtraction: true,
		// Wired end-to-end: mcpagent.Agent.CodexSessionID is populated by the
		// adapter, session_handle persists it, llmproviders.WithCodexResumeSessionID
		// re-exports the resume option, and server.go's restore switch reads
		// it back via `case "codex-cli":` (server.go:6270). Contract used to
		// say false; the drift test in coding_agent_contract_test.go now
		// enforces this matches the actual wiring.
		SupportsNativeResume:        true,
		UsesMCPBridge:               true,
		RequiresMCPBridgeConfig:     true,
		SupportsBridgeOnlyTools:     true,
		UsesNativeSystemPrompt:      true,
		LaunchesViaLoginShell:       true,
		ProcessScopedCleanup:        true,
		HandlesTmuxSessionLoss:      true,
		StructuredFallback:          false,
		ImageInputInteractive:       true,
		SurfacesTokenUsage:          true,
		TokenUsageSource:            "transcript-file",
		AdapterReadsTranscript:      true,
		TranscriptPathTemplate:      "~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-uuid>.jsonl",
		SupportsStructuredStreaming: true,
		RequiresWorkspaceTrust:      true,
		APIKeyEnvVars:               []string{"CODEX_API_KEY"},
		WorkingDirInstructionFile:   "AGENTS.md",
		UserInstructionFile:         "~/.codex/AGENTS.md",
		WorkingDirMCPConfigFile:     "", // Codex has no project-scoped MCP config file; AgentWorks writes a unique per-invocation $CODEX_HOME/<name>.config.toml profile.
		UserMCPConfigFile:           "~/.codex/config.toml",
	},
	ProviderCursorCLI: {
		Provider:                ProviderCursorCLI,
		DisplayName:             "Cursor CLI",
		CLIName:                 "cursor-agent",
		Transport:               CodingAgentTransportTmux,
		RequiresWorkingDir:      true,
		RequiresOwnerSessionID:  true,
		UsesPersistentSession:   true,
		SupportsLiveInput:       true,
		SupportsInterrupt:       true,
		SupportsTerminalStream:  true,
		SupportsFinalExtraction: true,
		// Wired end-to-end: mcpagent.Agent.CursorSessionID is populated by
		// the tmux adapter from Cursor's local store.db sidecar,
		// session_handle persists it, llmproviders.WithCursorResumeSessionID
		// re-exports the resume option, and server.go's restore switch
		// reads it back via `case "cursor-cli":`. The drift test in
		// coding_agent_contract_test.go enforces this matches the actual
		// wiring (nativeResumeRegistry membership).
		SupportsNativeResume:    true,
		UsesMCPBridge:           true,
		RequiresMCPBridgeConfig: true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   true,
		ProcessScopedCleanup:    true,
		HandlesTmuxSessionLoss:  true,
		StructuredFallback:      false,
		ImageInputInteractive:   true,
		SurfacesTokenUsage:      true,
		// Cursor's tmux interactive path falls back to a 4-chars-per-token
		// heuristic in estimateCursorTmuxTokens.
		TokenUsageSource:            "estimated",
		AdapterReadsTranscript:      true,
		TranscriptPathTemplate:      "~/.cursor/chats/<md5(cwd)>/<agentId>/store.db",
		SupportsStructuredStreaming: true,
		RequiresWorkspaceTrust:      true,
		APIKeyEnvVars:               []string{"CURSOR_API_KEY"},
		WorkingDirInstructionFile:   ".cursor/rules",    // directory of .mdc rule files; Cursor honors every file in here when alwaysApply:true is set in frontmatter.
		UserInstructionFile:         "~/.cursor/rules",  // same directory layout at the user level
		WorkingDirMCPConfigFile:     ".cursor/mcp.json", // adapter writes this when WithCursorMCPConfig is provided
		UserMCPConfigFile:           "~/.cursor/mcp.json",
	},
	ProviderPiCLI: {
		Provider:                    ProviderPiCLI,
		DisplayName:                 "Pi CLI",
		CLIName:                     "pi",
		Transport:                   CodingAgentTransportTmux,
		RequiresWorkingDir:          true,
		RequiresOwnerSessionID:      true,
		UsesPersistentSession:       true,
		SupportsLiveInput:           true,
		SupportsInterrupt:           true,
		SupportsTerminalStream:      true,
		SupportsStatusLine:          true,
		SupportsFinalExtraction:     true,
		SupportsNativeResume:        true,
		UsesMCPBridge:               true,
		RequiresMCPBridgeConfig:     true,
		SupportsBridgeOnlyTools:     true,
		UsesNativeSystemPrompt:      true,
		LaunchesViaLoginShell:       true,
		ProcessScopedCleanup:        true,
		HandlesTmuxSessionLoss:      true,
		StructuredFallback:          false,
		ImageInputInteractive:       false,
		SurfacesTokenUsage:          true,
		TokenUsageSource:            "transcript-file",
		AdapterReadsTranscript:      true,
		TranscriptPathTemplate:      "$PI_CODING_AGENT_SESSION_DIR/**/*_<session-id>.jsonl or ~/.pi/agent/sessions/**/*_<session-id>.jsonl",
		SupportsStructuredStreaming: true,
		APIKeyEnvVars:               []string{"PI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"},
		// Pi follows AGENTS.md-style project instructions, but this adapter
		// injects per-session system guidance through --append-system-prompt
		// rather than writing durable project files. MCP is provided through
		// the pi-mcp-adapter extension and a restored session-scoped .pi/mcp.json
		// project override; --no-builtin-tools provides the bridge-only gate.
		WorkingDirInstructionFile: "AGENTS.md",
		UserInstructionFile:       "~/.pi/agent/AGENTS.md",
		WorkingDirMCPConfigFile:   ".pi/mcp.json",
		UserMCPConfigFile:         "~/.pi/agent/mcp.json",
	},
}

// GetCodingAgentProviderContract returns the shared coding-agent contract for a
// provider/model pair.
func GetCodingAgentProviderContract(provider Provider, modelID string) (CodingAgentProviderContract, bool) {
	normalizedProvider := Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	contract, ok := codingAgentProviderContracts[normalizedProvider]
	if !ok {
		return CodingAgentProviderContract{}, false
	}

	if strings.TrimSpace(contract.ModelID) != "" && !strings.EqualFold(strings.TrimSpace(modelID), contract.ModelID) {
		return CodingAgentProviderContract{}, false
	}
	return contract, true
}

// IsCodingAgentProvider reports whether provider/model should be treated as a
// coding agent by host applications.
func IsCodingAgentProvider(provider Provider, modelID string) bool {
	_, ok := GetCodingAgentProviderContract(provider, modelID)
	return ok
}

// IsTmuxCodingAgentProvider reports whether provider/model uses the persistent
// tmux contract.
func IsTmuxCodingAgentProvider(provider Provider, modelID string) bool {
	contract, ok := GetCodingAgentProviderContract(provider, modelID)
	return ok && contract.Transport == CodingAgentTransportTmux
}

// CodingAgentProviderContracts returns all currently declared coding-agent
// contracts.
func CodingAgentProviderContracts() []CodingAgentProviderContract {
	contracts := make([]CodingAgentProviderContract, 0, len(codingAgentProviderContracts))
	for _, contract := range codingAgentProviderContracts {
		contracts = append(contracts, contract)
	}
	sort.Slice(contracts, func(i, j int) bool {
		if contracts[i].Provider == contracts[j].Provider {
			return contracts[i].ModelID < contracts[j].ModelID
		}
		return contracts[i].Provider < contracts[j].Provider
	})
	return contracts
}
