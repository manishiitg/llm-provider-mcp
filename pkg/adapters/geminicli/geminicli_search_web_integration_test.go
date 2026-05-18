package geminicli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGeminiCLIRealSearchWeb(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"What is the capital of France? Use web search and reply with the city and country only.",
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "paris") {
		t.Fatalf("expected result to mention Paris, got %q", result)
	}
}

func TestGeminiCLIRealSearchWebLiveData(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"Search the web for the latest Gemini CLI version number released in 2026. Reply with just the version string.",
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	result = strings.TrimSpace(result)
	if result == "" {
		t.Fatal("SearchWeb returned empty result")
	}
	t.Logf("Live web search result: %s", result)
}
