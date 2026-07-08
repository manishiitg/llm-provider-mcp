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
	if _, ok := wrapped.Model.(*claudecodeadapter.ClaudeCodeInteractiveAdapter); !ok {
		t.Fatalf("Claude Code default transport = %T, want *ClaudeCodeInteractiveAdapter", wrapped.Model)
	}
}

func TestInitializeClaudeCodeRejectsPrintTransport(t *testing.T) {
	t.Setenv(EnvClaudeCodeTransport, ClaudeCodeTransportPrint)
	t.Setenv(EnvClaudeCodeMode, "")

	_, err := InitializeLLM(Config{
		Provider: ProviderClaudeCode,
		ModelID:  "claude-haiku-4-5-20251001",
	})
	if err == nil {
		t.Fatal("InitializeLLM() error = nil, want print transport rejection")
	}
}

func TestInitializeClaudeCodeConfigTransportOverridesEnv(t *testing.T) {
	t.Setenv(EnvClaudeCodeTransport, ClaudeCodeTransportPrint)
	t.Setenv(EnvClaudeCodeMode, "")

	llm, err := InitializeLLM(Config{
		Provider:            ProviderClaudeCode,
		ModelID:             "claude-code",
		ClaudeCodeTransport: ClaudeCodeTransportTmux,
	})
	if err != nil {
		t.Fatalf("InitializeLLM() error = %v", err)
	}

	wrapped, ok := llm.(*ProviderAwareLLM)
	if !ok {
		t.Fatalf("InitializeLLM() returned %T, want *ProviderAwareLLM", llm)
	}
	if _, ok := wrapped.Model.(*claudecodeadapter.ClaudeCodeInteractiveAdapter); !ok {
		t.Fatalf("Claude Code config transport override = %T, want *ClaudeCodeInteractiveAdapter", wrapped.Model)
	}
}

func TestNormalizeClaudeCodeTransportAliases(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "default", raw: "", want: ClaudeCodeTransportTmux},
		{name: "legacy experimental alias", raw: "experimental", want: ClaudeCodeTransportTmux},
		{name: "tmux", raw: "tmux", want: ClaudeCodeTransportTmux},
		{name: "print", raw: "print", wantErr: true},
		{name: "p alias", raw: "-p", wantErr: true},
		{name: "agent sdk alias", raw: "agent-sdk", wantErr: true},
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
