package llmproviders

import "strings"

// CodingAgentTierModelRef is the provider-level default for a workflow tier.
type CodingAgentTierModelRef struct {
	Provider string                 `json:"provider"`
	ModelID  string                 `json:"model_id"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// CodingAgentDefaultTierModels describes the builder/high/medium/low,
// maintenance, pulse, and Chief of Staff defaults a coding-agent profile exposes
// to downstream workflow UIs.
type CodingAgentDefaultTierModels struct {
	Builder      CodingAgentTierModelRef `json:"builder"`
	High         CodingAgentTierModelRef `json:"high"`
	Medium       CodingAgentTierModelRef `json:"medium"`
	Low          CodingAgentTierModelRef `json:"low"`
	Maintenance  CodingAgentTierModelRef `json:"maintenance"`
	Pulse        CodingAgentTierModelRef `json:"pulse"`
	ChiefOfStaff CodingAgentTierModelRef `json:"chief_of_staff"`
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

func sameCodingAgentTierModels(provider, modelID string) *CodingAgentDefaultTierModels {
	if strings.TrimSpace(modelID) == "" {
		modelID = provider
	}
	ref := codingAgentHighReasoningRef(provider, modelID)
	return &CodingAgentDefaultTierModels{
		Builder:      ref,
		High:         ref,
		Medium:       ref,
		Low:          ref,
		Maintenance:  ref,
		Pulse:        ref,
		ChiefOfStaff: ref,
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
		high := codingAgentReasoningRef(providerID, "gpt-5.6-sol", "xhigh")
		maintenance := high
		builder := codingAgentHighReasoningRef(providerID, "gpt-5.6-terra")
		pulse := codingAgentHighReasoningRef(providerID, "gpt-5.6-terra")
		medium := codingAgentReasoningRef(providerID, "gpt-5.6-terra", "medium")
		low := codingAgentReasoningRef(providerID, "gpt-5.6-luna", "low")
		return &CodingAgentDefaultTierModels{
			Builder:      builder,
			High:         high,
			Medium:       medium,
			Low:          low,
			Maintenance:  maintenance,
			Pulse:        pulse,
			ChiefOfStaff: high,
		}, true
	case ProviderClaudeCode:
		high := codingAgentHighReasoningRef(providerID, "claude-opus-4-8")
		pulse := codingAgentHighReasoningRef(providerID, "claude-sonnet-5")
		builder := pulse
		return &CodingAgentDefaultTierModels{
			Builder:      builder,
			High:         high,
			Medium:       codingAgentHighReasoningRef(providerID, "claude-sonnet-5"),
			Low:          codingAgentHighReasoningRef(providerID, "claude-haiku-4-5-20251001"),
			Maintenance:  high,
			Pulse:        pulse,
			ChiefOfStaff: high,
		}, true
	case ProviderGeminiCLI:
		high := codingAgentHighReasoningRef(providerID, "high")
		return &CodingAgentDefaultTierModels{
			Builder:      high,
			High:         high,
			Medium:       codingAgentHighReasoningRef(providerID, "medium"),
			Low:          codingAgentHighReasoningRef(providerID, "low"),
			Maintenance:  high,
			Pulse:        high,
			ChiefOfStaff: high,
		}, true
	case ProviderCursorCLI:
		high := codingAgentHighReasoningRef(providerID, "grok-4.5")
		medium := codingAgentHighReasoningRef(providerID, DefaultCursorCLIModel)
		low := codingAgentHighReasoningRef(providerID, "auto")
		return &CodingAgentDefaultTierModels{
			Builder:      high,
			High:         high,
			Medium:       medium,
			Low:          low,
			Maintenance:  high,
			Pulse:        high,
			ChiefOfStaff: high,
		}, true
	case ProviderAgyCLI:
		return sameCodingAgentTierModels(providerID, DefaultAgyCLIModel), true
	case ProviderPiCLI:
		high := codingAgentHighReasoningRef(providerID, "google/gemini-3.5-flash")
		return &CodingAgentDefaultTierModels{
			Builder:      high,
			High:         high,
			Medium:       codingAgentHighReasoningRef(providerID, "google/gemini-3.5-flash"),
			Low:          codingAgentHighReasoningRef(providerID, DefaultPiCLIModel),
			Maintenance:  high,
			Pulse:        high,
			ChiefOfStaff: high,
		}, true
	}

	return nil, false
}
