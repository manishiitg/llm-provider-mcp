package llmproviders

import "strings"

// CodingAgentTierModelRef is the provider-level default for a workflow tier.
type CodingAgentTierModelRef struct {
	Provider string                 `json:"provider"`
	ModelID  string                 `json:"model_id"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// CodingAgentDefaultTierModels describes the builder/high/medium/low and pulse
// defaults a coding-agent profile exposes to downstream
// workflow UIs.
type CodingAgentDefaultTierModels struct {
	Builder CodingAgentTierModelRef `json:"builder"`
	High    CodingAgentTierModelRef `json:"high"`
	Medium  CodingAgentTierModelRef `json:"medium"`
	Low     CodingAgentTierModelRef `json:"low"`
	Pulse   CodingAgentTierModelRef `json:"pulse"`
}

func codingAgentHighReasoningRef(provider, modelID string) CodingAgentTierModelRef {
	return codingAgentReasoningRef(provider, modelID, "high")
}

func codingAgentReasoningRef(provider, modelID, effort string) CodingAgentTierModelRef {
	return CodingAgentTierModelRef{
		Provider: provider,
		ModelID:  modelID,
		Options:  map[string]interface{}{"reasoning_effort": effort},
	}
}

// GetCodingAgentDefaultTierModels returns the provider-owned workflow tier
// defaults for coding-agent providers. Phase intentionally follows high.
//
// Update ritual when a coding-agent model changes:
//   - update the provider's GetAll*Models registry so the selector is visible
//     to UI/API callers;
//   - update these tier defaults;
//   - run TestCodingAgentDefaultTierModelsArePublished so stale hidden model
//     IDs fail before release.
func GetCodingAgentDefaultTierModels(provider Provider) (*CodingAgentDefaultTierModels, bool) {
	providerID := strings.TrimSpace(string(provider))

	switch Provider(providerID) {
	case ProviderCodexCLI:
		high := codingAgentReasoningRef(providerID, "gpt-5.6-terra", "medium")
		builder := codingAgentHighReasoningRef(providerID, "gpt-5.6-sol")
		pulse := codingAgentReasoningRef(providerID, "gpt-5.6-terra", "high")
		medium := codingAgentReasoningRef(providerID, "gpt-5.6-luna", "high")
		low := codingAgentReasoningRef(providerID, "gpt-5.6-luna", "medium")
		return &CodingAgentDefaultTierModels{
			Builder: builder,
			High:    high,
			Medium:  medium,
			Low:     low,
			Pulse:   pulse,
		}, true
	case ProviderClaudeCode:
		high := codingAgentHighReasoningRef(providerID, "claude-sonnet-5")
		medium := codingAgentReasoningRef(providerID, "claude-sonnet-5", "medium")
		pulse := codingAgentHighReasoningRef(providerID, "claude-opus-5")
		builder := high
		return &CodingAgentDefaultTierModels{
			Builder: builder,
			High:    high,
			Medium:  medium,
			Low:     codingAgentReasoningRef(providerID, "claude-haiku-4-5-20251001", "medium"),
			Pulse:   pulse,
		}, true
	case ProviderCursorCLI:
		high := codingAgentHighReasoningRef(providerID, "grok-4.6")
		medium := codingAgentHighReasoningRef(providerID, DefaultCursorCLIModel)
		low := codingAgentHighReasoningRef(providerID, "auto")
		return &CodingAgentDefaultTierModels{
			Builder: high,
			High:    high,
			Medium:  medium,
			Low:     low,
			Pulse:   high,
		}, true
	case ProviderPiCLI:
		return &CodingAgentDefaultTierModels{
			Builder: codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "high"),
			High:    codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "high"),
			Medium:  codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "medium"),
			Low:     codingAgentReasoningRef(providerID, "google/gemini-3.5-flash-lite", "low"),
			Pulse:   codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "high"),
		}, true
	}

	return nil, false
}
