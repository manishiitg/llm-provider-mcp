package claudecode

import (
	"math"
	"testing"
)

// claudeCodeCachePricedModels lists every model ID the two claude-code
// adapters price per token. Cache reads are the largest token bucket in a
// resumed coding session (tens of thousands of tokens per turn against a
// handful of fresh prompt tokens), so a model that prices input and output
// but leaves CachedInputCostPer1MTokens at zero reports a cost that is not
// merely imprecise -- it can understate the real spend by more than the
// entire reported amount, and it always errs low.
var claudeCodeCachePricedModels = []string{
	"claude-fable-5",
	"claude-opus-5",
	"claude-opus-4-8",
	"claude-opus-4-7",
	"claude-opus-4-6",
	"claude-sonnet-5",
	"claude-sonnet-4-6",
	"claude-haiku-4-5-20251001",
}

// Anthropic prices a cache read at 10% of the base input rate. The direct
// Anthropic adapter (pkg/adapters/anthropic/anthropic_models.go) already
// encodes that ratio for these same models; the claude-code adapters must not
// drift from it just because the tokens arrive via the CLI.
func TestClaudeCodeModelsPriceCacheReads(t *testing.T) {
	interactive := NewClaudeCodeInteractiveAdapter("claude-code", &MockLogger{})

	for _, modelID := range claudeCodeCachePricedModels {
		t.Run(modelID, func(t *testing.T) {
			interactiveMeta, err := interactive.GetModelMetadata(modelID)
			if err != nil {
				t.Fatalf("interactive metadata error: %v", err)
			}
			compat := NewClaudeCodeAdapter("", modelID, &MockLogger{})
			compatMeta, err := compat.GetModelMetadata(modelID)
			if err != nil {
				t.Fatalf("compat metadata error: %v", err)
			}

			for adapterName, meta := range map[string]struct {
				input, cached float64
			}{
				"interactive": {interactiveMeta.InputCostPer1MTokens, interactiveMeta.CachedInputCostPer1MTokens},
				"compat":      {compatMeta.InputCostPer1MTokens, compatMeta.CachedInputCostPer1MTokens},
			} {
				if meta.input <= 0 {
					t.Fatalf("%s: %s has no input pricing; update this test's model list", adapterName, modelID)
				}
				// mcpagent skips cache cost entirely on a zero rate
				// (agent.go: cacheTokens > 0 && CachedInputCostPer1MTokens > 0),
				// so zero here silently drops the charge rather than
				// approximating it.
				if meta.cached <= 0 {
					t.Errorf("%s: %s prices input at %v but cache reads at %v; cache tokens would be billed as free",
						adapterName, modelID, meta.input, meta.cached)
					continue
				}
				if want := meta.input / 10; math.Abs(meta.cached-want) > 1e-9 {
					t.Errorf("%s: %s cache read = %v, want %v (10%% of the %v input rate)",
						adapterName, modelID, meta.cached, want, meta.input)
				}
			}
		})
	}
}
