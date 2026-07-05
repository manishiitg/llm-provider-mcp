package picli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

var knownPiCLIModels = []string{
	DefaultModelID,
	"google/gemini-2.5-flash",
}

// GetAllPiCLIModels returns the frontend-visible Pi CLI routed model selectors.
func GetAllPiCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownPiCLIModels))
	adapter := &PiCLIAdapter{}

	for _, modelID := range knownPiCLIModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}

		switch modelID {
		case DefaultModelID:
			meta.ModelName = "Pi CLI (Gemini 3.5 Flash)"
		case "google/gemini-2.5-flash":
			meta.ModelName = "Pi CLI (Gemini 2.5 Flash)"
		}

		models = append(models, meta)
	}

	return models
}
