package llmproviders

import (
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/musecli"
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
			name:          "codex uses gpt 6 sol medium",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-6-sol",
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
	if defaults.Low.ModelID != "claude-sonnet-5" || defaults.Low.Options["reasoning_effort"] != "low" {
		t.Fatalf("low = %+v, want claude-sonnet-5/low", defaults.Low)
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
			name:           "claude code follows high",
			provider:       ProviderClaudeCode,
			wantSameAsHigh: true,
			wantReasoning:  "high",
		},
		{
			name:          "codex uses gpt 6 sol high",
			provider:      ProviderCodexCLI,
			wantModelID:   "gpt-6-sol",
			wantReasoning: "high",
		},
		{
			name:          "cursor uses auto high",
			provider:      ProviderCursorCLI,
			wantModelID:   "auto",
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

func TestCodingAgentDefaultTierModelsBuilder(t *testing.T) {
	for _, tt := range []struct {
		provider Provider
		model    string
		effort   string
	}{
		{provider: ProviderClaudeCode, model: "claude-opus-5-5", effort: "medium"},
		{provider: ProviderCodexCLI, model: "gpt-6-sol", effort: "high"},
	} {
		t.Run(string(tt.provider), func(t *testing.T) {
			defaults, ok := GetCodingAgentDefaultTierModels(tt.provider)
			if !ok {
				t.Fatalf("GetCodingAgentDefaultTierModels(%q) ok = false", tt.provider)
			}
			if defaults.Builder.Provider != string(tt.provider) || defaults.Builder.ModelID != tt.model {
				t.Fatalf("builder = %+v, want %s/%s", defaults.Builder, tt.provider, tt.model)
			}
			if defaults.Builder.Options["reasoning_effort"] != tt.effort {
				t.Fatalf("builder reasoning_effort = %#v, want %q", defaults.Builder.Options["reasoning_effort"], tt.effort)
			}
		})
	}
}

// TestCodingAgentDefaultTierModelsCodexGPT6Family covers the execution tiers.
// Builder and Pulse use GPT-6 Sol with high reasoning and are
// covered by TestCodingAgentDefaultTierModelsBuilder and the pulse test.
func TestCodingAgentDefaultTierModelsCodexGPT6Family(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderCodexCLI)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(codex-cli) ok = false")
	}

	for name, check := range map[string]struct {
		ref    CodingAgentTierModelRef
		model  string
		effort string
	}{
		"high":   {ref: defaults.High, model: "gpt-6-sol", effort: "medium"},
		"medium": {ref: defaults.Medium, model: "gpt-6-luna", effort: "high"},
		"low":    {ref: defaults.Low, model: "gpt-6-luna", effort: "high"},
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
	check("high", defaults.High, "auto")
	check("medium", defaults.Medium, "auto")
	check("low", defaults.Low, "auto")
	check("builder", defaults.Builder, "auto")
	check("pulse", defaults.Pulse, "auto")
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

func TestCodingAgentDefaultTierModelsMuseSingleModelEffortLadder(t *testing.T) {
	defaults, ok := GetCodingAgentDefaultTierModels(ProviderMuseCLI)
	if !ok {
		t.Fatal("GetCodingAgentDefaultTierModels(muse-cli) ok = false")
	}
	check := func(name string, got CodingAgentTierModelRef, wantEffort string) {
		t.Helper()
		if got.Provider != string(ProviderMuseCLI) {
			t.Fatalf("%s provider = %q, want %q", name, got.Provider, ProviderMuseCLI)
		}
		if got.ModelID != "muse-spark-1.3-contributor" {
			t.Fatalf("%s model_id = %q, want muse-spark-1.3-contributor (single model for now)", name, got.ModelID)
		}
		if got.Options["reasoning_effort"] != wantEffort {
			t.Fatalf("%s reasoning_effort = %#v, want %q", name, got.Options["reasoning_effort"], wantEffort)
		}
	}
	check("builder", defaults.Builder, "max")
	check("high", defaults.High, "xhigh")
	check("medium", defaults.Medium, "high")
	check("low", defaults.Low, "medium")
	check("pulse", defaults.Pulse, "max")
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
	out = append(out, musecli.GetAllMuseCLIModels()...)
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
