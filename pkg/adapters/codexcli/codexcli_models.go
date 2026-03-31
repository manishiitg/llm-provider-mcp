package codexcli

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

var knownCodexCLIModels = []string{
	"codex-cli",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.3-codex",
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

		if modelID == "codex-cli" {
			meta.ModelName = "Auto (default, pricing varies)"
		}

		models = append(models, meta)
	}

	return models
}
