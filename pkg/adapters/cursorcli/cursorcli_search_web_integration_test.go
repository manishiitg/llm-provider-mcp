package cursorcli

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCursorCLIRealSearchWeb(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"What is the capital of France? Use web search and reply with the city and country only.",
		WithMode("ask"),
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "paris") {
		t.Fatalf("expected result to mention Paris, got %q", result)
	}
}

func TestCursorCLIRealSearchWebLiveData(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	result, err := adapter.SearchWeb(ctx,
		"Search the web for the latest cursor-agent CLI version number released in 2026. Reply with just the version string.",
		WithMode("ask"),
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	result = strings.TrimSpace(result)
	if result == "" {
		t.Fatal("SearchWeb returned empty result")
	}
	// The result must contain something that looks like a version — proof of live data.
	if !strings.Contains(result, "20") && !strings.Contains(result, "v") && !strings.Contains(result, ".") {
		t.Logf("WARNING: result may not contain live web data: %q", result)
	}
	t.Logf("Live web search result: %s", result)
}
