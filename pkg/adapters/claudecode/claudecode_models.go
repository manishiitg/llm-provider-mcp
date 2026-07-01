package claudecode

import "github.com/manishiitg/multi-llm-provider-go/llmtypes"

var knownClaudeCodeModels = []string{
	"claude-code",
	"claude-fable-5",
	"claude-opus-4-8",
	"claude-opus-4-7",
	"claude-opus-4-6",
	"claude-sonnet-5",
	"claude-sonnet-4-6",
	"claude-haiku-4-5-20251001",
}

// GetAllClaudeCodeModels returns the frontend-visible Claude Code CLI models.
func GetAllClaudeCodeModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownClaudeCodeModels))
	adapter := NewClaudeCodeInteractiveAdapter("claude-code", nil)

	for _, modelID := range knownClaudeCodeModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}

		switch modelID {
		case "claude-code":
			meta.ModelName = "Auto (default, pricing varies)"
		case "claude-fable-5":
			meta.ModelName = "Fable 5"
		case "claude-opus-4-8":
			meta.ModelName = "Opus 4.8"
		case "claude-opus-4-7":
			meta.ModelName = "Opus 4.7"
		case "claude-opus-4-6":
			meta.ModelName = "Opus 4.6"
		case "claude-sonnet-5":
			meta.ModelName = "Sonnet 5"
		case "claude-sonnet-4-6":
			meta.ModelName = "Sonnet 4.6"
		case "claude-haiku-4-5-20251001":
			meta.ModelName = "Haiku 4.5"
		}

		meta.SupportsReasoningEffort = true
		meta.ReasoningEffortLevels = []string{"low", "medium", "high", "max"}
		models = append(models, meta)
	}

	return models
}
