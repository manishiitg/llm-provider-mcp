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
			name:          "codex uses gpt 5.6 terra medium",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.6-terra",
			wantReasoning: "medium",
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
	if defaults.Low.ModelID != "claude-haiku-4-5-20251001" || defaults.Low.Options["reasoning_effort"] != "medium" {
		t.Fatalf("low = %+v, want claude-haiku-4-5-20251001/medium", defaults.Low)
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
			name:          "claude code uses opus 5 high",
			provider:      ProviderClaudeCode,
			wantModelID:   "claude-opus-5",
			wantReasoning: "high",
		},
		{
			name:          "codex uses gpt 5.6 terra high",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-5.6-terra",
			wantReasoning: "high",
		},
		{
			name:          "cursor uses grok 4.6 high",
			provider:      ProviderCursorCLI,
			wantModelID:   "grok-4.6",
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

func TestCodingAgentDefaultTierModelsClaudeBuilderDefault(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderClaudeCode)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(claude-code) ok = false")
	}

	if defaults.Builder.Provider != string(ProviderClaudeCode) || defaults.Builder.ModelID != "claude-sonnet-5" {
		t.Fatalf("builder = %+v, want claude-code/claude-sonnet-5", defaults.Builder)
	}
	if defaults.Builder.Options["reasoning_effort"] != "high" {
		t.Fatalf("builder reasoning_effort = %#v, want high", defaults.Builder.Options["reasoning_effort"])
	}
	if defaults.Pulse.Provider != string(ProviderClaudeCode) {
		t.Fatalf("pulse provider = %q, want %q", defaults.Pulse.Provider, ProviderClaudeCode)
	}
	if defaults.Pulse.ModelID != "claude-opus-5" {
		t.Fatalf("pulse model_id = %q, want claude-opus-5", defaults.Pulse.ModelID)
	}
	if defaults.Pulse.Options["reasoning_effort"] != "high" {
		t.Fatalf("pulse reasoning_effort = %#v, want high", defaults.Pulse.Options["reasoning_effort"])
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
		"builder": {ref: defaults.Builder, model: "gpt-5.6-sol", effort: "high"},
		"high":    {ref: defaults.High, model: "gpt-5.6-terra", effort: "medium"},
		"medium":  {ref: defaults.Medium, model: "gpt-5.6-luna", effort: "high"},
		"low":     {ref: defaults.Low, model: "gpt-5.6-luna", effort: "medium"},
		"pulse":   {ref: defaults.Pulse, model: "gpt-5.6-terra", effort: "high"},
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
	check("high", defaults.High, "grok-4.6")
	check("medium", defaults.Medium, "composer-2.5")
	check("low", defaults.Low, "auto")
	check("builder", defaults.Builder, "grok-4.6")
	check("pulse", defaults.Pulse, "grok-4.6")
}

func TestCodingAgentDefaultTierModelsPiCLITierDefaults(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderPiCLI)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(pi-cli) ok = false")
	}
	check := func(name string, got CodingAgentTierModelRef, wantModel, wantEffort string) {
		t.Helper()
		if got.Provider != string(ProviderPiCLI) {
			t.Fatalf("%s provider = %q, want %q", name, got.Provider, ProviderPiCLI)
		}
		if got.ModelID != wantModel {
			t.Fatalf("%s model_id = %q, want %q", name, got.ModelID, wantModel)
		}
		if got.Options["reasoning_effort"] != wantEffort {
			t.Fatalf("%s reasoning_effort = %#v, want %q", name, got.Options["reasoning_effort"], wantEffort)
		}
	}
	check("builder", defaults.Builder, "google/gemini-3.8-flash", "high")
	check("high", defaults.High, "google/gemini-3.8-flash", "high")
	check("medium", defaults.Medium, "google/gemini-3.8-flash", "medium")
	check("low", defaults.Low, "google/gemini-3.5-flash-lite", "low")
	check("pulse", defaults.Pulse, "google/gemini-3.8-flash", "high")
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
		"builder": defaults.Builder,
		"high":    defaults.High,
		"medium":  defaults.Medium,
		"low":     defaults.Low,
		"pulse":   defaults.Pulse,
	}
}
