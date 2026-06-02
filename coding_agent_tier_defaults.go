package llmproviders

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/opencodecli"
)

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

func codingAgentTierModelRef(provider, modelID string) CodingAgentTierModelRef {
	return CodingAgentTierModelRef{Provider: provider, ModelID: modelID}
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
	ref := codingAgentTierModelRef(provider, modelID)
	return &CodingAgentDefaultTierModels{
		Main:   ref,
		High:   ref,
		Medium: ref,
		Low:    ref,
		Phase:  ref,
	}
}

func opencodeSubProviderTierModel(sp opencodecli.OpenCodeSubProvider, tier string) CodingAgentTierModelRef {
	modelID := sp.DefaultModelID
	if shortcut, ok := sp.TierShortcuts[tier]; ok && strings.TrimSpace(shortcut) != "" {
		modelID = strings.TrimSpace(shortcut)
	}
	return codingAgentTierModelRef(sp.ID, modelID)
}

func opencodeSubProviderDefaultTierModels(sp opencodecli.OpenCodeSubProvider) *CodingAgentDefaultTierModels {
	high := opencodeSubProviderTierModel(sp, "high")
	return &CodingAgentDefaultTierModels{
		Main:   codingAgentTierModelRef(sp.ID, sp.DefaultModelID),
		High:   high,
		Medium: opencodeSubProviderTierModel(sp, "medium"),
		Low:    opencodeSubProviderTierModel(sp, "low"),
		Phase:  high,
	}
}

// GetCodingAgentDefaultTierModels returns the provider-owned workflow tier
// defaults for coding-agent providers. Phase intentionally follows high.
func GetCodingAgentDefaultTierModels(provider Provider) (*CodingAgentDefaultTierModels, bool) {
	providerID := strings.TrimSpace(string(provider))

	switch Provider(providerID) {
	case ProviderCodexCLI:
		high := codingAgentHighReasoningRef(providerID, "high")
		return &CodingAgentDefaultTierModels{
			Main:   high,
			High:   high,
			Medium: codingAgentHighReasoningRef(providerID, "medium"),
			Low:    codingAgentHighReasoningRef(providerID, "low"),
			Phase:  high,
		}, true
	case ProviderClaudeCode:
		high := codingAgentHighReasoningRef(providerID, "claude-opus-4-6")
		return &CodingAgentDefaultTierModels{
			Main:   codingAgentHighReasoningRef(providerID, "claude-code"),
			High:   high,
			Medium: codingAgentHighReasoningRef(providerID, "claude-sonnet-4-6"),
			Low:    codingAgentHighReasoningRef(providerID, "claude-haiku-4-5-20251001"),
			Phase:  high,
		}, true
	case ProviderGeminiCLI:
		high := codingAgentTierModelRef(providerID, "high")
		return &CodingAgentDefaultTierModels{
			Main:   codingAgentTierModelRef(providerID, "auto"),
			High:   high,
			Medium: codingAgentTierModelRef(providerID, "medium"),
			Low:    codingAgentTierModelRef(providerID, "low"),
			Phase:  high,
		}, true
	case ProviderOpenCodeCLI:
		high := codingAgentTierModelRef(providerID, "high")
		return &CodingAgentDefaultTierModels{
			Main:   codingAgentTierModelRef(providerID, DefaultOpenCodeModel),
			High:   high,
			Medium: codingAgentTierModelRef(providerID, "medium"),
			Low:    codingAgentTierModelRef(providerID, "low"),
			Phase:  high,
		}, true
	case ProviderCursorCLI:
		return sameCodingAgentTierModels(providerID, DefaultCursorCLIModel), true
	case ProviderAgyCLI:
		return sameCodingAgentTierModels(providerID, DefaultAgyCLIModel), true
	}

	if sp, ok := opencodecli.FindOpenCodeSubProvider(providerID); ok {
		return opencodeSubProviderDefaultTierModels(sp), true
	}

	return nil, false
}
