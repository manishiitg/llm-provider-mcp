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
		high := codingAgentReasoningRef(providerID, "gpt-6.1-terra", "medium")
		builder := codingAgentReasoningRef(providerID, "gpt-6.1-sol", "high")
		medium := codingAgentReasoningRef(providerID, "gpt-6.1-terra", "high")
		low := codingAgentReasoningRef(providerID, "gpt-6-luna", "high")
		return &CodingAgentDefaultTierModels{
			Builder: builder,
			High:    high,
			Medium:  medium,
			Low:     low,
			Pulse:   builder,
		}, true
	case ProviderClaudeCode:
		// Medium and Low run Haiku 5.5 (owner, 2026-10-16): high effort for
		// Medium, medium effort for Low; Builder, High and Pulse stay Sonnet.
		high := codingAgentHighReasoningRef(providerID, "claude-sonnet-5-5")
		medium := codingAgentReasoningRef(providerID, "claude-haiku-5-5", "high")
		builder := codingAgentReasoningRef(providerID, "claude-sonnet-5-5", "medium")
		return &CodingAgentDefaultTierModels{
			Builder: builder,
			High:    high,
			Medium:  medium,
			Low:     codingAgentReasoningRef(providerID, "claude-haiku-5-5", "medium"),
			Pulse:   high,
		}, true
	case ProviderCursorCLI:
		// Use the same certified default as ordinary Cursor chats. Auto is
		// still selectable when a person's account offers it (PLAT-872).
		model := codingAgentHighReasoningRef(providerID, DefaultCursorCLIModel)
		return &CodingAgentDefaultTierModels{
			Builder: model,
			High:    model,
			Medium:  model,
			Low:     model,
			Pulse:   model,
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
		// Use regular Spark 1.3 for every role, retaining each role's effort.
		const museModel = DefaultMuseCLIModel
		return &CodingAgentDefaultTierModels{
			Builder: codingAgentReasoningRef(providerID, museModel, "max"),
			High:    codingAgentReasoningRef(providerID, museModel, "xhigh"),
			Medium:  codingAgentReasoningRef(providerID, museModel, "high"),
			Low:     codingAgentReasoningRef(providerID, museModel, "medium"),
			Pulse:   codingAgentReasoningRef(providerID, museModel, "max"),
		}, true
	case ProviderAgyCLI:
		// Effort is baked into agy's model slugs (high/medium/low); Builder
		// and Pulse run the flagship, matching `agy models` output.
		const agyModel = "gemini-3.8-flash"
		return &CodingAgentDefaultTierModels{
			Builder: codingAgentReasoningRef(providerID, agyModel+"-high", "high"),
			High:    codingAgentReasoningRef(providerID, agyModel+"-high", "high"),
			Medium:  codingAgentReasoningRef(providerID, agyModel+"-medium", "medium"),
			Low:     codingAgentReasoningRef(providerID, agyModel+"-low", "low"),
			Pulse:   codingAgentReasoningRef(providerID, agyModel+"-high", "high"),
		}, true
	}

	return nil, false
}
