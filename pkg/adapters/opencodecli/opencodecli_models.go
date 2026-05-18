package opencodecli

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// knownOpenCodeCLIModels is the legacy single-selector entry retained so the
// existing "opencode-cli" provider tile keeps working alongside the new
// sub-provider tiles (Kimi, DeepSeek, Qwen, MiniMax, GLM, Free). The
// sub-provider tiles surface their own curated model catalogs and add the
// `<openCodeProviderID>/<modelID>` prefix themselves.
var knownOpenCodeCLIModels = []string{
	"opencode-cli",
}

// GetAllOpenCodeCLIModels returns the frontend-visible OpenCode CLI models
// for the generic `opencode-cli` provider tile. The sub-provider tiles
// (Kimi, DeepSeek, Qwen, MiniMax, GLM, Free) build their own model lists
// from OpenCodeSubProviders().
func GetAllOpenCodeCLIModels() []*llmtypes.ModelMetadata {
	models := make([]*llmtypes.ModelMetadata, 0, len(knownOpenCodeCLIModels))
	adapter := &OpenCodeCLIAdapter{}

	for _, modelID := range knownOpenCodeCLIModels {
		meta, err := adapter.GetModelMetadata(modelID)
		if err != nil || meta == nil {
			continue
		}
		meta.ModelSelectionMode = "dynamic"
		models = append(models, meta)
	}

	return models
}

// resolveOpenCodeCLIModelID turns a user-facing model id (or tier label)
// into the model string actually passed to `opencode run --model`.
//
// The order of precedence is:
//  1. Tier labels ("high", "medium", "low") resolve to a fixed paid model.
//     The fixed mapping is retained for callers that never touched a
//     sub-provider tile. When the caller has selected a sub-provider, they
//     should instead use resolveOpenCodeSubProviderModelID below.
//  2. The literal sentinels "" / "opencode-cli" / "auto" mean "do not pass
//     --model, let OpenCode use its configured default".
//  3. Anything else passes through verbatim (e.g. "openai/gpt-5.1").
func resolveOpenCodeCLIModelID(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", "opencode-cli", "auto":
		return ""
	case "high":
		return "openai/gpt-5.1"
	case "medium":
		return "anthropic/claude-sonnet-4-5"
	case "low":
		return "opencode/deepseek-v4-flash"
	default:
		return strings.TrimSpace(modelID)
	}
}

// resolveOpenCodeSubProviderModelID turns a caller-supplied model id into
// the full `<openCodeProviderID>/<modelID>` string for a specific
// sub-provider. The caller may pass:
//   - "" or the sub-provider's manifest id ("opencode-cli-kimi") to get the
//     sub-provider's DefaultModelID.
//   - A tier label ("high"/"medium"/"low") to get the configured shortcut,
//     falling back to DefaultModelID if the tier is empty.
//   - A bare model id ("kimi-k2-thinking") to use exactly that model.
//   - A full provider-prefixed id ("kimi-for-coding/kimi-k2-thinking") to
//     pass through unchanged.
func resolveOpenCodeSubProviderModelID(sp OpenCodeSubProvider, modelID string) string {
	trimmed := strings.TrimSpace(modelID)

	// Already provider-prefixed → pass through.
	if strings.Contains(trimmed, "/") {
		return trimmed
	}

	bare := trimmed
	switch trimmed {
	case "", sp.ID, "auto":
		bare = sp.DefaultModelID
	case "high", "medium", "low":
		if v, ok := sp.TierShortcuts[trimmed]; ok && strings.TrimSpace(v) != "" {
			bare = strings.TrimSpace(v)
		} else {
			bare = sp.DefaultModelID
		}
	}
	if bare == "" {
		return ""
	}
	return sp.OpenCodeProviderID + "/" + bare
}
