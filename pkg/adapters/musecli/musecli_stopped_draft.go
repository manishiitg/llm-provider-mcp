package musecli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// museActiveComposerDraft reads only the last prompt glyph's input line.
// Older prompts and replies in scrollback cannot authorize clearing a draft.
func museActiveComposerDraft(pane string) (string, bool) {
	idx := strings.LastIndex(pane, "❯")
	if idx < 0 {
		return "", false
	}
	line := pane[idx+len("❯"):]
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	return strings.TrimSpace(line), true
}

// musePrepareStoppedPrompt clears only our own visibly retained draft after
// an explicit Stop. If the user edited it, refuse to append a new message.
func musePrepareStoppedPrompt(ctx context.Context, owner, session, pane string) error {
	key, err := musePersistentKey(owner)
	if err != nil {
		return err
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	expected := ""
	if entry != nil && entry.tmuxName == session {
		expected = entry.stoppedPrompt
	}
	musePersistentPool.Unlock()
	if expected == "" {
		return nil
	}
	draft, ok := museActiveComposerDraft(pane)
	if !ok {
		return fmt.Errorf("Muse composer unavailable after Stop; new prompt was not typed")
	}
	if draft != "" {
		prefix := []rune(expected)
		if len(prefix) > 32 {
			prefix = prefix[:32]
		}
		if !strings.HasPrefix(draft, string(prefix)) {
			return fmt.Errorf("Muse composer changed after Stop; new prompt was not typed")
		}
		clear := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "C-u")
		if out, err := clear.CombinedOutput(); err != nil {
			return fmt.Errorf("clear stopped Muse draft: %w\n%s", err, out)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			pane, err = museTmuxCapturePane(ctx, session)
			if err != nil {
				return fmt.Errorf("verify stopped Muse draft cleared: %w", err)
			}
			if current, ok := museActiveComposerDraft(pane); ok && current == "" {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("stopped Muse draft remained in composer after clear; new prompt was not typed")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	musePersistentPool.Lock()
	if current := musePersistentPool.m[key]; current != nil && current.tmuxName == session && current.stoppedPrompt == expected {
		current.stoppedPrompt = ""
	}
	musePersistentPool.Unlock()
	return nil
}
