package llmproviders

import (
	"strings"
	"testing"

	kimiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/kimi"
)

func TestInitializeKimiCodeDefaultsToCLIAndSkipsAPIKeyRequirement(t *testing.T) {
	t.Setenv("KIMI_CODE_TRANSPORT", "")
	t.Setenv("KIMI_API_KEY", "")

	llm, err := initializeKimi(Config{
		Provider: ProviderKimi,
		ModelID:  kimiadapter.ModelKimiCode,
	})
	if err != nil {
		t.Fatalf("initializeKimi returned error for default CLI transport without API key: %v", err)
	}
	if _, ok := llm.(*kimiadapter.KimiCLIAdapter); !ok {
		t.Fatalf("initializeKimi returned %T, want *kimi.KimiCLIAdapter", llm)
	}
}

func TestInitializeKimiCLITransportSkipsAPIKeyRequirement(t *testing.T) {
	t.Setenv("KIMI_CODE_TRANSPORT", "cli")
	t.Setenv("KIMI_API_KEY", "")

	llm, err := initializeKimi(Config{
		Provider: ProviderKimi,
		ModelID:  kimiadapter.ModelKimiCode,
	})
	if err != nil {
		t.Fatalf("initializeKimi returned error for CLI transport without API key: %v", err)
	}
	if _, ok := llm.(*kimiadapter.KimiCLIAdapter); !ok {
		t.Fatalf("initializeKimi returned %T, want *kimi.KimiCLIAdapter", llm)
	}
}

func TestInitializeKimiHTTPTransportRequiresAPIKey(t *testing.T) {
	t.Setenv("KIMI_CODE_TRANSPORT", "http")
	t.Setenv("KIMI_API_KEY", "")

	_, err := initializeKimi(Config{
		Provider: ProviderKimi,
		ModelID:  kimiadapter.ModelKimiCode,
	})
	if err == nil {
		t.Fatal("initializeKimi returned nil error without KIMI_API_KEY on forced HTTP transport")
	}
	if !strings.Contains(err.Error(), "KIMI_API_KEY is required") {
		t.Fatalf("initializeKimi error = %q, want KIMI_API_KEY requirement", err.Error())
	}
}
