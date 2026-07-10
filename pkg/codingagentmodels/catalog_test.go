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
		"google/gemini-3.5-flash":       false,
		"google/gemini-3.1-pro-preview": false,
		"minimax/MiniMax-M2.7":          false,
		"zai/glm-5.2":                   false,
		"moonshotai/kimi-k2.7-code":     false,
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
