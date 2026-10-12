package musecli

import "testing"

func TestMuseWonderModelsPublishedWithEfforts(t *testing.T) {
	models := GetAllMuseCLIModels()
	if len(models) < 2 {
		t.Fatalf("models = %d, want Wonder entries", len(models))
	}
	if models[0].ModelID != "muse-wonder-1.0" || models[0].ModelName != "Muse Wonder" {
		t.Fatalf("first model = %q/%q, want Muse Wonder", models[0].ModelID, models[0].ModelName)
	}
	if models[1].ModelID != "muse-wonder-full-1.0" || models[1].ModelName != "Muse Wonder Full (Muse Code subscribers)" {
		t.Fatalf("second model = %q/%q, want Muse Wonder Full label", models[1].ModelID, models[1].ModelName)
	}
	wantEfforts := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	for _, model := range models[:2] {
		if len(model.ReasoningEffortLevels) != len(wantEfforts) {
			t.Fatalf("%s effort levels = %v", model.ModelID, model.ReasoningEffortLevels)
		}
		for i, want := range wantEfforts {
			if model.ReasoningEffortLevels[i] != want {
				t.Fatalf("%s effort[%d] = %q, want %q", model.ModelID, i, model.ReasoningEffortLevels[i], want)
			}
		}
	}
}
