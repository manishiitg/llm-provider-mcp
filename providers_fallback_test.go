package llmproviders

import (
	"testing"

	claudecodeadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	zaiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/zai"
)

func TestInitializeClaudeCodeDefaultUsesExperimentalTransport(t *testing.T) {
	t.Setenv(EnvClaudeCodeTransport, "")
	t.Setenv(EnvClaudeCodeMode, "")

	llm, err := InitializeLLM(Config{
		Provider: ProviderClaudeCode,
		ModelID:  "claude-code",
	})
	if err != nil {
		t.Fatalf("InitializeLLM() error = %v", err)
	}

	wrapped, ok := llm.(*ProviderAwareLLM)
	if !ok {
		t.Fatalf("InitializeLLM() returned %T, want *ProviderAwareLLM", llm)
	}
	if _, ok := wrapped.Model.(*claudecodeadapter.ClaudeCodeExperimentalAdapter); !ok {
		t.Fatalf("Claude Code default transport = %T, want *ClaudeCodeExperimentalAdapter", wrapped.Model)
	}
}

func TestInitializeClaudeCodeCanUsePrintTransport(t *testing.T) {
	t.Setenv(EnvClaudeCodeTransport, ClaudeCodeTransportPrint)
	t.Setenv(EnvClaudeCodeMode, "")
	t.Setenv(EnvClaudeCodeAllowLegacyPrint, "1")

	llm, err := InitializeLLM(Config{
		Provider: ProviderClaudeCode,
		ModelID:  "claude-haiku-4-5-20251001",
	})
	if err != nil {
		t.Fatalf("InitializeLLM() error = %v", err)
	}

	wrapped, ok := llm.(*ProviderAwareLLM)
	if !ok {
		t.Fatalf("InitializeLLM() returned %T, want *ProviderAwareLLM", llm)
	}
	if _, ok := wrapped.Model.(*claudecodeadapter.ClaudeCodeAdapter); !ok {
		t.Fatalf("Claude Code print transport = %T, want *ClaudeCodeAdapter", wrapped.Model)
	}
}

func TestInitializeClaudeCodeConfigTransportOverridesEnv(t *testing.T) {
	t.Setenv(EnvClaudeCodeTransport, ClaudeCodeTransportPrint)
	t.Setenv(EnvClaudeCodeMode, "")
	t.Setenv(EnvClaudeCodeAllowLegacyPrint, "1")

	llm, err := InitializeLLM(Config{
		Provider:            ProviderClaudeCode,
		ModelID:             "claude-code",
		ClaudeCodeTransport: ClaudeCodeTransportExperimental,
	})
	if err != nil {
		t.Fatalf("InitializeLLM() error = %v", err)
	}

	wrapped, ok := llm.(*ProviderAwareLLM)
	if !ok {
		t.Fatalf("InitializeLLM() returned %T, want *ProviderAwareLLM", llm)
	}
	if _, ok := wrapped.Model.(*claudecodeadapter.ClaudeCodeExperimentalAdapter); !ok {
		t.Fatalf("Claude Code config transport override = %T, want *ClaudeCodeExperimentalAdapter", wrapped.Model)
	}
}

func TestNormalizeClaudeCodeTransportAliases(t *testing.T) {
	t.Setenv(EnvClaudeCodeAllowLegacyPrint, "1")

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "default", raw: "", want: ClaudeCodeTransportExperimental},
		{name: "experimental", raw: "experimental", want: ClaudeCodeTransportExperimental},
		{name: "tmux alias", raw: "tmux", want: ClaudeCodeTransportExperimental},
		{name: "print", raw: "print", want: ClaudeCodeTransportPrint},
		{name: "p alias", raw: "-p", want: ClaudeCodeTransportPrint},
		{name: "agent sdk alias", raw: "agent-sdk", want: ClaudeCodeTransportPrint},
		{name: "invalid", raw: "json", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeClaudeCodeTransport(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeClaudeCodeTransport(%q) error = nil, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeClaudeCodeTransport(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeClaudeCodeTransport(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestNormalizeClaudeCodeTransportRejectsLegacyPrintUnlessExplicitlyAllowed(t *testing.T) {
	t.Setenv(EnvClaudeCodeAllowLegacyPrint, "")

	if _, err := normalizeClaudeCodeTransport(ClaudeCodeTransportPrint); err == nil {
		t.Fatal("normalizeClaudeCodeTransport(print) error = nil, want disabled legacy print error")
	}
}

func TestGetDefaultModelCodexCLIUsesGPT55(t *testing.T) {
	t.Setenv("CODEX_CLI_PRIMARY_MODEL", "")

	if got := GetDefaultModel(ProviderCodexCLI); got != DefaultCodexCLIModel {
		t.Fatalf("GetDefaultModel(ProviderCodexCLI) = %q, want %q", got, DefaultCodexCLIModel)
	}
}

func TestGetDefaultModelCursorCLIUsesSentinel(t *testing.T) {
	t.Setenv("CURSOR_CLI_PRIMARY_MODEL", "")

	if got := GetDefaultModel(ProviderCursorCLI); got != DefaultCursorCLIModel {
		t.Fatalf("GetDefaultModel(ProviderCursorCLI) = %q, want %q", got, DefaultCursorCLIModel)
	}
}

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
