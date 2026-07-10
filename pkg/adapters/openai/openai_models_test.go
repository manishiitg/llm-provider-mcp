package openai

import "testing"

func TestOpenAIGPT55MetadataIncludesPricing(t *testing.T) {
	meta, err := GetOpenAIModelMetadata("gpt-5.5-2026-04-23")
	if err != nil {
		t.Fatalf("GetOpenAIModelMetadata: %v", err)
	}
	if meta.ModelID != "gpt-5.5-2026-04-23" {
		t.Fatalf("ModelID = %q, want original snapshot id", meta.ModelID)
	}
	if meta.InputCostPer1MTokens != 5.00 || meta.OutputCostPer1MTokens != 30.00 || meta.CachedInputCostPer1MTokens != 0.50 {
		t.Fatalf("GPT-5.5 pricing = in %.2f cached %.2f out %.2f, want 5.00/0.50/30.00",
			meta.InputCostPer1MTokens, meta.CachedInputCostPer1MTokens, meta.OutputCostPer1MTokens)
	}
	if meta.ContextWindow != 1050000 {
		t.Fatalf("ContextWindow = %d, want 1050000", meta.ContextWindow)
	}
}

func TestOpenAIGPT56FamilyMetadata(t *testing.T) {
	tests := []struct {
		model      string
		input      float64
		cached     float64
		cacheWrite float64
		output     float64
	}{
		{model: ModelGPT56Sol, input: 5.00, cached: 0.50, cacheWrite: 6.25, output: 30.00},
		{model: ModelGPT56Terra, input: 2.50, cached: 0.25, cacheWrite: 3.125, output: 15.00},
		{model: ModelGPT56Luna, input: 1.00, cached: 0.10, cacheWrite: 1.25, output: 6.00},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			meta, err := GetOpenAIModelMetadata(tt.model)
			if err != nil {
				t.Fatalf("GetOpenAIModelMetadata: %v", err)
			}
			if meta.InputCostPer1MTokens != tt.input || meta.CachedInputCostPer1MTokens != tt.cached ||
				meta.CachedInputCostWritePer1MTokens != tt.cacheWrite || meta.OutputCostPer1MTokens != tt.output {
				t.Fatalf("pricing = in %.3f cached %.3f write %.3f out %.3f, want %.3f/%.3f/%.3f/%.3f",
					meta.InputCostPer1MTokens, meta.CachedInputCostPer1MTokens, meta.CachedInputCostWritePer1MTokens,
					meta.OutputCostPer1MTokens, tt.input, tt.cached, tt.cacheWrite, tt.output)
			}
			if !meta.SupportsReasoningEffort || meta.ContextWindow != 372000 {
				t.Fatalf("metadata = %+v, want reasoning and 372000 context", meta)
			}
		})
	}
}
