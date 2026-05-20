package geminicli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var knownGeminiCLIModels = []string{
	"auto",
	"high",
	"medium",
	"low",
}

// GetAllGeminiCLIModels returns the frontend-visible Gemini CLI model aliases.
func GetAllGeminiCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownGeminiCLIModels))
	adapter := &GeminiCLIAdapter{}

	for _, modelID := range knownGeminiCLIModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}

		switch modelID {
		case "auto":
			meta.ModelName = "Auto (recommended, pricing varies)"
		case "high":
			meta.ModelName = "High (Gemini 3.5 Flash)"
		case "medium":
			meta.ModelName = "Medium (Gemini 3.5 Flash)"
		case "low":
			meta.ModelName = "Low (Gemini 3.1 Flash Lite)"
		}

		models = append(models, meta)
	}

	return models
}

func resolveGeminiCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "high":
		return "gemini-3.5-flash"
	case "medium":
		return "gemini-3.5-flash"
	case "low":
		return "gemini-3.1-flash-lite"
	default:
		return strings.TrimSpace(modelID)
	}
}
