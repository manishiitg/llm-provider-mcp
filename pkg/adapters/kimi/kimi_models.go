package kimi

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

const (
	ModelKimiCode = "kimi-code"
)

func GetDefaultVisibleKimiModelIDs() []string {
	return []string{ModelKimiCode}
}

func GetAllKimiModels() []*llmtypes.ModelMetadata {
	return []*llmtypes.ModelMetadata{
		{
			Provider:                "kimi",
			ModelID:                 ModelKimiCode,
			ModelName:               "Kimi Code (via Claude Code)",
			ContextWindow:           262144,
			SupportsToolCalls:       true,
			SupportsReasoningEffort: true,
			ReasoningEffortLevels:   []string{"low", "medium", "high", "max"},
		},
	}
}
