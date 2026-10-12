package musecli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	// museInitializingClearWait bounds the pre-typing wait for a boot in
	// progress. Resume shows "Initializing runtime" for ~1-2s (live probe,
	// 2026-10-12); the gate is soft — on timeout typing proceeds and the
	// existing took-effect retry still catches swallowed keystrokes.
	museInitializingClearWait = 30 * time.Second
	// museResubmitShortConfirm is the intake window before a refusal notice
	// is acted on. Intake normally lands in seconds; a slow native-log
	// write must not look like a dropped send.
	museResubmitShortConfirm = 20 * time.Second
	// museResubmitRunStartWait bounds the wait for a run to finish
	// starting after a refused submit before the turn fails clearly.
	museResubmitRunStartWait = 60 * time.Second
	// museResubmitMaxRetries bounds intake-gated resubmits after the
	// initial submit. Each retry re-checks intake first, so a slow
	// landing never turns into a duplicate.
	museResubmitMaxRetries = 2
)

// musePaneInitializing reports the TUI boot marker. It shows while the
// runtime is still coming up, before the composer accepts input.
func musePaneInitializing(pane string) bool {
	return strings.Contains(pane, "Initializing")
}

// museWaitInitializingClear polls until the boot marker clears or the bound
// passes. It returns false on timeout or capture failure; callers proceed
// anyway — this is a fast-path guard, not readiness proof.
func museWaitInitializingClear(ctx context.Context, session string) bool {
	deadline := time.Now().Add(museInitializingClearWait)
	for {
		pane, err := museTmuxCapturePane(ctx, session)
		if err == nil && !musePaneInitializing(pane) {
			return true
		}
		if err := ctx.Err(); err != nil {
			return false
		}
		if time.Now().After(deadline) {
			log.Printf("[MUSE_SUBMIT_NOTICE] session=%s still initializing after %s; typing anyway", session, museInitializingClearWait)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// museRunStartedSince reports whether any workdir-matching session log under
// dataHome records a run `started` event after `since`. It mirrors the
// discovery walk: only logs touched since the submit are read, and an empty
// workdir skips attribution.
func museRunStartedSince(dataHome string, since time.Time, workdir string) bool {
	root := filepath.Join(dataHome, "muse", "sessions")
	started := false
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if started || walkErr != nil || d.IsDir() || d.Name() != "session.jsonl" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || len(strings.Split(rel, string(filepath.Separator))) != 5 {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if workdir != "" && !museLogMatchesWorkspace(raw, workdir) {
			return nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, `"kind":"run"`) {
				continue
			}
			var row struct {
				RecordedAt  int64  `json:"recorded_at"`
				PayloadType string `json:"payload_type"`
				Payload     struct {
					Kind  string `json:"kind"`
					Event struct {
						Kind string `json:"kind"`
					} `json:"event"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			if row.PayloadType == "runtime.session" && row.Payload.Kind == "run" &&
				row.Payload.Event.Kind == "started" && row.RecordedAt >= since.UnixMicro() {
				started = true
				return nil
			}
		}
		return nil
	})
	return started
}

func museIntakeFound(dataHome string, since time.Time, snippet, workdir string) bool {
	if strings.TrimSpace(snippet) == "" {
		return false
	}
	_, _, err := museDiscoverSessionSince(dataHome, since, snippet, workdir)
	return err == nil
}

// museWaitIntakeShort polls for the intake record inside the short
// confirm window. A slow native-log write must look like a slow write,
// not a dropped send.
func museWaitIntakeShort(ctx context.Context, dataHome string, since time.Time, snippet, workdir string) bool {
	deadline := time.Now().Add(museResubmitShortConfirm)
	for {
		if museIntakeFound(dataHome, since, snippet, workdir) {
			return true
		}
		if err := ctx.Err(); err != nil {
			return false
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// museConfirmSubmitOrResubmit runs after museSendPrompt and before the long
// authoritative intake wait. A refusal notice with no intake inside a short
// window means the TUI dropped the send while a run was still starting
// (live 2026-10-12: resume +37s, intake never arrived): wait bounded for the
// run to start, re-check intake, and resubmit while it stays absent.
//
// The durable-ack invariant holds: a pane notice alone never triggers a
// resubmit — every retry requires a run-started signal plus a still-absent
// intake record, so a stale notice over a landed message (PLAT-422, one
// message ran 3+ times) resubmits nothing. Returns nil to proceed to
// museWaitIntake (confirmed or uncertain), or a clear error when Muse never
// becomes ready or resubmits keep landing nowhere.
func museConfirmSubmitOrResubmit(ctx context.Context, dataHome, session, prompt string, turnStart time.Time, workdir string) error {
	snippet := promptSnippet(prompt)
	for attempt := 0; ; attempt++ {
		if museWaitIntakeShort(ctx, dataHome, turnStart, snippet, workdir) {
			return nil
		}
		pane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return nil // pane unreadable; the long wait below still decides
		}
		if !museRejectedNotice(pane) {
			return nil // no refusal evidence; slow start stays on the long wait
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		startDeadline := time.Now().Add(museResubmitRunStartWait)
		for !museRunStartedSince(dataHome, turnStart, workdir) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if time.Now().After(startDeadline) {
				if museIntakeFound(dataHome, turnStart, snippet, workdir) {
					return nil
				}
				return fmt.Errorf("muse never became ready: refusal notice shown, no run started within %s and no intake record; not resubmitted blindly", museResubmitRunStartWait)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		if museIntakeFound(dataHome, turnStart, snippet, workdir) {
			return nil
		}
		if attempt >= museResubmitMaxRetries {
			return fmt.Errorf("muse dropped the submit %d times with no intake record; refusing to resubmit further", attempt+1)
		}
		log.Printf("[MUSE_RESUBMIT] session=%s attempt=%d run started without intake; clearing and resubmitting", session, attempt+1)
		if out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "C-u").CombinedOutput(); err != nil {
			log.Printf("[MUSE_RESUBMIT] session=%s clear before resubmit failed: %v\n%s", session, err, out)
			return nil // transport failed; the long wait still decides
		}
		if err := museSendPrompt(ctx, session, prompt); err != nil {
			log.Printf("[MUSE_RESUBMIT] session=%s resubmit failed: %v", session, err)
			return nil // the long wait below still decides from the native log
		}
	}
}
