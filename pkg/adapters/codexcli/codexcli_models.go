package codexcli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var knownCodexCLIModels = []string{
	"codex-cli",
	"high",
	"medium",
	"low",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.3-codex-spark",
}

// GetAllCodexCLIModels returns the frontend-visible Codex CLI models.
func GetAllCodexCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownCodexCLIModels))
	adapter := &CodexCLIAdapter{}

	for _, modelID := range knownCodexCLIModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}

		switch modelID {
		case "codex-cli":
			meta.ModelName = "Auto (default, pricing varies)"
		case "high":
			meta.ModelName = "High (GPT-5.6 Sol)"
		case "medium":
			meta.ModelName = "Medium (GPT-5.6 Terra)"
		case "low":
			meta.ModelName = "Low (GPT-5.6 Luna)"
		case "gpt-5.6-sol":
			meta.ModelName = "GPT-5.6 Sol"
		case "gpt-5.6-terra":
			meta.ModelName = "GPT-5.6 Terra"
		case "gpt-5.6-luna":
			meta.ModelName = "GPT-5.6 Luna"
		case "gpt-5.5":
			meta.ModelName = "GPT-5.5"
		case "gpt-5.4":
			meta.ModelName = "GPT-5.4"
		case "gpt-5.3-codex-spark":
			meta.ModelName = "GPT-5.3 Codex Spark"
		}

		models = append(models, meta)
	}

	return models
}

func resolveCodexCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "high":
		return "gpt-5.6-sol"
	case "medium":
		return "gpt-5.6-terra"
	case "low":
		return "gpt-5.6-luna"
	default:
		return strings.TrimSpace(modelID)
	}
}
