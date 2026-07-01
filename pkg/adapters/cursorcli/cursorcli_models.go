package cursorcli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var knownCursorCLIModels = []string{
	"cursor-cli",
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
		meta.ModelSelectionMode = "dynamic"
		models = append(models, meta)
	}

	return models
}

// resolveCursorCLIModelID maps the LLM-config-side model alias to the concrete
// --model arg passed to cursor-agent.
//
// Generic Runloop selectors intentionally omit --model and let Cursor choose
// the account-valid default. Explicit Cursor model ids such as composer-2.5,
// gpt-5, or sonnet-4-thinking still pass through unchanged.
func resolveCursorCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", "cursor-cli", "auto", "high", "medium", "low":
		return ""
	default:
		return strings.TrimSpace(modelID)
	}
}
