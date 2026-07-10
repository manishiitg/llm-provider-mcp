package picli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

const (
	ModelGemini31ProPreview = "google/gemini-3.1-pro-preview"
	ModelMiniMaxM27         = "minimax/MiniMax-M2.7"
	ModelGLM52              = "zai/glm-5.2"
	ModelKimiK27Code        = "moonshotai/kimi-k2.7-code"
)

var knownPiCLIModels = []string{
	DefaultModelID,
	ModelGemini31ProPreview,
	ModelMiniMaxM27,
	ModelGLM52,
	ModelKimiK27Code,
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
		case ModelGemini31ProPreview:
			meta.ModelName = "Pi CLI (Gemini 3.1 Pro Preview)"
		case ModelMiniMaxM27:
			meta.ModelName = "Pi CLI (MiniMax M2.7)"
			meta.ContextWindow = 204800
		case ModelGLM52:
			meta.ModelName = "Pi CLI (GLM 5.2)"
		case ModelKimiK27Code:
			meta.ModelName = "Pi CLI (Kimi K2.7 Code)"
			meta.ContextWindow = 262144
		}
		meta.ModelSelectionMode = "dynamic"

		models = append(models, meta)
	}

	return models
}
