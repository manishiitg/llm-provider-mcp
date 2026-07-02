package llmproviders

import "testing"

func TestCodingAgentDefaultTierModelsAutoImproveDefaults(t *testing.T) {
	tests := []struct {
		name            string
		provider        Provider
		wantModelID     string
		wantSameAsHigh  bool
		wantReasoning   string
		wantProviderSet bool
	}{
		{
			name:            "claude code uses fable",
			provider:        ProviderClaudeCode,
			wantModelID:     "claude-fable-5",
			wantReasoning:   "high",
			wantProviderSet: true,
		},
		{
			name:            "codex uses gpt 5.5 xhigh",
			provider:        ProviderCodexCLI,
			wantModelID:     "gpt-5.5",
			wantReasoning:   "xhigh",
			wantProviderSet: true,
		},
		{
			name:            "cursor follows high",
			provider:        ProviderCursorCLI,
			wantSameAsHigh:  true,
			wantReasoning:   "high",
			wantProviderSet: true,
		},
		{
			name:            "pi follows high",
			provider:        ProviderPiCLI,
			wantSameAsHigh:  true,
			wantReasoning:   "high",
			wantProviderSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaults, ok := GetCodingAgentDefaultTierModels(tt.provider)
			if !ok {
				t.Fatalf("GetCodingAgentDefaultTierModels(%q) ok = false", tt.provider)
			}
			if tt.wantProviderSet && defaults.AutoImprove.Provider != string(tt.provider) {
				t.Fatalf("auto_improve provider = %q, want %q", defaults.AutoImprove.Provider, tt.provider)
			}
			if tt.wantSameAsHigh {
				if defaults.AutoImprove.Provider != defaults.High.Provider ||
					defaults.AutoImprove.ModelID != defaults.High.ModelID {
					t.Fatalf("auto_improve = %+v, want same provider/model as high %+v", defaults.AutoImprove, defaults.High)
				}
			} else if defaults.AutoImprove.ModelID != tt.wantModelID {
				t.Fatalf("auto_improve model_id = %q, want %q", defaults.AutoImprove.ModelID, tt.wantModelID)
			}
			if got := defaults.AutoImprove.Options["reasoning_effort"]; got != tt.wantReasoning {
				t.Fatalf("auto_improve reasoning_effort = %#v, want %q", got, tt.wantReasoning)
			}
		})
	}
}
