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
	"claude-fable-5-1",
	"claude-opus-5",
	"claude-sonnet-5",
	"claude-haiku-4-5-20251001",
}

// These older Anthropic models price a cache read at 10% of the base input
// rate. Opus 5.5 has a different rate and is checked separately below.
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

func TestClaudeCodeOpus55Pricing(t *testing.T) {
	for name, adapter := range map[string]func() (float64, float64, float64, float64, error){
		"interactive": func() (float64, float64, float64, float64, error) {
			meta, err := NewClaudeCodeInteractiveAdapter("claude-code", &MockLogger{}).GetModelMetadata("claude-opus-5-5")
			if err != nil {
				return 0, 0, 0, 0, err
			}
			return meta.InputCostPer1MTokens, meta.OutputCostPer1MTokens, meta.CachedInputCostPer1MTokens, meta.CachedInputCostWritePer1MTokens, nil
		},
		"compat": func() (float64, float64, float64, float64, error) {
			meta, err := NewClaudeCodeAdapter("", "claude-opus-5-5", &MockLogger{}).GetModelMetadata("claude-opus-5-5")
			if err != nil {
				return 0, 0, 0, 0, err
			}
			return meta.InputCostPer1MTokens, meta.OutputCostPer1MTokens, meta.CachedInputCostPer1MTokens, meta.CachedInputCostWritePer1MTokens, nil
		},
	} {
		input, output, cacheRead, cacheWrite, err := adapter()
		if err != nil {
			t.Fatalf("%s metadata: %v", name, err)
		}
		if input != 4 || output != 20 || cacheRead != 0.2 || cacheWrite != 5 {
			t.Errorf("%s Opus 5.5 pricing = (%v, %v, %v, %v), want (4, 20, 0.2, 5)", name, input, output, cacheRead, cacheWrite)
		}
	}
}
