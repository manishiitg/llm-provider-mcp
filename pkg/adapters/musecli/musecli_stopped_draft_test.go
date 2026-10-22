package musecli

import (
	"context"
	"os"
	"path/filepath"
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

// A timed-out submit left a draft in the TUI; the next user message was
// appended to it. Withdraw only that owned draft before typing a new turn.
func TestMuseUnconfirmedDraftIsClearedBeforeTheNextExplicitTurn(t *testing.T) {
	owner, session := "unconfirmed-draft-unit", "unit-pane"
	key, err := musePersistentKey(owner)
	if err != nil {
		t.Fatal(err)
	}
	musePersistentPool.Lock()
	entry := &musePersistentSession{tmuxName: session, lastSubmittedPrompt: "Review the supplied quotes\nthen compare totals"}
	musePersistentPool.m[key] = entry
	musePersistentPool.Unlock()
	t.Cleanup(func() { musePersistentPool.Lock(); delete(musePersistentPool.m, key); musePersistentPool.Unlock() })
	museRememberUnconfirmedDraft(owner, "another-pane")
	if entry.stoppedPrompt != "" {
		t.Fatal("another pane changed the retained draft")
	}
	museRememberUnconfirmedDraft(owner, session)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\ncase \"$1\" in\n capture-pane) printf '❯ \n' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := musePrepareStoppedPrompt(context.Background(), owner, session, "❯ Review the supplied quotes\nthen compare totals\n"); err != nil {
		t.Fatal(err)
	}
	if entry.stoppedPrompt != "" {
		t.Fatal("owned draft cleanup stayed pending")
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "C-u") != 1 || strings.Contains(string(raw), "Enter") || strings.Contains(string(raw), " -l ") {
		t.Fatalf("cleanup must clear once, without typing or submitting: %s", raw)
	}
}
