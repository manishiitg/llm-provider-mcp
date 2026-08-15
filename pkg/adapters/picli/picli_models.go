package picli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

const (
	ModelGemini35FlashLite  = "google/gemini-3.5-flash-lite"
	ModelGemini31ProPreview = "google/gemini-3.1-pro-preview"
	ModelMiniMaxM3          = "minimax/MiniMax-M3"
	ModelGLM53              = "zai/glm-5.3"
	ModelKimiK3             = "moonshotai/kimi-k3"
	ModelGrok46             = "xai/grok-4.6"
)

var knownPiCLIModels = []string{
	DefaultModelID,
	ModelGemini35FlashLite,
	ModelGemini31ProPreview,
	ModelMiniMaxM3,
	ModelGLM53,
	ModelKimiK3,
	ModelGrok46,
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
			meta.ModelName = "Pi CLI (Gemini 3.7 Flash)"
		case ModelGemini35FlashLite:
			meta.ModelName = "Pi CLI (Gemini 3.5 Flash-Lite)"
		case ModelGemini31ProPreview:
			meta.ModelName = "Pi CLI (Gemini 3.1 Pro Preview)"
		case ModelMiniMaxM3:
			// 1M-token context, same as the adapter default -- no override needed.
			meta.ModelName = "Pi CLI (MiniMax M3)"
		case ModelGLM53:
			// 1M-token context, same as the adapter default -- no override needed.
			meta.ModelName = "Pi CLI (GLM 5.3)"
		case ModelKimiK3:
			// 1M-token context, same as the adapter default -- no override needed.
			meta.ModelName = "Pi CLI (Kimi K3)"
		case ModelGrok46:
			meta.ModelName = "Pi CLI (Grok 4.6)"
			meta.ContextWindow = 500000
		}
		meta.ModelSelectionMode = "dynamic"

		models = append(models, meta)
	}

	return models
}
