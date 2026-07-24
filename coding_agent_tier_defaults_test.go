package llmproviders

import (
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
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
			name:          "codex uses gpt 5.6 terra xhigh",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.6-terra",
			wantReasoning: "xhigh",
		},
		{
			name:          "claude code uses sonnet 5 high",
			provider:      ProviderClaudeCode,
			wantModelID:   "claude-sonnet-5",
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
			name:            "claude code uses opus for maintenance",
			provider:        ProviderClaudeCode,
			wantModelID:     "claude-opus-4-8",
			wantReasoning:   "high",
			wantProviderSet: true,
		},
		{
			name:            "codex uses gpt 5.6 sol high",
			provider:        ProviderCodexCLI,
			wantModelID:     "gpt-5.6-sol",
			wantReasoning:   "high",
			wantProviderSet: true,
		},
		{
			name:            "cursor uses grok 4.5",
			provider:        ProviderCursorCLI,
			wantModelID:     "grok-4.5",
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
			if tt.wantProviderSet && defaults.Maintenance.Provider != string(tt.provider) {
				t.Fatalf("maintenance provider = %q, want %q", defaults.Maintenance.Provider, tt.provider)
			}
			if tt.wantSameAsHigh {
				if defaults.Maintenance.Provider != defaults.High.Provider ||
					defaults.Maintenance.ModelID != defaults.High.ModelID {
					t.Fatalf("maintenance = %+v, want same provider/model as high %+v", defaults.Maintenance, defaults.High)
				}
			} else if defaults.Maintenance.ModelID != tt.wantModelID {
				t.Fatalf("maintenance model_id = %q, want %q", defaults.Maintenance.ModelID, tt.wantModelID)
			}
			if got := defaults.Maintenance.Options["reasoning_effort"]; got != tt.wantReasoning {
				t.Fatalf("maintenance reasoning_effort = %#v, want %q", got, tt.wantReasoning)
			}
		})
	}
}

func TestCodingAgentDefaultTierModelsClaudeExecutionTiers(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderClaudeCode)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(claude-code) ok = false")
	}

	if defaults.High.ModelID != "claude-sonnet-5" || defaults.High.Options["reasoning_effort"] != "high" {
		t.Fatalf("high = %+v, want claude-sonnet-5/high", defaults.High)
	}
	if defaults.Medium.ModelID != "claude-sonnet-5" || defaults.Medium.Options["reasoning_effort"] != "medium" {
		t.Fatalf("medium = %+v, want claude-sonnet-5/medium", defaults.Medium)
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
			name:          "codex uses gpt 5.6 terra xhigh",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.6-terra",
			wantReasoning: "xhigh",
		},
		{
			name:          "cursor uses grok 4.5 high",
			provider:      ProviderCursorCLI,
			wantModelID:   "grok-4.5",
			wantReasoning: "high",
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

func TestCodingAgentDefaultTierModelsClaudeBuilderAndMaintenanceDefaults(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderClaudeCode)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(claude-code) ok = false")
	}

	for name, got := range map[string]CodingAgentTierModelRef{
		"builder":     defaults.Builder,
		"maintenance": defaults.Maintenance,
	} {
		if got.Provider != string(ProviderClaudeCode) {
			t.Fatalf("%s provider = %q, want %q", name, got.Provider, ProviderClaudeCode)
		}
		if got.ModelID != "claude-opus-4-8" {
			t.Fatalf("%s model_id = %q, want claude-opus-4-8", name, got.ModelID)
		}
		if got.Options["reasoning_effort"] != "high" {
			t.Fatalf("%s reasoning_effort = %#v, want high", name, got.Options["reasoning_effort"])
		}
	}

	if defaults.Pulse.Provider != string(ProviderClaudeCode) {
		t.Fatalf("pulse provider = %q, want %q", defaults.Pulse.Provider, ProviderClaudeCode)
	}
	if defaults.Pulse.ModelID != "claude-sonnet-5" {
		t.Fatalf("pulse model_id = %q, want claude-sonnet-5", defaults.Pulse.ModelID)
	}
	if defaults.Pulse.Options["reasoning_effort"] != "high" {
		t.Fatalf("pulse reasoning_effort = %#v, want high", defaults.Pulse.Options["reasoning_effort"])
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
			name:          "claude code uses opus for chief of staff",
			provider:      ProviderClaudeCode,
			wantModelID:   "claude-opus-4-8",
			wantReasoning: "high",
		},
		{
			name:          "codex uses chief sol high",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.6-sol",
			wantReasoning: "high",
		},
		{
			name:           "cursor follows grok 4.5 high",
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

func TestCodingAgentDefaultTierModelsCodexGPT56Family(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderCodexCLI)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(codex-cli) ok = false")
	}

	for name, check := range map[string]struct {
		ref    CodingAgentTierModelRef
		model  string
		effort string
	}{
		"builder":     {ref: defaults.Builder, model: "gpt-5.6-sol", effort: "high"},
		"high":        {ref: defaults.High, model: "gpt-5.6-terra", effort: "xhigh"},
		"medium":      {ref: defaults.Medium, model: "gpt-5.6-terra", effort: "medium"},
		"low":         {ref: defaults.Low, model: "gpt-5.6-luna", effort: "low"},
		"maintenance": {ref: defaults.Maintenance, model: "gpt-5.6-sol", effort: "high"},
		"pulse":       {ref: defaults.Pulse, model: "gpt-5.6-terra", effort: "xhigh"},
		"chief":       {ref: defaults.ChiefOfStaff, model: "gpt-5.6-sol", effort: "high"},
	} {
		if check.ref.ModelID != check.model || check.ref.Options["reasoning_effort"] != check.effort {
			t.Fatalf("%s = %+v, want model %s effort %s", name, check.ref, check.model, check.effort)
		}
	}
}

func TestCodingAgentDefaultTierModelsCursorTierDefaults(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderCursorCLI)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(cursor-cli) ok = false")
	}
	check := func(name string, got CodingAgentTierModelRef, want string) {
		t.Helper()
		if got.Provider != string(ProviderCursorCLI) {
			t.Fatalf("%s provider = %q, want %q", name, got.Provider, ProviderCursorCLI)
		}
		if got.ModelID != want {
			t.Fatalf("%s model_id = %q, want %q", name, got.ModelID, want)
		}
		if got.Options["reasoning_effort"] != "high" {
			t.Fatalf("%s reasoning_effort = %#v, want high", name, got.Options["reasoning_effort"])
		}
	}
	check("high", defaults.High, "grok-4.5")
	check("medium", defaults.Medium, "composer-2.5")
	check("low", defaults.Low, "auto")
	check("builder", defaults.Builder, "grok-4.5")
	check("pulse", defaults.Pulse, "grok-4.5")
	check("maintenance", defaults.Maintenance, "grok-4.5")
	check("chief_of_staff", defaults.ChiefOfStaff, "grok-4.5")
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
	out = append(out, picli.GetAllPiCLIModels()...)
	return out
}

func codingAgentDefaultTierModelRefs(defaults *CodingAgentDefaultTierModels) map[string]CodingAgentTierModelRef {
	return map[string]CodingAgentTierModelRef{
		"builder":        defaults.Builder,
		"high":           defaults.High,
		"medium":         defaults.Medium,
		"low":            defaults.Low,
		"maintenance":    defaults.Maintenance,
		"pulse":          defaults.Pulse,
		"chief_of_staff": defaults.ChiefOfStaff,
	}
}
