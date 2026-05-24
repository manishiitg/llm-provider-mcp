package llmproviders

import (
	"context"
	"fmt"
	"strings"
)

// allowedCodingAgentControlKeys is the whitelist of tmux key names accepted by
// SendCodingAgentControlKey. Keep this short — adding keys broadens the attack
// surface for stray UI events to disrupt a live CLI pane.
var allowedCodingAgentControlKeys = map[string]struct{}{
	"Escape": {},
	"C-c":    {},
}

// IsAllowedCodingAgentControlKey reports whether key is on the whitelist
// accepted by SendCodingAgentControlKey. Useful for early validation at the
// HTTP boundary.
func IsAllowedCodingAgentControlKey(key string) bool {
	_, ok := allowedCodingAgentControlKeys[strings.TrimSpace(key)]
	return ok
}

// SendCodingAgentControlKey injects a tmux control key (e.g. "Escape", "C-c")
// into a currently running coding-agent transport. Parallels
// SendCodingAgentLiveInput but sends a raw key instead of text. Returns a
// CodingAgentContinuationError for non-applicable providers and a provider-
// specific error if no live session is registered for the owner.
func SendCodingAgentControlKey(ctx context.Context, provider Provider, modelID, ownerSessionID, key string) error {
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
	trimmedKey := strings.TrimSpace(key)
	if !IsAllowedCodingAgentControlKey(trimmedKey) {
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: normalizedProvider,
			Reason:   fmt.Sprintf("control key %q is not allowed", trimmedKey),
		}
	}

	switch normalizedProvider {
	case ProviderClaudeCode:
		return SendClaudeCodeExperimentalControlKey(ctx, ownerSessionID, trimmedKey)
	case ProviderCodexCLI:
		return SendCodexCLIInteractiveControlKey(ctx, ownerSessionID, trimmedKey)
	case ProviderGeminiCLI:
		return SendGeminiCLIInteractiveControlKey(ctx, ownerSessionID, trimmedKey)
	case ProviderCursorCLI:
		return SendCursorCLIInteractiveControlKey(ctx, ownerSessionID, trimmedKey)
	case ProviderAgyCLI:
		return SendAgyCLIInteractiveControlKey(ctx, ownerSessionID, trimmedKey)
	case ProviderOpenCodeCLI:
		return SendOpenCodeCLIInteractiveControlKey(ctx, ownerSessionID, trimmedKey)
	default:
		return &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: normalizedProvider,
			Reason:   "provider has no live input transport",
		}
	}
}
