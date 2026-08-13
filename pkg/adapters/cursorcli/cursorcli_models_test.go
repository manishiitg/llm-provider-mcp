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
	if got := resolveCursorCLIModelID("auto"); got != "auto" {
		t.Fatalf("resolveCursorCLIModelID(auto) = %q, want explicit auto selector", got)
	}
}

func TestResolveCursorCLIModelIDMapsFriendlyGrok46(t *testing.T) {
	if got := resolveCursorCLIModelID("grok-4.6"); got != "cursor-grok-4.6-medium" {
		t.Fatalf("resolveCursorCLIModelID(grok-4.6) = %q, want cursor-grok-4.6-medium", got)
	}
}

func TestResolveCursorCLIModelIDKeepsExplicitModel(t *testing.T) {
	if got := resolveCursorCLIModelID("gpt-5"); got != "gpt-5" {
		t.Fatalf("resolveCursorCLIModelID(gpt-5) = %q, want gpt-5", got)
	}
	if got := resolveCursorCLIModelID("composer-2.5"); got != "composer-2.5" {
		t.Fatalf("resolveCursorCLIModelID(composer-2.5) = %q, want composer-2.5", got)
	}
	if got := resolveCursorCLIModelID("cursor-grok-4.6-medium"); got != "cursor-grok-4.6-medium" {
		t.Fatalf("resolveCursorCLIModelID(cursor-grok-4.6-medium) = %q, want cursor-grok-4.6-medium", got)
	}
}

func TestGetAllCursorCLIModelsShowsSimpleChoices(t *testing.T) {
	models := GetAllCursorCLIModels()
	if len(models) != 3 {
		t.Fatalf("GetAllCursorCLIModels returned %d models, want 3: %#v", len(models), models)
	}
	wantIDs := []string{"auto", "composer-2.5", "grok-4.6"}
	for i, want := range wantIDs {
		if models[i].ModelID != want {
			t.Fatalf("models[%d].ModelID = %q, want %q", i, models[i].ModelID, want)
		}
	}
}
