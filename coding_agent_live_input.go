package llmproviders

import (
	"context"
	"strings"
)

// SendCodingAgentLiveInput sends a user message to a currently running coding
// agent transport. Host applications should call this typed entry point instead
// of switching over provider-specific tmux implementations themselves.
func SendCodingAgentLiveInput(ctx context.Context, provider Provider, modelID, ownerSessionID, message string) error {
	normalizedProvider := Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	contract, ok := GetCodingAgentProviderContract(normalizedProvider, modelID)
	if !ok {
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonApplicable,
			Provider: normalizedProvider,
			Reason:   "provider is not a coding-agent provider",
		}
	}
	if !contract.SupportsLiveInput {
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: normalizedProvider,
			Reason:   "provider transport does not support live input",
		}
	}
	if strings.TrimSpace(ownerSessionID) == "" {
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: normalizedProvider,
			Reason:   "owner session id is required",
		}
	}
	if strings.TrimSpace(message) == "" {
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: normalizedProvider,
			Reason:   "message is empty",
		}
	}

	switch normalizedProvider {
	case ProviderClaudeCode:
		return SendClaudeCodeExperimentalInput(ctx, ownerSessionID, message)
	case ProviderCodexCLI:
		return SendCodexCLIInteractiveInput(ctx, ownerSessionID, message)
	case ProviderGeminiCLI:
		return SendGeminiCLIInteractiveInput(ctx, ownerSessionID, message)
	case ProviderCursorCLI:
		return SendCursorCLIInteractiveInput(ctx, ownerSessionID, message)
	case ProviderAgyCLI:
		return SendAgyCLIInteractiveInput(ctx, ownerSessionID, message)
	case ProviderOpenCodeCLI:
		return SendOpenCodeCLIInteractiveInput(ctx, ownerSessionID, message)
	default:
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: normalizedProvider,
			Reason:   "provider has no live input transport",
		}
	}
}
