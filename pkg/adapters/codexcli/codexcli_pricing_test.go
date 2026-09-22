package codexcli

import "testing"

func TestCodexCurrentModelPricing(t *testing.T) {
	adapter := &CodexCLIAdapter{}
	for _, tt := range []struct {
		model                        string
		input, cached, write, output float64
	}{
		{"gpt-6-sol", 2, 0.2, 2.5, 10},
		{"gpt-6-luna", 0.1, 0.01, 0.125, 0.5},
	} {
		meta, err := adapter.GetModelMetadata(tt.model)
		if err != nil {
			t.Fatalf("GetModelMetadata(%s): %v", tt.model, err)
		}
		if meta.InputCostPer1MTokens != tt.input || meta.CachedInputCostPer1MTokens != tt.cached ||
			meta.CachedInputCostWritePer1MTokens != tt.write || meta.OutputCostPer1MTokens != tt.output ||
			meta.ContextWindow != 1050000 || meta.LongContextThresholdTokens != 272000 {
			t.Errorf("%s metadata = %+v", tt.model, meta)
		}
	}
}
