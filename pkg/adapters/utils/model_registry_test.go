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
