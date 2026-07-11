package llmproviders

import "testing"

func TestGeminiCLIProviderIsRemoved(t *testing.T) {
	if _, err := ValidateProvider("gemini-cli"); err == nil {
		t.Fatal("ValidateProvider(gemini-cli) succeeded, want unsupported provider error")
	}
	if _, ok := GetCodingAgentProviderContract(Provider("gemini-cli"), ""); ok {
		t.Fatal("GetCodingAgentProviderContract(gemini-cli) found a removed provider")
	}
}
