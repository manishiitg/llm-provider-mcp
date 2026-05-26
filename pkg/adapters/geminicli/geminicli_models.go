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
			meta.ModelName = "High (Gemini 3.1 Pro)"
		case "medium":
			meta.ModelName = "Medium (Gemini 3.5 Flash)"
		case "low":
			meta.ModelName = "Low (Gemini 3.1 Flash Lite)"
		}

		models = append(models, meta)
	}

	return models
}

// resolveGeminiCLIModelID maps the tier aliases the frontend exposes to the
// concrete model IDs that gemini-cli's --model flag accepts.
//
// Verified against gemini-cli 0.41.2's bundled model registry:
//
//	"gemini-3.1-pro"               — GA frontier model (the previous code shipped
//	                                  "gemini-3-pro-preview", which is the older
//	                                  preview snapshot)
//	"gemini-3.5-flash"             — GA mid-tier model; requires gemini-cli that
//	                                  includes 3.5 in its bundled registry (post-
//	                                  0.41.2). Older builds will fall through to
//	                                  the API and may 400; brew upgrade gemini-cli
//	                                  on dev boxes and deploy VMs.
//	"gemini-3.1-flash-lite-preview" — current shipped Flash-Lite. There is no
//	                                  GA "gemini-3.1-flash-lite" name yet (the
//	                                  previous code shipped that suffix and it
//	                                  silently fell through as an unknown ID).
func resolveGeminiCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "high":
		return "gemini-3.1-pro"
	case "medium":
		return "gemini-3.5-flash"
	case "low":
		return "gemini-3.1-flash-lite-preview"
	default:
		return strings.TrimSpace(modelID)
	}
}
