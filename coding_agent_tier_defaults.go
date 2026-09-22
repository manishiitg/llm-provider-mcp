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
		high := codingAgentReasoningRef(providerID, "gpt-6-sol", "medium")
		builder := codingAgentReasoningRef(providerID, "gpt-6-sol", "high")
		medium := codingAgentReasoningRef(providerID, "gpt-6-luna", "high")
		low := codingAgentReasoningRef(providerID, "gpt-6-luna", "high")
		return &CodingAgentDefaultTierModels{
			Builder: builder,
			High:    high,
			Medium:  medium,
			Low:     low,
			Pulse:   builder,
		}, true
	case ProviderClaudeCode:
		high := codingAgentHighReasoningRef(providerID, "claude-sonnet-5")
		medium := codingAgentReasoningRef(providerID, "claude-sonnet-5", "medium")
		builder := codingAgentReasoningRef(providerID, "claude-opus-5-5", "medium")
		return &CodingAgentDefaultTierModels{
			Builder: builder,
			High:    high,
			Medium:  medium,
			Low:     codingAgentReasoningRef(providerID, "claude-sonnet-5", "low"),
			Pulse:   high,
		}, true
	case ProviderCursorCLI:
		// Auto routing avoids pinning a model that can run out of quota.
		auto := codingAgentHighReasoningRef(providerID, "auto")
		return &CodingAgentDefaultTierModels{
			Builder: auto,
			High:    auto,
			Medium:  auto,
			Low:     auto,
			Pulse:   auto,
		}, true
	case ProviderPiCLI:
		return &CodingAgentDefaultTierModels{
			Builder: codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "high"),
			High:    codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "high"),
			Medium:  codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "medium"),
			Low:     codingAgentReasoningRef(providerID, "google/gemini-3.5-flash-lite", "low"),
			Pulse:   codingAgentReasoningRef(providerID, "google/gemini-3.8-flash", "high"),
		}, true
	case ProviderMuseCLI:
		// Single model for now (2026-09-10): every tier runs
		// muse-spark-1.3-contributor, with max for the long-horizon Builder
		// and Pulse roles, xhigh for High, high for Medium, and medium for Low.
		const museModel = "muse-spark-1.3-contributor"
		return &CodingAgentDefaultTierModels{
			Builder: codingAgentReasoningRef(providerID, museModel, "max"),
			High:    codingAgentReasoningRef(providerID, museModel, "xhigh"),
			Medium:  codingAgentReasoningRef(providerID, museModel, "high"),
			Low:     codingAgentReasoningRef(providerID, museModel, "medium"),
			Pulse:   codingAgentReasoningRef(providerID, museModel, "max"),
		}, true
	}

	return nil, false
}
