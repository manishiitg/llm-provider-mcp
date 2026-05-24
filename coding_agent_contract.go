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

	RequiresWorkingDir      bool
	RequiresOwnerSessionID  bool
	UsesPersistentSession   bool
	SupportsLiveInput       bool
	SupportsInterrupt       bool
	SupportsTerminalStream  bool
	SupportsFinalExtraction bool
	SupportsNativeResume    bool
	UsesMCPBridge           bool
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
}

var codingAgentProviderContracts = map[Provider]CodingAgentProviderContract{
	ProviderClaudeCode: {
		Provider:                ProviderClaudeCode,
		DisplayName:             "Claude Code",
		CLIName:                 "claude",
		Transport:               CodingAgentTransportTmux,
		RequiresWorkingDir:      true,
		RequiresOwnerSessionID:  true,
		UsesPersistentSession:   true,
		SupportsLiveInput:       true,
		SupportsInterrupt:       true,
		SupportsTerminalStream:  true,
		SupportsFinalExtraction: true,
		SupportsNativeResume:    true,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   true,
		ProcessScopedCleanup:    true,
		HandlesTmuxSessionLoss:  true,
		StructuredFallback:      true,
		ImageInputInteractive:   true,
		SurfacesTokenUsage:      true,
		TokenUsageSource:        "stream-json",
		AdapterReadsTranscript:  false,
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
		SupportsFinalExtraction: true,
		// Wired end-to-end: mcpagent.Agent.CodexSessionID is populated by the
		// adapter, session_handle persists it, llmproviders.WithCodexResumeSessionID
		// re-exports the resume option, and server.go's restore switch reads
		// it back via `case "codex-cli":` (server.go:6270). Contract used to
		// say false; the drift test in coding_agent_contract_test.go now
		// enforces this matches the actual wiring.
		SupportsNativeResume:    true,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   true,
		ProcessScopedCleanup:    true,
		HandlesTmuxSessionLoss:  true,
		StructuredFallback:      true,
		ImageInputInteractive:   true,
		SurfacesTokenUsage:      true,
		TokenUsageSource:        "stream-json",
		AdapterReadsTranscript:  false,
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
		// the structured adapter from cursor's stream-json init event,
		// session_handle persists it, llmproviders.WithCursorResumeSessionID
		// re-exports the resume option, and server.go's restore switch
		// reads it back via `case "cursor-cli":`. The drift test in
		// coding_agent_contract_test.go enforces this matches the actual
		// wiring (nativeResumeRegistry membership).
		SupportsNativeResume:    true,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   true,
		ProcessScopedCleanup:    true,
		HandlesTmuxSessionLoss:  true,
		StructuredFallback:      true,
		ImageInputInteractive:   true,
		SurfacesTokenUsage:      true,
		// Cursor's structured (--print) path parses stream-json exactly; the
		// tmux interactive path falls back to a 4-chars-per-token heuristic
		// in estimateCursorTmuxTokens. We classify by the higher-fidelity
		// canonical path. Reports from tmux-mode runs should be flagged as
		// approximate at the cost-ledger layer.
		TokenUsageSource:       "stream-json",
		AdapterReadsTranscript: true,
		TranscriptPathTemplate: "~/.cursor/chats/<md5(cwd)>/<agentId>/store.db",
	},
	ProviderGeminiCLI: {
		Provider:    ProviderGeminiCLI,
		DisplayName: "Gemini CLI",
		CLIName:     "gemini",
		// Gemini CLI is being deprecated by Google. We pin it to the structured
		// transport so we don't carry tmux-specific bug surface for a CLI that's
		// going away — matches the OpenCode CLI contract shape.
		Transport:               CodingAgentTransportStructured,
		RequiresWorkingDir:      true,
		RequiresOwnerSessionID:  false,
		UsesPersistentSession:   false,
		SupportsLiveInput:       false,
		SupportsInterrupt:       false,
		SupportsTerminalStream:  false,
		SupportsFinalExtraction: true,
		SupportsNativeResume:    true,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   false,
		ProcessScopedCleanup:    false,
		HandlesTmuxSessionLoss:  false,
		StructuredFallback:      true,
		ImageInputInteractive:   false,
		SurfacesTokenUsage:      true,
		TokenUsageSource:        "transcript-file",
		AdapterReadsTranscript:  true,
		TranscriptPathTemplate:  "~/.gemini/tmp/gemini-cli-project-<projectDirID>/chats/session-*.jsonl",
	},
	ProviderOpenCodeCLI: {
		Provider:                ProviderOpenCodeCLI,
		DisplayName:             "OpenCode CLI",
		CLIName:                 "opencode",
		Transport:               CodingAgentTransportStructured,
		RequiresWorkingDir:      true,
		RequiresOwnerSessionID:  false,
		UsesPersistentSession:   false,
		SupportsLiveInput:       false,
		SupportsInterrupt:       false,
		SupportsTerminalStream:  false,
		SupportsFinalExtraction: true,
		SupportsNativeResume:    true,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   false,
		ProcessScopedCleanup:    false,
		HandlesTmuxSessionLoss:  false,
		StructuredFallback:      true,
		ImageInputInteractive:   true,
		SurfacesTokenUsage:      true,
		TokenUsageSource:        "stream-json",
		AdapterReadsTranscript:  false,
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
