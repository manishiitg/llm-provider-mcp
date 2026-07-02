package cursorcli

import "testing"

func TestResolveCursorCLIModelIDPinsDefaultToComposer25(t *testing.T) {
	for _, modelID := range []string{"", "cursor-cli", "high", "medium", "low"} {
		if got := resolveCursorCLIModelID(modelID); got != "composer-2.5" {
			t.Fatalf("resolveCursorCLIModelID(%q) = %q, want composer-2.5", modelID, got)
		}
	}
}

func TestResolveCursorCLIModelIDLeavesAutoUnpinned(t *testing.T) {
	if got := resolveCursorCLIModelID("auto"); got != "" {
		t.Fatalf("resolveCursorCLIModelID(auto) = %q, want empty selector", got)
	}
}

func TestResolveCursorCLIModelIDKeepsExplicitModel(t *testing.T) {
	if got := resolveCursorCLIModelID("gpt-5"); got != "gpt-5" {
		t.Fatalf("resolveCursorCLIModelID(gpt-5) = %q, want gpt-5", got)
	}
	if got := resolveCursorCLIModelID("composer-2.5"); got != "composer-2.5" {
		t.Fatalf("resolveCursorCLIModelID(composer-2.5) = %q, want composer-2.5", got)
	}
}
