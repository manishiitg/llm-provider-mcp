package kimi

import "testing"

func TestGetDefaultVisibleKimiModelIDsIncludesCodeAndVision(t *testing.T) {
	models := GetDefaultVisibleKimiModelIDs()
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0] != ModelKimiCode {
		t.Fatalf("models[0] = %q, want %q", models[0], ModelKimiCode)
	}
	if models[1] != ModelKimiK26 {
		t.Fatalf("models[1] = %q, want %q", models[1], ModelKimiK26)
	}
}

func TestGetKimiModelMetadataVision(t *testing.T) {
	meta, err := GetKimiModelMetadata(ModelKimiK26)
	if err != nil {
		t.Fatalf("GetKimiModelMetadata returned error: %v", err)
	}
	if meta.Provider != "kimi" {
		t.Fatalf("Provider = %q, want kimi", meta.Provider)
	}
	if meta.ModelID != ModelKimiK26 {
		t.Fatalf("ModelID = %q, want %q", meta.ModelID, ModelKimiK26)
	}
	if !meta.SupportsJSONMode {
		t.Fatal("SupportsJSONMode = false, want true")
	}
}
