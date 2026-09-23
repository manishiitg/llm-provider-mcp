package musecli

import (
	"context"
	"strings"
	"testing"
)

func TestMuseActiveComposerDraftUsesLatestPrompt(t *testing.T) {
	pane := "❯ older prompt\nassistant reply\n❯ Write a detailed 1200-word essay\n  muse · workspace\n"
	draft, ok := museActiveComposerDraft(pane)
	if !ok || draft != "Write a detailed 1200-word essay" {
		t.Fatalf("composer = (%q, %v)", draft, ok)
	}
}

func TestMuseStoppedDraftRefusesChangedComposer(t *testing.T) {
	owner := "stopped-draft-unit"
	key, err := musePersistentKey(owner)
	if err != nil {
		t.Fatal(err)
	}
	musePersistentPool.Lock()
	musePersistentPool.m[key] = &musePersistentSession{tmuxName: "unit-pane", stoppedPrompt: "Write a detailed 1200-word essay"}
	musePersistentPool.Unlock()
	t.Cleanup(func() {
		musePersistentPool.Lock()
		delete(musePersistentPool.m, key)
		musePersistentPool.Unlock()
	})
	err = musePrepareStoppedPrompt(context.Background(), owner, "unit-pane", "❯ user changed the draft\n  muse · workspace\n")
	if err == nil || !strings.Contains(err.Error(), "composer changed") {
		t.Fatalf("changed composer should block submission, got %v", err)
	}
}
