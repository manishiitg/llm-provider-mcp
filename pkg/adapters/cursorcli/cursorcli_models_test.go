package cursorcli

import "testing"

func TestResolveCursorCLIModelIDLeavesDefaultUnpinned(t *testing.T) {
	for _, modelID := range []string{"", "cursor-cli", "auto", "high", "medium", "low"} {
		if got := resolveCursorCLIModelID(modelID); got != "" {
			t.Fatalf("resolveCursorCLIModelID(%q) = %q, want empty default selector", modelID, got)
		}
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
