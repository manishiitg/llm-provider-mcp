package llmproviders

import "strings"

// CodingAgentTierModelRef is the provider-level default for a workflow tier.
type CodingAgentTierModelRef struct {
	Provider string                 `json:"provider"`
	ModelID  string                 `json:"model_id"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// CodingAgentDefaultTierModels describes the main/high/medium/low/phase and
// auto-improve defaults a coding-agent plan exposes to downstream workflow UIs.
type CodingAgentDefaultTierModels struct {
	Main        CodingAgentTierModelRef `json:"main"`
	High        CodingAgentTierModelRef `json:"high"`
	Medium      CodingAgentTierModelRef `json:"medium"`
	Low         CodingAgentTierModelRef `json:"low"`
	Phase       CodingAgentTierModelRef `json:"phase"`
	AutoImprove CodingAgentTierModelRef `json:"auto_improve"`
}

func codingAgentHighReasoningRef(provider, modelID string) CodingAgentTierModelRef {
	return CodingAgentTierModelRef{
		Provider: provider,
		ModelID:  modelID,
		Options:  map[string]interface{}{"reasoning_effort": "high"},
	}
}

func sameCodingAgentTierModels(provider, modelID string) *CodingAgentDefaultTierModels {
	if strings.TrimSpace(modelID) == "" {
		modelID = provider
	}
	ref := codingAgentHighReasoningRef(provider, modelID)
	return &CodingAgentDefaultTierModels{
		Main:        ref,
		High:        ref,
		Medium:      ref,
		Low:         ref,
		Phase:       ref,
		AutoImprove: ref,
	}
}

// GetCodingAgentDefaultTierModels returns the provider-owned workflow tier
// defaults for coding-agent providers. Phase intentionally follows high.
func GetCodingAgentDefaultTierModels(provider Provider) (*CodingAgentDefaultTierModels, bool) {
	providerID := strings.TrimSpace(string(provider))

	switch Provider(providerID) {
	case ProviderCodexCLI:
		high := codingAgentHighReasoningRef(providerID, "gpt-5.5")
		autoImprove := codingAgentHighReasoningRef(providerID, "gpt-5.5")
		autoImprove.Options = map[string]interface{}{"reasoning_effort": "xhigh"}
		return &CodingAgentDefaultTierModels{
			Main:        high,
			High:        high,
			Medium:      codingAgentHighReasoningRef(providerID, "gpt-5.4"),
			Low:         codingAgentHighReasoningRef(providerID, "gpt-5.3-codex-spark"),
			Phase:       high,
			AutoImprove: autoImprove,
		}, true
	case ProviderClaudeCode:
		high := codingAgentHighReasoningRef(providerID, "claude-opus-4-8")
		return &CodingAgentDefaultTierModels{
			Main:        codingAgentHighReasoningRef(providerID, "claude-code"),
			High:        high,
			Medium:      codingAgentHighReasoningRef(providerID, "claude-sonnet-5"),
			Low:         codingAgentHighReasoningRef(providerID, "claude-haiku-4-5-20251001"),
			Phase:       high,
			AutoImprove: codingAgentHighReasoningRef(providerID, "claude-fable-5"),
		}, true
	case ProviderGeminiCLI:
		high := codingAgentHighReasoningRef(providerID, "high")
		return &CodingAgentDefaultTierModels{
			Main:        codingAgentHighReasoningRef(providerID, "auto"),
			High:        high,
			Medium:      codingAgentHighReasoningRef(providerID, "medium"),
			Low:         codingAgentHighReasoningRef(providerID, "low"),
			Phase:       high,
			AutoImprove: high,
		}, true
	case ProviderCursorCLI:
		return sameCodingAgentTierModels(providerID, DefaultCursorCLIModel), true
	case ProviderAgyCLI:
		return sameCodingAgentTierModels(providerID, DefaultAgyCLIModel), true
	case ProviderPiCLI:
		high := codingAgentHighReasoningRef(providerID, "google/gemini-3.5-flash")
		return &CodingAgentDefaultTierModels{
			Main:        codingAgentHighReasoningRef(providerID, DefaultPiCLIModel),
			High:        high,
			Medium:      codingAgentHighReasoningRef(providerID, "google/gemini-3.5-flash"),
			Low:         codingAgentHighReasoningRef(providerID, "google/gemini-2.5-flash"),
			Phase:       high,
			AutoImprove: high,
		}, true
	}

	return nil, false
}
