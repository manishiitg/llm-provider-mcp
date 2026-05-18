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

// resolveCursorCLIModelID maps the LLM-config-side model alias to the
// concrete --model arg passed to cursor-agent.
//
// cursor-agent's own implicit default (no --model) is "composer-2-fast",
// which downgrades quality silently. To keep our default predictable
// and on the latest non-fast Composer release, the empty/"cursor-cli"
// alias maps to "composer-2.5". Users who want the speed/cost tradeoff
// can pass "composer-2.5-fast" (or "composer-2-fast") explicitly.
func resolveCursorCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", "cursor-cli":
		return "composer-2.5"
	case "auto":
		return "auto"
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
