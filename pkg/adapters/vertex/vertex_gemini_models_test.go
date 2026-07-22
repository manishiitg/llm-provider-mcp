package vertex

import "testing"

func TestLatestGeminiModelMetadata(t *testing.T) {
	tests := []struct {
		modelID     string
		name        string
		inputCost   float64
		outputCost  float64
		cachedCost  float64
		thinkingLen int
	}{
		{ModelGemini36Flash, "Gemini 3.6 Flash", 1.50, 7.50, 0.15, 2},
		{ModelGemini35FlashLite, "Gemini 3.5 Flash-Lite", 0.30, 2.50, 0.03, 3},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			metadata, err := GetVertexGeminiModelMetadata(tt.modelID)
			if err != nil {
				t.Fatalf("GetVertexGeminiModelMetadata(%q): %v", tt.modelID, err)
			}
			if metadata.ModelName != tt.name || metadata.ContextWindow != 1048576 {
				t.Fatalf("unexpected metadata: %+v", metadata)
			}
			if metadata.InputCostPer1MTokens != tt.inputCost || metadata.OutputCostPer1MTokens != tt.outputCost || metadata.CachedInputCostPer1MTokens != tt.cachedCost {
				t.Fatalf("unexpected pricing metadata: %+v", metadata)
			}
			if !metadata.SupportsToolCalls || !metadata.SupportsJSONMode || !metadata.SupportsThinkingLevel || len(metadata.ThinkingLevels) != tt.thinkingLen {
				t.Fatalf("unexpected capability metadata: %+v", metadata)
			}
		})
	}
}

func TestLatestGeminiVersionSuffixNormalization(t *testing.T) {
	metadata, err := GetVertexGeminiModelMetadata(ModelGemini36Flash + "-001")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ModelID != ModelGemini36Flash+"-001" {
		t.Fatalf("ModelID = %q, want versioned ID preserved", metadata.ModelID)
	}
}
