package llmproviders

import (
	"testing"

	zaiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/zai"
)

func TestGetDefaultFallbackModelsForModel_ZAITextModels(t *testing.T) {
	tests := []struct {
		name       string
		primary    string
		wantModels []string
	}{
		{
			name:       "glm-5.1 falls back to glm-4.7",
			primary:    zaiadapter.ModelGLM51,
			wantModels: []string{zaiadapter.ModelGLM47},
		},
		{
			name:       "glm-4.7 falls back to glm-5.1",
			primary:    zaiadapter.ModelGLM47,
			wantModels: []string{zaiadapter.ModelGLM51},
		},
		{
			name:       "vision model gets no default text fallback",
			primary:    zaiadapter.ModelGLM5VTurbo,
			wantModels: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetDefaultFallbackModelsForModel(ProviderZAI, tt.primary)
			if len(got) != len(tt.wantModels) {
				t.Fatalf("expected %d models, got %d: %v", len(tt.wantModels), len(got), got)
			}
			for i := range got {
				if got[i] != tt.wantModels[i] {
					t.Fatalf("expected fallback %q at index %d, got %q", tt.wantModels[i], i, got[i])
				}
			}
		})
	}
}
