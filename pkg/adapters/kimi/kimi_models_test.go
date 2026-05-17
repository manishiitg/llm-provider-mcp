package kimi

import "testing"

func TestGetDefaultVisibleKimiModelIDsIncludesVisionOnly(t *testing.T) {
	models := GetDefaultVisibleKimiModelIDs()
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0] != ModelKimiK26 {
		t.Fatalf("models[0] = %q, want %q", models[0], ModelKimiK26)
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

func TestGetKimiModelMetadataDefaultsToVision(t *testing.T) {
	meta, err := GetKimiModelMetadata("")
	if err != nil {
		t.Fatalf("GetKimiModelMetadata returned error: %v", err)
	}
	if meta.ModelID != ModelKimiK26 {
		t.Fatalf("ModelID = %q, want %q", meta.ModelID, ModelKimiK26)
	}
}

func TestGetKimiModelMetadataRejectsKimiCode(t *testing.T) {
	_, err := GetKimiModelMetadata(ModelKimiCode)
	if err == nil {
		t.Fatal("GetKimiModelMetadata returned nil error for removed kimi-code model")
	}
}
