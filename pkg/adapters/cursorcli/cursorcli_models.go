package cursorcli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var knownCursorCLIModels = []string{
	"cursor-cli",
	"gpt-5",
	"sonnet-4",
	"sonnet-4-thinking",
}

// GetAllCursorCLIModels returns the frontend-visible Cursor Agent CLI models.
func GetAllCursorCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownCursorCLIModels))
	adapter := &CursorCLIAdapter{}

	for _, modelID := range knownCursorCLIModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}
		models = append(models, meta)
	}

	return models
}

func resolveCursorCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", "cursor-cli", "auto":
		return ""
	case "high":
		return "gpt-5"
	case "medium":
		return "sonnet-4-thinking"
	case "low":
		return "sonnet-4"
	default:
		return strings.TrimSpace(modelID)
	}
}
