package cursorcli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var knownCursorCLIModels = []string{
	"auto",
	"composer-2.5",
	"grok-4.5",
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
// Generic Runloop selectors pin Cursor to the current high-quality Composer
// default. Use "auto" when the caller explicitly wants Cursor to choose the
// account default without passing --model. "grok-4.5" is Runloop's friendly
// selector for Cursor's canonical grok-4.5-xhigh id. Explicit Cursor model ids
// such as composer-2.5, gpt-5, or sonnet-4-thinking still pass through unchanged.
func resolveCursorCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", "cursor-cli", "high", "medium", "low":
		return "composer-2.5"
	case "auto":
		return ""
	case "grok-4.5":
		return "grok-4.5-xhigh"
	default:
		return strings.TrimSpace(modelID)
	}
}
