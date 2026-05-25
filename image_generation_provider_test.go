package llmproviders

import "testing"

func TestInitializeImageGenerationModelSupportsAgyCLI(t *testing.T) {
	model, err := InitializeImageGenerationModel(Config{Provider: ProviderAgyCLI, ModelID: "agy-cli"})
	if err != nil {
		t.Fatalf("InitializeImageGenerationModel(agy-cli) error = %v", err)
	}
	if model == nil {
		t.Fatal("InitializeImageGenerationModel(agy-cli) returned nil model")
	}
}
