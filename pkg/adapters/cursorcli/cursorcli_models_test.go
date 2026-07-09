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

func TestResolveCursorCLIModelIDMapsFriendlyGrok45(t *testing.T) {
	if got := resolveCursorCLIModelID("grok-4.5"); got != "grok-4.5-xhigh" {
		t.Fatalf("resolveCursorCLIModelID(grok-4.5) = %q, want grok-4.5-xhigh", got)
	}
}

func TestResolveCursorCLIModelIDKeepsExplicitModel(t *testing.T) {
	if got := resolveCursorCLIModelID("gpt-5"); got != "gpt-5" {
		t.Fatalf("resolveCursorCLIModelID(gpt-5) = %q, want gpt-5", got)
	}
	if got := resolveCursorCLIModelID("composer-2.5"); got != "composer-2.5" {
		t.Fatalf("resolveCursorCLIModelID(composer-2.5) = %q, want composer-2.5", got)
	}
	if got := resolveCursorCLIModelID("grok-4.5-xhigh"); got != "grok-4.5-xhigh" {
		t.Fatalf("resolveCursorCLIModelID(grok-4.5-xhigh) = %q, want grok-4.5-xhigh", got)
	}
}

func TestGetAllCursorCLIModelsShowsSimpleChoices(t *testing.T) {
	models := GetAllCursorCLIModels()
	if len(models) != 3 {
		t.Fatalf("GetAllCursorCLIModels returned %d models, want 3: %#v", len(models), models)
	}
	wantIDs := []string{"auto", "composer-2.5", "grok-4.5"}
	for i, want := range wantIDs {
		if models[i].ModelID != want {
			t.Fatalf("models[%d].ModelID = %q, want %q", i, models[i].ModelID, want)
		}
	}
}
