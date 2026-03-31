package geminicli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

var knownGeminiCLIModels = []string{
	"auto",
	"pro",
	"flash",
	"flash-lite",
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
		case "pro":
			meta.ModelName = "Gemini Pro"
		case "flash":
			meta.ModelName = "Gemini Flash"
		case "flash-lite":
			meta.ModelName = "Gemini Flash Lite"
		}

		models = append(models, meta)
	}

	return models
}
