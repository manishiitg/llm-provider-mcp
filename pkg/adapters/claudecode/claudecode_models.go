package claudecode

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// claudeCodeRetiredModels maps a retired model to the one that replaced it.
// Workflows and Crews keep the model they saved; the adapters run and price
// the replacement, so a retirement never leaves a saved config broken or
// unpriced. Claude Sonnet 5 was replaced by Claude Sonnet 5.5 (2026-09-29);
// Claude Haiku 4.5 by Claude Haiku 5.5 (2026-10-08, owner).
var claudeCodeRetiredModels = map[string]string{
	"claude-sonnet-5":           "claude-sonnet-5-5",
	"claude-haiku-4-5":          "claude-haiku-5-5",
	"claude-haiku-4-5-20251001": "claude-haiku-5-5",
}

// CurrentClaudeCodeModel returns modelID, or its replacement when retired.
func CurrentClaudeCodeModel(modelID string) string {
	if replacement, ok := claudeCodeRetiredModels[strings.ToLower(strings.TrimSpace(modelID))]; ok {
		return replacement
	}
	return modelID
}

// Context1MSuffix asks Claude Code for a model's 1M-token context window
// ("claude-opus-5-5[1m]"); without it the model runs with its normal window.
// AgentWorks adds it per chat when the person turns "1M context" on.
const Context1MSuffix = "[1m]"

// claudeCode1MContextModels offer a 1M context window on request.
var claudeCode1MContextModels = map[string]bool{
	"claude-opus-5-5":   true,
	"claude-sonnet-5-5": true,
	"claude-opus-5":     true,
}

// Strip1MContext returns modelID without the 1M suffix, and whether it had one.
func Strip1MContext(modelID string) (string, bool) {
	trimmed := strings.TrimSpace(modelID)
	if len(trimmed) > len(Context1MSuffix) && strings.EqualFold(trimmed[len(trimmed)-len(Context1MSuffix):], Context1MSuffix) {
		return trimmed[:len(trimmed)-len(Context1MSuffix)], true
	}
	return modelID, false
}

// Supports1MContext reports whether a Claude Code model offers a 1M window.
func Supports1MContext(modelID string) bool {
	base, _ := Strip1MContext(modelID)
	return claudeCode1MContextModels[strings.ToLower(CurrentClaudeCodeModel(base))]
}

// With1MContext returns the model ID that runs with the 1M window, or modelID
// unchanged when that model has no 1M window.
func With1MContext(modelID string) string {
	base, _ := Strip1MContext(modelID)
	if !Supports1MContext(base) {
		return modelID
	}
	return CurrentClaudeCodeModel(base) + Context1MSuffix
}

// with1MContextWindow reports a 1M-suffixed model's metadata: the base model's
// prices with its 1M window.
func with1MContextWindow(meta *llmtypes.ModelMetadata, modelID string, err error) (*llmtypes.ModelMetadata, error) {
	if meta != nil {
		copy := *meta
		copy.ModelID = modelID
		copy.ContextWindow = 1_000_000
		return &copy, err
	}
	return meta, err
}

var knownClaudeCodeModels = []string{
	"claude-code",
	"claude-fable-5-1",
	"claude-opus-5-5",
	"claude-sonnet-5-5",
	"claude-haiku-5-5",
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
		case "claude-fable-5-1":
			meta.ModelName = "Fable 5.1"
		case "claude-opus-5-5":
			meta.ModelName = "Opus 5.5"
		case "claude-sonnet-5-5":
			meta.ModelName = "Sonnet 5.5"
		case "claude-haiku-5-5":
			meta.ModelName = "Haiku 5.5"
		}

		meta.SupportsReasoningEffort = true
		meta.ReasoningEffortLevels = []string{"low", "medium", "high", "max"}
		models = append(models, meta)
	}

	return models
}
