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
	// model/transport, such as kimi/kimi-code.
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
		ImageInputInteractive:   false,
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
		SupportsNativeResume:    false,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   true,
		ProcessScopedCleanup:    true,
		HandlesTmuxSessionLoss:  true,
		StructuredFallback:      true,
		ImageInputInteractive:   false,
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
		SupportsNativeResume:    false,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   true,
		ProcessScopedCleanup:    true,
		HandlesTmuxSessionLoss:  true,
		StructuredFallback:      true,
		ImageInputInteractive:   true,
	},
	ProviderGeminiCLI: {
		Provider:                ProviderGeminiCLI,
		DisplayName:             "Gemini CLI",
		CLIName:                 "gemini",
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
		ImageInputInteractive:   false,
	},
	ProviderOpenCodeCLI: {
		Provider:                ProviderOpenCodeCLI,
		DisplayName:             "OpenCode CLI",
		CLIName:                 "opencode",
		Transport:               CodingAgentTransportTmux,
		RequiresWorkingDir:      true,
		RequiresOwnerSessionID:  true,
		UsesPersistentSession:   true,
		SupportsLiveInput:       true,
		SupportsInterrupt:       true,
		SupportsTerminalStream:  true,
		SupportsFinalExtraction: true,
		SupportsNativeResume:    false,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   true,
		ProcessScopedCleanup:    true,
		HandlesTmuxSessionLoss:  true,
		StructuredFallback:      true,
		ImageInputInteractive:   false,
	},
	ProviderKimi: {
		Provider:                ProviderKimi,
		ModelID:                 "kimi-code",
		DisplayName:             "Kimi Code",
		CLIName:                 "kimi",
		Transport:               CodingAgentTransportStructured,
		RequiresWorkingDir:      true,
		RequiresOwnerSessionID:  false,
		UsesPersistentSession:   false,
		SupportsLiveInput:       false,
		SupportsInterrupt:       false,
		SupportsTerminalStream:  false,
		SupportsFinalExtraction: true,
		SupportsNativeResume:    false,
		UsesMCPBridge:           true,
		SupportsBridgeOnlyTools: true,
		UsesNativeSystemPrompt:  true,
		LaunchesViaLoginShell:   false,
		ProcessScopedCleanup:    false,
		StructuredFallback:      true,
		ImageInputInteractive:   false,
	},
}

// GetCodingAgentProviderContract returns the shared coding-agent contract for a
// provider/model pair. Provider Kimi is model-scoped: kimi/kimi-code is a coding
// agent, while normal Kimi API models are not.
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
// contracts. Model-scoped entries such as kimi/kimi-code are included.
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
