package llmproviders

import "testing"

func TestResolveGeminiCLIAPIKeyFallsBackToVertexKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	vertexKey := "vertex-ai-studio-key"

	got, source := resolveGeminiCLIAPIKey(Config{
		Provider: ProviderGeminiCLI,
		APIKeys:  &ProviderAPIKeys{Vertex: &vertexKey},
	})
	if got != vertexKey {
		t.Fatalf("resolveGeminiCLIAPIKey() key = %q, want vertex key", got)
	}
	if source != "vertex config" {
		t.Fatalf("resolveGeminiCLIAPIKey() source = %q, want vertex config", source)
	}
}

func TestResolveGeminiCLIAPIKeyPrefersDedicatedGeminiCLIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "env-key")
	t.Setenv("GOOGLE_API_KEY", "google-env-key")
	geminiKey := "gemini-cli-key"
	vertexKey := "vertex-key"

	got, source := resolveGeminiCLIAPIKey(Config{
		Provider: ProviderGeminiCLI,
		APIKeys: &ProviderAPIKeys{
			GeminiCLI: &geminiKey,
			Vertex:    &vertexKey,
		},
	})
	if got != geminiKey {
		t.Fatalf("resolveGeminiCLIAPIKey() key = %q, want gemini-cli key", got)
	}
	if source != "gemini-cli config" {
		t.Fatalf("resolveGeminiCLIAPIKey() source = %q, want gemini-cli config", source)
	}
}

func TestResolveGeminiCLIAPIKeyFallsBackToGoogleAPIKeyEnv(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "google-env-key")

	got, source := resolveGeminiCLIAPIKey(Config{Provider: ProviderGeminiCLI})
	if got != "google-env-key" {
		t.Fatalf("resolveGeminiCLIAPIKey() key = %q, want GOOGLE_API_KEY", got)
	}
	if source != "GOOGLE_API_KEY env var" {
		t.Fatalf("resolveGeminiCLIAPIKey() source = %q, want GOOGLE_API_KEY env var", source)
	}
}
