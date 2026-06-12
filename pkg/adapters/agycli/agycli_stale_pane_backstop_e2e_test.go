package agycli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestAgyInteractiveResponseStalePaneBackstopE2E is the regression guard for the
// production hang where a COMPLETED agy turn was never recognized as done.
//
// The real incident: a message_sequence turn finished its work (files written,
// pane idle with a "> " prompt), but a stale "○ " tool card left in the tmux
// scrollback held hasAgyReadyPrompt false forever. Because the call context has
// no deadline and every completion branch is gated behind hasAgyReadyPrompt, the
// loop spun for 20+ minutes — executeMessageSequenceItem never returned, so the
// step's completion notification never fired and the workflow could not advance.
//
// The stale-pane backstop is the detection-independent safety net: once the pane
// has produced activity and then frozen (byte-identical) past the backstop
// window, the loop extracts whatever response text is on the pane and returns it,
// regardless of what hasAgyReadyPrompt reports.
//
// Hermetic: drives a real detached tmux session whose pane shows assistant-like
// response text WITHOUT any ready-prompt marker (no "> ", no "type your message"),
// then freezes. hasAgyReadyPrompt therefore stays false the entire time — exactly
// the stuck-detection condition — so only the backstop can end the wait.
func TestAgyInteractiveResponseStalePaneBackstopE2E(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not in PATH; agy tmux e2e requires tmux")
	}
	// Short, deterministic backstop window so the test is fast. Keep the
	// first-activity window large so it can never be the thing that returns —
	// this test must prove the STALE-PANE path fires.
	t.Setenv(EnvAgyInteractiveStalePaneBackstopSeconds, "3")
	t.Setenv(EnvAgyInteractiveFirstActivityTimeoutSeconds, "120")

	const sentinel = "AGY_BACKSTOP_RESPONSE_SENTINEL"
	sessionName := newAgyTmuxSessionName()
	// A pane that renders assistant-like response prose and then freezes. There is
	// deliberately NO "> " input prompt and no ready marker, so hasAgyReadyPrompt
	// stays false — mirroring the stuck pane where a stale scrollback artifact
	// suppressed ready detection. The text is plain prose so it survives
	// extraction (parseAgyInteractiveResponse), exercising the "return what we
	// have" success path of the backstop.
	cmd := []string{"sh", "-c", "printf '" + sentinel + " is the final answer.\\n'; sleep 600"}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := startAgyTmuxSession(ctx, sessionName, cmd, nil, t.TempDir()); err != nil {
		t.Fatalf("start static tmux session: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = killAgyTmuxSession(closeCtx, sessionName)
	})

	waitForAgyRealPaneContains(t, sessionName, sentinel, 10*time.Second, make(chan error))

	// Empty baseline => the whole pane is the delta => sawActivity becomes true
	// on the first tick, satisfying the backstop's "produced activity" guard.
	started := time.Now()
	captured, err := waitForAgyInteractiveResponse(ctx, sessionName, "", "what is 2+2?", nil, nil, false)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("stale-pane backstop should return the extracted response, got error after %s: %v", elapsed, err)
	}
	if !strings.Contains(captured, sentinel) {
		t.Fatalf("returned pane does not contain the response sentinel %q", sentinel)
	}
	// Must return only after waiting out the backstop window (proves the backstop
	// drove it, not an unrelated early exit), and well before any hang.
	if elapsed < 2*time.Second {
		t.Fatalf("returned too early in %s; expected to wait out the ~3s stale-pane window", elapsed)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("took %s to return; the stale-pane backstop did not bound the wait", elapsed)
	}
}
