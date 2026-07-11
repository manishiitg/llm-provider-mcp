package utils

import "testing"

func TestGetAllModelMetadataIncludesAgyCLI(t *testing.T) {
	models := GetAllModelMetadata()
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.Provider == "agy-cli" && model.ModelID == "agy-cli" {
			return
		}
	}
	t.Fatal("GetAllModelMetadata() missing agy-cli/agy-cli metadata")
}

func TestGetAllModelMetadataExcludesRemovedGeminiCLI(t *testing.T) {
	for _, model := range GetAllModelMetadata() {
		if model != nil && model.Provider == "gemini-cli" {
			t.Fatalf("GetAllModelMetadata() includes removed Gemini CLI model %q", model.ModelID)
		}
	}
}
