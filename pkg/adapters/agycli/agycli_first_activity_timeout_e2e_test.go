package agycli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestAgyInteractiveResponseFailsFastWhenNoActivityE2E is the regression guard
// for the production hang where a workflow-step's prompt was never delivered into
// the agy tmux pane: the pane sat at an idle prompt, the CLI showed zero activity,
// and waitForAgyInteractiveResponse spun forever because every completion/failsafe
// branch is gated behind sawActivity AND the call context had no deadline (both the
// provider default and the workflow caller run with timeout=0).
//
// It is hermetic: it drives a real detached tmux session that prints a static,
// agy-like idle pane and then sleeps — i.e. a pane that never produces activity,
// exactly like the stuck session. With the fix, waitForAgyInteractiveResponse must
// return a "no activity" error within the first-activity window instead of hanging.
func TestAgyInteractiveResponseFailsFastWhenNoActivityE2E(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not in PATH; agy tmux e2e requires tmux")
	}
	// Short, deterministic first-activity window so the test is fast.
	t.Setenv(EnvAgyInteractiveFirstActivityTimeoutSeconds, "3")

	const sentinel = "AGY_E2E_STATIC_PANE"
	sessionName := newAgyTmuxSessionName()
	// A pane that renders a static, neutral idle prompt and never changes — no
	// spinner/"Thinking…"/streaming, so hasAgyActivity stays false and no pane
	// delta is ever produced. This is the "input never landed" state.
	cmd := []string{"sh", "-c", "printf '" + sentinel + "\\n> '; sleep 600"}

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

	// Wait until the static pane has rendered, then snapshot it as the baseline
	// so every subsequent capture yields an empty delta (== no activity).
	waitForAgyRealPaneContains(t, sessionName, sentinel, 10*time.Second, make(chan error))
	baseline, err := captureAgyPane(ctx, sessionName)
	if err != nil {
		t.Fatalf("capture baseline pane: %v", err)
	}

	started := time.Now()
	_, err = waitForAgyInteractiveResponse(ctx, sessionName, baseline, "what is 2+2?", nil, nil, false)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("expected a no-activity error, got nil after %s", elapsed)
	}
	if !strings.Contains(err.Error(), "no activity") {
		t.Fatalf("error = %q, want it to mention %q", err.Error(), "no activity")
	}
	// Must fail fast (proves it no longer hangs), but only after waiting out the
	// configured window (proves the cutoff drives it, not an unrelated early exit).
	if elapsed < 2*time.Second {
		t.Fatalf("returned too early in %s; expected to wait out the ~3s first-activity window", elapsed)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("took %s to fail; the no-activity cutoff did not bound the wait", elapsed)
	}
}

// TestAgyCLIRealInteractiveResponseFailsFastWhenNoInputDeliveredE2E is the
// real-agy fidelity check for the same regression: launch a live Antigravity CLI
// tmux session, let it reach its ready prompt, then deliberately DO NOT deliver
// any prompt (the exact production "lost paste" state, where the pane stayed a
// byte-static idle `>` for 18+ minutes). waitForAgyInteractiveResponse must now
// surface a no-activity error within the first-activity window instead of hanging
// forever on a deadline-less context.
//
// Gated by RUN_AGY_CLI_REAL_E2E; requires a signed-in `agy`.
func TestAgyCLIRealInteractiveResponseFailsFastWhenNoInputDeliveredE2E(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Setenv(EnvAgyInteractiveFirstActivityTimeoutSeconds, "8")

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	workDir, err := os.MkdirTemp("/private/tmp", "agy-real-noinput-work-*")
	if err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)
	WithDangerouslySkipPermissions(true)(opts)

	args, env, workingDir, cleanupFiles, err := adapter.buildAgyInteractiveLaunch(opts, "", "agy-real-noinput-"+agyRandomHex(4))
	if err != nil {
		t.Fatalf("build launch: %v", err)
	}
	t.Cleanup(cleanupFiles)

	sessionName := newAgyTmuxSessionName()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := startAgyTmuxSession(ctx, sessionName, args, env, workingDir); err != nil {
		t.Fatalf("start agy tmux session: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		requestAgyGracefulExit(closeCtx, sessionName)
		_ = killAgyTmuxSession(closeCtx, sessionName)
	})

	// Drive the live CLI to its ready prompt exactly like a real turn would,
	// but stop there — never call sendAgyInputToTmux.
	if _, err := waitForAgyPromptWithTrustSignal(ctx, sessionName, nil); err != nil {
		if errors.Is(err, errAgyAuthRequired) {
			t.Skipf("agy is not signed in on this machine: %v", err)
		}
		t.Fatalf("wait for agy ready prompt: %v", err)
	}
	// The splash→ready-prompt transition keeps repainting the pane for a beat
	// after the prompt first appears. Production's stuck pane was byte-static, so
	// wait for the pane to settle (unchanged for 2s) before snapshotting the
	// baseline; otherwise residual launch frames look like spurious "activity".
	baseline := waitForAgyPaneStable(t, ctx, sessionName, 2*time.Second, 30*time.Second)

	started := time.Now()
	_, err = waitForAgyInteractiveResponse(ctx, sessionName, baseline, "what is 2+2?", nil, nil, false)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("expected a no-activity error, got nil after %s", elapsed)
	}
	if !strings.Contains(err.Error(), "no activity") {
		pane, _ := captureAgyPane(context.Background(), sessionName)
		t.Fatalf("error = %q, want it to mention %q; pane:\n%s", err.Error(), "no activity", pane)
	}
	if elapsed > 40*time.Second {
		t.Fatalf("took %s to fail; the no-activity cutoff did not bound the live wait", elapsed)
	}
	t.Logf("live agy idle pane failed fast in %s with: %v", elapsed, err)
}

// waitForAgyPaneStable returns the pane contents once they have been unchanged
// for stableFor, or fails after timeout. Used to snapshot a settled baseline so
// the response loop sees genuinely empty deltas (matching the production hang).
func waitForAgyPaneStable(t *testing.T, ctx context.Context, sessionName string, stableFor, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var prev string
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		cur, err := captureAgyPane(ctx, sessionName)
		if err != nil {
			t.Fatalf("capture pane while waiting for stability: %v", err)
		}
		if cur != prev {
			prev = cur
			stableSince = time.Now()
		} else if cur != "" && time.Since(stableSince) >= stableFor {
			return cur
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("agy pane did not stabilize within %s", timeout)
	return ""
}
