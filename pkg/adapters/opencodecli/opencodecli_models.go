package opencodecli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var knownOpenCodeCLIModels = []string{
	"opencode-cli",
}

// GetAllOpenCodeCLIModels returns the frontend-visible OpenCode CLI models.
func GetAllOpenCodeCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownOpenCodeCLIModels))
	adapter := &OpenCodeCLIAdapter{}

	for _, modelID := range knownOpenCodeCLIModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}
		meta.ModelSelectionMode = "dynamic"
		models = append(models, meta)
	}

	return models
}

func resolveOpenCodeCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", "opencode-cli", "auto":
		return ""
	case "high":
		return "openai/gpt-5.1"
	case "medium":
		return "anthropic/claude-sonnet-4-5"
	case "low":
		return "opencode/deepseek-v4-flash"
	default:
		return strings.TrimSpace(modelID)
	}
}
