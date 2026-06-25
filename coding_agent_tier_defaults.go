package llmproviders

import "strings"

// CodingAgentTierModelRef is the provider-level default for a workflow tier.
type CodingAgentTierModelRef struct {
	Provider string                 `json:"provider"`
	ModelID  string                 `json:"model_id"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// CodingAgentDefaultTierModels describes the main/high/medium/low/phase
// defaults a coding-agent plan exposes to downstream workflow UIs.
type CodingAgentDefaultTierModels struct {
	Main   CodingAgentTierModelRef `json:"main"`
	High   CodingAgentTierModelRef `json:"high"`
	Medium CodingAgentTierModelRef `json:"medium"`
	Low    CodingAgentTierModelRef `json:"low"`
	Phase  CodingAgentTierModelRef `json:"phase"`
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
		Main:   ref,
		High:   ref,
		Medium: ref,
		Low:    ref,
		Phase:  ref,
	}
}

// GetCodingAgentDefaultTierModels returns the provider-owned workflow tier
// defaults for coding-agent providers. Phase intentionally follows high.
func GetCodingAgentDefaultTierModels(provider Provider) (*CodingAgentDefaultTierModels, bool) {
	providerID := strings.TrimSpace(string(provider))

	switch Provider(providerID) {
	case ProviderCodexCLI:
		high := codingAgentHighReasoningRef(providerID, "gpt-5.5")
		return &CodingAgentDefaultTierModels{
			Main:   high,
			High:   high,
			Medium: codingAgentHighReasoningRef(providerID, "gpt-5.4"),
			Low:    codingAgentHighReasoningRef(providerID, "gpt-5.3-codex-spark"),
			Phase:  high,
		}, true
	case ProviderClaudeCode:
		high := codingAgentHighReasoningRef(providerID, "claude-opus-4-8")
		return &CodingAgentDefaultTierModels{
			Main:   codingAgentHighReasoningRef(providerID, "claude-code"),
			High:   high,
			Medium: codingAgentHighReasoningRef(providerID, "claude-sonnet-4-6"),
			Low:    codingAgentHighReasoningRef(providerID, "claude-haiku-4-5-20251001"),
			Phase:  high,
		}, true
	case ProviderGeminiCLI:
		high := codingAgentHighReasoningRef(providerID, "high")
		return &CodingAgentDefaultTierModels{
			Main:   codingAgentHighReasoningRef(providerID, "auto"),
			High:   high,
			Medium: codingAgentHighReasoningRef(providerID, "medium"),
			Low:    codingAgentHighReasoningRef(providerID, "low"),
			Phase:  high,
		}, true
	case ProviderCursorCLI:
		return sameCodingAgentTierModels(providerID, DefaultCursorCLIModel), true
	case ProviderAgyCLI:
		return sameCodingAgentTierModels(providerID, DefaultAgyCLIModel), true
	case ProviderPiCLI:
		return &CodingAgentDefaultTierModels{
			Main:   codingAgentHighReasoningRef(providerID, DefaultPiCLIModel),
			High:   codingAgentHighReasoningRef(providerID, "google/gemini-3.5-flash"),
			Medium: codingAgentHighReasoningRef(providerID, "google/gemini-3.5-flash"),
			Low:    codingAgentHighReasoningRef(providerID, "google/gemini-2.5-flash"),
			Phase:  codingAgentHighReasoningRef(providerID, "google/gemini-3.5-flash"),
		}, true
	}

	return nil, false
}
