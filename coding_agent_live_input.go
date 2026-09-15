package llmproviders

import (
	"context"
	"strings"
)

// SendCodingAgentLiveInput sends a user message to a currently running coding
// agent transport. Host applications should call this typed entry point instead
// of switching over provider-specific tmux implementations themselves.
func SendCodingAgentLiveInput(ctx context.Context, provider Provider, modelID, ownerSessionID, message string) error {
	return sendCodingAgentInput(ctx, provider, modelID, ownerSessionID, message, false)
}

// SendCodingAgentRetainedInput starts a new logical turn in a retained coding
// terminal. Pi needs this distinction because live steering is accepted while
// busy, whereas retained recovery must wait for the idle composer.
func SendCodingAgentRetainedInput(ctx context.Context, provider Provider, modelID, ownerSessionID, message string) error {
	return sendCodingAgentInput(ctx, provider, modelID, ownerSessionID, message, true)
}

func sendCodingAgentInput(ctx context.Context, provider Provider, modelID, ownerSessionID, message string, retainedTurn bool) error {
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
		return SendClaudeCodeInput(ctx, ownerSessionID, message)
	case ProviderCodexCLI:
		return SendCodexCLIInteractiveInput(ctx, ownerSessionID, message)
	case ProviderCursorCLI:
		return SendCursorCLIInteractiveInput(ctx, ownerSessionID, message)
	case ProviderPiCLI:
		if retainedTurn {
			return SendPiCLIRetainedInput(ctx, ownerSessionID, message)
		}
		return SendPiCLIInteractiveInput(ctx, ownerSessionID, message)
	case ProviderMuseCLI:
		return SendMuseCLIInteractiveInput(ctx, ownerSessionID, message)
	default:
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: normalizedProvider,
			Reason:   "provider has no live input transport",
		}
	}
}
