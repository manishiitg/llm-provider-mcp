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

func TestResolveCursorCLIModelIDKeepsExplicitModel(t *testing.T) {
	if got := resolveCursorCLIModelID("gpt-5"); got != "gpt-5" {
		t.Fatalf("resolveCursorCLIModelID(gpt-5) = %q, want gpt-5", got)
	}
	if got := resolveCursorCLIModelID("composer-2.5"); got != "composer-2.5" {
		t.Fatalf("resolveCursorCLIModelID(composer-2.5) = %q, want composer-2.5", got)
	}
	if got := resolveCursorCLIModelID("grok-4.7"); got != "grok-4.7" {
		t.Fatalf("resolveCursorCLIModelID(grok-4.7) = %q, want grok-4.7", got)
	}
}

func TestGetAllCursorCLIModelsShowsSimpleChoices(t *testing.T) {
	models := GetAllCursorCLIModels()
	if len(models) != 3 {
		t.Fatalf("GetAllCursorCLIModels returned %d models, want 3: %#v", len(models), models)
	}
	wantIDs := []string{"auto", "composer-2.5", "grok-4.7"}
	for i, want := range wantIDs {
		if models[i].ModelID != want {
			t.Fatalf("models[%d].ModelID = %q, want %q", i, models[i].ModelID, want)
		}
	}
}

func TestGrok47PricingModes(t *testing.T) {
	adapter := &CursorCLIAdapter{}
	for _, tt := range []struct {
		model                       string
		input, cached, output, long float64
	}{
		{"grok-4.7", 2, 0.5, 6, 2},
		{"grok-4.7[context=500k,effort=high,fast=true]", 4, 1, 12, 1.5},
	} {
		meta, err := adapter.GetModelMetadata(tt.model)
		if err != nil {
			t.Fatalf("GetModelMetadata(%s): %v", tt.model, err)
		}
		if meta.InputCostPer1MTokens != tt.input || meta.CachedInputCostPer1MTokens != tt.cached ||
			meta.OutputCostPer1MTokens != tt.output || meta.LongContextThresholdTokens != 256000 ||
			meta.LongContextInputMultiplier != tt.long || meta.LongContextOutputMultiplier != tt.long {
			t.Errorf("%s metadata = %+v", tt.model, meta)
		}
	}
}
