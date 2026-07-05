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
