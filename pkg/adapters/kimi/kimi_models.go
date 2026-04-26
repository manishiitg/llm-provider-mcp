package kimi

import (
	"fmt"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	ModelKimiCode = "kimi-code"
	ModelKimiK26  = "kimi-k2.6"
)

func GetDefaultVisibleKimiModelIDs() []string {
	return []string{ModelKimiCode, ModelKimiK26}
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
		{
			Provider:          "kimi",
			ModelID:           ModelKimiK26,
			ModelName:         "Kimi K2.6 Vision",
			ContextWindow:     262144,
			SupportsToolCalls: true,
			SupportsJSONMode:  true,
		},
	}
}

func GetKimiModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = ModelKimiCode
	}
	for _, metadata := range GetAllKimiModels() {
		if metadata.ModelID == modelID {
			result := *metadata
			return &result, nil
		}
	}
	return nil, fmt.Errorf("unknown Kimi model: %s", modelID)
}
