package cursorcli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var knownCursorCLIModels = []string{
	"auto",
	"composer-2.5",
	"grok-4.6",
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
// default. Use "auto" when the caller explicitly wants Cursor's Auto router;
// it must be passed as `--model auto` because omitting the flag can retain the
// account's current pinned model. "grok-4.6" is Runloop's friendly selector
// for Cursor's canonical grok id. Explicit Cursor model ids such as
// composer-2.5, gpt-5, or sonnet-4-thinking still pass through unchanged.
//
// Verified against `cursor-agent --list-models` on 2026-08-13: Cursor moved
// its grok lineup under a "cursor-" prefix (cursor-grok-4.6-*), superseding
// the prior grok-4.5 generation this used to resolve to.
func resolveCursorCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", "cursor-cli", "high", "medium", "low":
		return "composer-2.5"
	case "auto":
		return "auto"
	case "grok-4.6":
		return "cursor-grok-4.6-medium"
	default:
		return strings.TrimSpace(modelID)
	}
}
