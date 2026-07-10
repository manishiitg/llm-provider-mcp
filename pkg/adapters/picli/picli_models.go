package picli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

type visiblePiCLIModel struct {
	id   string
	name string
}

var knownPiCLIModels = []visiblePiCLIModel{
	{id: DefaultModelID, name: "Gemini 3.5 Flash"},
	{id: "google/gemini-2.5-flash", name: "Gemini 2.5 Flash"},
	{id: "zai/glm-5.2", name: "GLM-5.2"},
	{id: "kimi-coding/k2p7", name: "Kimi K2.7 Code"},
}

// GetAllPiCLIModels returns the frontend-visible Pi CLI routed model selectors.
func GetAllPiCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownPiCLIModels))
	adapter := &PiCLIAdapter{}

	for _, model := range knownPiCLIModels {
		meta, err := adapter.GetModelMetadata(model.id)
		if err != nil || meta == nil {
			continue
		}
		meta.ModelName = model.name

		models = append(models, meta)
	}

	return models
}
