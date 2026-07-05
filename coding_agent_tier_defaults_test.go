package llmproviders

import (
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/agycli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/geminicli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/picli"
)

func TestCodingAgentDefaultTierModelsHighDefaults(t *testing.T) {
	tests := []struct {
		name          string
		provider      Provider
		wantModelID   string
		wantReasoning string
	}{
		{
			name:          "codex uses gpt 5.5 xhigh",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.5",
			wantReasoning: "xhigh",
		},
		{
			name:          "claude code uses opus high",
			provider:      ProviderClaudeCode,
			wantModelID:   "claude-opus-4-8",
			wantReasoning: "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaults, ok := GetCodingAgentDefaultTierModels(tt.provider)
			if !ok {
				t.Fatalf("GetCodingAgentDefaultTierModels(%q) ok = false", tt.provider)
			}
			if defaults.High.Provider != string(tt.provider) {
				t.Fatalf("high provider = %q, want %q", defaults.High.Provider, tt.provider)
			}
			if defaults.High.ModelID != tt.wantModelID {
				t.Fatalf("high model_id = %q, want %q", defaults.High.ModelID, tt.wantModelID)
			}
			if got := defaults.High.Options["reasoning_effort"]; got != tt.wantReasoning {
				t.Fatalf("high reasoning_effort = %#v, want %q", got, tt.wantReasoning)
			}
		})
	}
}

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
			name:            "claude code follows high",
			provider:        ProviderClaudeCode,
			wantSameAsHigh:  true,
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

func TestCodingAgentDefaultTierModelsPulseDefaults(t *testing.T) {
	tests := []struct {
		name           string
		provider       Provider
		wantModelID    string
		wantSameAsHigh bool
		wantReasoning  string
	}{
		{
			name:          "claude code uses sonnet 5 high",
			provider:      ProviderClaudeCode,
			wantModelID:   "claude-sonnet-5",
			wantReasoning: "high",
		},
		{
			name:          "codex uses gpt 5.5 high",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.5",
			wantReasoning: "high",
		},
		{
			name:           "cursor follows high",
			provider:       ProviderCursorCLI,
			wantSameAsHigh: true,
			wantReasoning:  "high",
		},
		{
			name:           "pi follows high",
			provider:       ProviderPiCLI,
			wantSameAsHigh: true,
			wantReasoning:  "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaults, ok := GetCodingAgentDefaultTierModels(tt.provider)
			if !ok {
				t.Fatalf("GetCodingAgentDefaultTierModels(%q) ok = false", tt.provider)
			}
			if defaults.Pulse.Provider != string(tt.provider) {
				t.Fatalf("pulse provider = %q, want %q", defaults.Pulse.Provider, tt.provider)
			}
			if tt.wantSameAsHigh {
				if defaults.Pulse.Provider != defaults.High.Provider ||
					defaults.Pulse.ModelID != defaults.High.ModelID {
					t.Fatalf("pulse = %+v, want same provider/model as high %+v", defaults.Pulse, defaults.High)
				}
			} else if defaults.Pulse.ModelID != tt.wantModelID {
				t.Fatalf("pulse model_id = %q, want %q", defaults.Pulse.ModelID, tt.wantModelID)
			}
			if got := defaults.Pulse.Options["reasoning_effort"]; got != tt.wantReasoning {
				t.Fatalf("pulse reasoning_effort = %#v, want %q", got, tt.wantReasoning)
			}
		})
	}
}

