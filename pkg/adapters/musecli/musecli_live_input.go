package musecli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Live-input transport for the muse tmux lane: steering follow-up text and
// control keys into the owner's pooled TUI. Muse was absent from both
// provider dispatches (structured-only era) — the layer-2 steer cert caught
// it: deliver failed with "provider has no live input transport".
//
// museSendPrompt is reused verbatim for follow-ups (chunked/paste routing +
// Enter) so steering delivery matches turn submission byte-for-byte, minus
// intake/quiescence: delivery is fire-and-report and the caller tracks
// completion, the same contract as cursor's sendCursorLiveInputToTmux.

// musePersistentTmuxForOwner resolves the pooled tmux session name for an
// owner session id. False when nothing is pooled (never started, already
// killed, or key derivation failed).
func musePersistentTmuxForOwner(owner string) (string, bool) {
	key, err := musePersistentKey(owner)
	if err != nil {
		return "", false
	}
	musePersistentPool.Lock()
	defer musePersistentPool.Unlock()
	entry := musePersistentPool.m[key]
	if entry == nil || strings.TrimSpace(entry.tmuxName) == "" {
		return "", false
	}
	return entry.tmuxName, true
}

// SendMuseInteractiveInput types a follow-up message into the owner's live
// pooled TUI and submits it.
func SendMuseInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	key, err := musePersistentKey(ownerSessionID)
	if err != nil {
		return err
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	if entry == nil {
		musePersistentPool.Unlock()
		return fmt.Errorf("no active Muse interactive session registered for owner session %s", ownerSessionID)
	}
	tmuxName := entry.tmuxName
	logPath := entry.logPath
	if logPath == "" && entry.nativeSessionID != "" {
		logPath = museSessionLogPath(entry.nativeSessionID)
	}
	baseline := museTranscriptMaxSequence(logPath)
	musePersistentPool.Unlock()
	if !museTmuxSessionAlive(ctx, tmuxName) {
		return fmt.Errorf("muse tmux session %q for owner %s is gone", tmuxName, ownerSessionID)
	}
	if err := museSendPrompt(ctx, tmuxName, message); err != nil {
		return err
	}
	musePersistentPool.Lock()
	if current := musePersistentPool.m[key]; current != nil && current.tmuxName == tmuxName {
		current.retainedBaselineSequence = baseline
		if current.logPath == "" {
			current.logPath = logPath
		}
	}
	musePersistentPool.Unlock()
	return nil
}

// SendMuseInteractiveControlKey injects a raw tmux key (Escape, C-c, Enter,
// Up, Down — see IsAllowedCodingAgentControlKey) into the owner's live
// pooled TUI.
func SendMuseInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	tmuxName, ok := musePersistentTmuxForOwner(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Muse interactive session registered for owner session %s", ownerSessionID)
	}
	send := exec.CommandContext(ctx, "tmux", "send-keys", "-t", tmuxName, key)
	if out, err := send.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys %s: %w\n%s", key, err, out)
	}
	return nil
}
