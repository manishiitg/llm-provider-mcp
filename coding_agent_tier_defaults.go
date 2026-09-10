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
		builder := codingAgentReasoningRef(providerID, "gpt-6-astra", "medium")
		medium := codingAgentReasoningRef(providerID, "gpt-5.6-luna", "high")
		low := codingAgentReasoningRef(providerID, "gpt-5.6-luna", "medium")
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
		builder := high
		return &CodingAgentDefaultTierModels{
			Builder: builder,
			High:    high,
			Medium:  medium,
			Low:     codingAgentReasoningRef(providerID, "claude-haiku-4-5-20251001", "medium"),
			Pulse:   builder,
		}, true
	case ProviderCursorCLI:
		// All tiers on Cursor's auto routing (product decision 2026-09-03,
		// reversing the earlier grok-4.6 pin for Builder/High/Pulse: a live
		// RTS run hit "quota_exhausted" on grok-4.6 with no fallback, so pick
		// a model that always has capacity rather than pinning one that can
		// run out).
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
		// Initial mapping from the on-disk catalog (2026-09-10): the
		// default+current contributor build carries Builder/High/Pulse,
		// 1.3 takes Medium, 1.2 takes Low. Effort calibration is TBD
		// pending live runs.
		contributor := codingAgentHighReasoningRef(providerID, "muse-spark-1.3-contributor")
		return &CodingAgentDefaultTierModels{
			Builder: contributor,
			High:    contributor,
			Medium:  codingAgentReasoningRef(providerID, "muse-spark-1.3", "medium"),
			Low:     codingAgentReasoningRef(providerID, "muse-spark-1.2", "medium"),
			Pulse:   contributor,
		}, true
	}

	return nil, false
}
