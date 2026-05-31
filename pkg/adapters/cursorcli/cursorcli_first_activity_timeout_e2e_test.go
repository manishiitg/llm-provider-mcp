package cursorcli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCursorInteractiveResponseFailsFastWhenNoActivityE2E guards against the
// silent-hang class: a workflow step whose prompt is never delivered into the
// Cursor tmux pane used to spin forever in waitForCursorInteractiveResponse —
// every completion/failsafe branch is gated behind sawActivity, and the call
// context carries no deadline by default (both the provider default and the
// workflow caller run with timeout=0).
//
// Hermetic: drives a real detached tmux session that prints a static, idle pane
// and then sleeps — a pane that never produces activity, exactly like the stuck
// session. With the fix, waitForCursorInteractiveResponse must return a
// "no activity" error within the first-activity window instead of hanging.
func TestCursorInteractiveResponseFailsFastWhenNoActivityE2E(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not in PATH; cursor tmux e2e requires tmux")
	}
	t.Setenv(EnvCursorInteractiveFirstActivityTimeoutSeconds, "3")

	const sentinel = "CURSOR_E2E_STATIC_PANE"
	sessionName := newCursorTmuxSessionName()
	// A pane that renders a static idle prompt and never changes — no spinner /
	// "Generating…" / streaming, so hasCursorActivity stays false and no pane
	// delta is ever produced. This is the "input never landed" state.
	cmd := []string{"sh", "-c", "printf '" + sentinel + "\\n> '; sleep 600"}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := startCursorTmuxSession(ctx, sessionName, cmd, nil, t.TempDir()); err != nil {
		t.Fatalf("start static tmux session: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = killCursorTmuxSession(closeCtx, sessionName)
	})

	waitForCursorRealPaneContains(t, sessionName, sentinel, 10*time.Second, make(chan error))
	baseline, err := captureCursorPane(ctx, sessionName)
	if err != nil {
		t.Fatalf("capture baseline pane: %v", err)
	}

	started := time.Now()
	_, err = waitForCursorInteractiveResponse(ctx, sessionName, baseline, "what is 2+2?", nil, nil, false)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("expected a no-activity error, got nil after %s", elapsed)
	}
	if !strings.Contains(err.Error(), "no activity") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "no activity")
	}
	if elapsed < 2*time.Second {
		t.Fatalf("returned too early in %s; expected to wait out the ~3s first-activity window", elapsed)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("took %s to fail; the no-activity cutoff did not bound the wait", elapsed)
	}
}
