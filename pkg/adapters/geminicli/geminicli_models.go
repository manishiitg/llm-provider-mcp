package geminicli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// knownGeminiCLIModels is the ordered list of model IDs the frontend exposes
// for gemini-cli. Tier aliases (auto/high/medium/low) come first and resolve
// via resolveGeminiCLIModelID; bare GA model names follow so users with
// access to the newer frontier Pro variants can pick them explicitly.
//
// Why both: gemini-cli validates the model against the backend's
// fetchAvailableModels response. The "-preview" / safe tier aliases work for
// every account; the GA names (gemini-3.1-pro, gemini-3-pro) only resolve
// for accounts that have those models provisioned. Exposing both lets users
// pick whichever their account actually supports without us having to detect
// account tier server-side.
var knownGeminiCLIModels = []string{
	"auto",
	"high",
	"medium",
	"low",
	"gemini-3.1-pro",
	"gemini-3-pro",
}

// GetAllGeminiCLIModels returns the frontend-visible Gemini CLI model aliases.
func GetAllGeminiCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownGeminiCLIModels))
	adapter := &GeminiCLIAdapter{}

	for _, modelID := range knownGeminiCLIModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}

		switch modelID {
		case "auto":
			meta.ModelName = "Auto (recommended, pricing varies)"
		case "high":
			meta.ModelName = "High (Gemini 3.1 Pro Preview)"
		case "medium":
			meta.ModelName = "Medium (Gemini 3 Flash Preview)"
		case "low":
			meta.ModelName = "Low (Gemini 3.1 Flash Lite Preview)"
		case "gemini-3.1-pro":
			meta.ModelName = "Gemini 3.1 Pro (GA — requires account access)"
		case "gemini-3-pro":
			meta.ModelName = "Gemini 3 Pro (GA — requires account access)"
		}

		models = append(models, meta)
	}

	return models
}

// resolveGeminiCLIModelID maps the tier aliases the frontend exposes to the
// concrete model IDs that gemini-cli's --model flag accepts.
//
// IMPORTANT: gemini-cli validates the model name against the BACKEND's
// fetchAvailableModels response, not just the CLI's bundled registry. Bare
// GA names ("gemini-3.1-pro", "gemini-3-pro") are accepted by the CLI's
// flag parser but rejected at chat time by the backend on accounts that
// haven't been provisioned for those models — gemini-cli surfaces this as
// `Model "gemini-3.1-pro" was not found or is invalid` in its TUI picker
// and halts the chat. The "-preview" variants resolve for every
// authenticated account, so they're the tier defaults; users whose
// account has the GA model can pick "gemini-3.1-pro" / "gemini-3-pro"
// directly from the frontend (those entries live in knownGeminiCLIModels).
func resolveGeminiCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "high":
		return "gemini-3.1-pro-preview"
	case "medium":
		return "gemini-3-flash-preview"
	case "low":
		return "gemini-3.1-flash-lite-preview"
	default:
		return strings.TrimSpace(modelID)
	}
}
