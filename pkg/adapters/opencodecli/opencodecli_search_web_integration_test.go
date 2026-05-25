package opencodecli

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeCLIRealSearchWeb(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)
	t.Cleanup(func() { _ = CleanupOpenCodeCLIInteractiveSessions(context.Background()) })

	adapter := NewOpenCodeCLIAdapter("", freeTierTestModel(), &MockLogger{})
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

func TestOpenCodeCLIRealSearchWebLiveData(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)
	t.Cleanup(func() { _ = CleanupOpenCodeCLIInteractiveSessions(context.Background()) })

	adapter := NewOpenCodeCLIAdapter("", freeTierTestModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"Search the web for the latest OpenCode CLI version number released in 2026. Reply with just the version string.",
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
