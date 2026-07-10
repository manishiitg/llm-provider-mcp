package picli

import "testing"

func TestGetAllPiCLIModelsIncludesCuratedChineseModels(t *testing.T) {
	models := GetAllPiCLIModels()
	byID := make(map[string]string, len(models))
	for _, model := range models {
		byID[model.ModelID] = model.ModelName
		if !model.SupportsReasoningEffort {
			t.Fatalf("model %q must expose Pi thinking levels", model.ModelID)
		}
	}

	for id, wantName := range map[string]string{
		"zai/glm-5.2":      "GLM-5.2",
		"kimi-coding/k2p7": "Kimi K2.7 Code",
	} {
		if got := byID[id]; got != wantName {
			t.Fatalf("model %q name = %q, want %q; models = %#v", id, got, wantName, byID)
		}
	}
}