func TestCodingAgentDefaultTierModelsChiefOfStaffDefaults(t *testing.T) {
	tests := []struct {
		name           string
		provider       Provider
		wantModelID    string
		wantSameAsHigh bool
		wantReasoning  string
	}{
		{
			name:           "claude code uses opus high",
			provider:       ProviderClaudeCode,
			wantSameAsHigh: true,
			wantReasoning:  "high",
		},
		{
			name:          "codex uses auto improve xhigh",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.5",
			wantReasoning: "xhigh",
		},
		{
			name:           "cursor follows high",
			provider:       ProviderCursorCLI,
			wantSameAsHigh: true,
			wantReasoning:  "high",
		},
		{
			name:           "pi follows high",
			provider:       ProviderPiCLI,
			wantSameAsHigh: true,
			wantReasoning:  "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaults, ok := GetCodingAgentDefaultTierModels(tt.provider)
			if !ok {
				t.Fatalf("GetCodingAgentDefaultTierModels(%q) ok = false", tt.provider)
			}
			if defaults.ChiefOfStaff.Provider != string(tt.provider) {
				t.Fatalf("chief_of_staff provider = %q, want %q", defaults.ChiefOfStaff.Provider, tt.provider)
			}
			if tt.wantSameAsHigh {
				if defaults.ChiefOfStaff.Provider != defaults.High.Provider ||
					defaults.ChiefOfStaff.ModelID != defaults.High.ModelID {
					t.Fatalf("chief_of_staff = %+v, want same provider/model as high %+v", defaults.ChiefOfStaff, defaults.High)
				}
			} else if defaults.ChiefOfStaff.ModelID != tt.wantModelID {
				t.Fatalf("chief_of_staff model_id = %q, want %q", defaults.ChiefOfStaff.ModelID, tt.wantModelID)
			}
			if got := defaults.ChiefOfStaff.Options["reasoning_effort"]; got != tt.wantReasoning {
				t.Fatalf("chief_of_staff reasoning_effort = %#v, want %q", got, tt.wantReasoning)
			}
		})
	}
}

func TestCodingAgentDefaultTierModelsArePublished(t *testing.T) {
	published := map[string]map[string]bool{}
	for _, meta := range codingAgentPublishedModelMetadata() {
		if meta == nil {
			continue
		}
		provider := strings.TrimSpace(meta.Provider)
		modelID := strings.TrimSpace(meta.ModelID)
		if provider == "" || modelID == "" {
			continue
		}
		if published[provider] == nil {
			published[provider] = map[string]bool{}
		}
		published[provider][modelID] = true
	}

	for _, contract := range CodingAgentProviderContracts() {
		defaults, ok := GetCodingAgentDefaultTierModels(contract.Provider)
		if !ok {
			t.Fatalf("missing tier defaults for coding-agent provider %s", contract.Provider)
		}
		for name, ref := range codingAgentDefaultTierModelRefs(defaults) {
			provider := strings.TrimSpace(ref.Provider)
			modelID := strings.TrimSpace(ref.ModelID)
			if provider == "" || modelID == "" {
				t.Fatalf("%s.%s default is incomplete: %+v", contract.Provider, name, ref)
			}
			if !published[provider][modelID] {
				t.Fatalf("%s.%s default %s/%s is not published in model metadata registry", contract.Provider, name, provider, modelID)
			}
		}
	}
}

func codingAgentPublishedModelMetadata() []*llmtypes.ModelMetadata {
	var out []*llmtypes.ModelMetadata
	out = append(out, claudecode.GetAllClaudeCodeModels()...)
	out = append(out, codexcli.GetAllCodexCLIModels()...)
	out = append(out, cursorcli.GetAllCursorCLIModels()...)
	out = append(out, geminicli.GetAllGeminiCLIModels()...)
	out = append(out, agycli.GetAllAgyCLIModels()...)
	out = append(out, picli.GetAllPiCLIModels()...)
	return out
}

func codingAgentDefaultTierModelRefs(defaults *CodingAgentDefaultTierModels) map[string]CodingAgentTierModelRef {
	return map[string]CodingAgentTierModelRef{
		"main":           defaults.Main,
		"high":           defaults.High,
		"medium":         defaults.Medium,
		"low":            defaults.Low,
		"phase":          defaults.Phase,
		"auto_improve":   defaults.AutoImprove,
		"pulse":          defaults.Pulse,
		"chief_of_staff": defaults.ChiefOfStaff,
	}
}
