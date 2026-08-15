package codingagentmodels

import "testing"

func TestListPiIncludesCurrentCuratedModelsAndDynamicHint(t *testing.T) {
	catalog, err := List("pi-cli")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !catalog.AcceptsCustomID || catalog.LiveListCommand != "pi --list-models" {
		t.Fatalf("catalog = %#v", catalog)
	}
	want := map[string]bool{
		"google/gemini-3.7-flash":       false,
		"google/gemini-3.5-flash-lite":  false,
		"google/gemini-3.1-pro-preview": false,
		"minimax/MiniMax-M3":            false,
		"zai/glm-5.3":                   false,
		"moonshotai/kimi-k3":            false,
		"xai/grok-4.6":                  false,
	}
	for _, model := range catalog.Models {
		if _, ok := want[model.ID]; ok {
			want[model.ID] = true
		}
	}
	for model, found := range want {
		if !found {
			t.Errorf("Pi catalog is missing %q", model)
		}
	}
}

func TestListRejectsUnknownProvider(t *testing.T) {
	if _, err := List("missing"); err == nil {
		t.Fatal("List() error = nil")
	}
}
