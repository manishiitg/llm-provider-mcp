package musecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const museRunningToolPane = `◆ Used sleep 240; inspect outputs — running (2m 43s · esc to interrupt)
──────────────────────────────
❯
──────────────────────────────
  muse-spark-1.3-contributor · xhigh · /tmp/work
`

func TestMuseRunningToolIsNotCompletedP0(t *testing.T) {
	if museTUIAtPrompt(museRunningToolPane) {
		t.Fatal("a frozen running tool must not count as a completed turn")
	}
	if !museTUIAtPrompt(museRunningToolPane + "\n◆ Finished successfully.\n❯\necho · /tmp/work\n") {
		t.Fatal("historical running text must not hide a later completed response")
	}
}

// Real tmux, synthetic tool output, no model or workflow side effects. Keep
// both the transcript and pane quiet past the completion quiet window, then
// finish the tool. This reproduces the shell-sleep case from production.
func TestMuseLongRunningToolWaitTmuxP0(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"busy":          museRunningToolPane,
		"idle":          "◆ Completed this turn.\n❯\necho · /tmp/work\n",
		"session.jsonl": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(dir, "session.jsonl")
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	session := "mlp-muse-lifetime-test-" + museRandomSessionSuffix()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session,
		"-x", "160", "-y", "30", "-c", dir, "sh", "-c",
		"cat busy; while [ ! -f done ]; do sleep 0.1; done; printf '\033[2J\033[H'; cat idle; exec sleep 30")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch scratch terminal: %v %s", err, out)
	}
	defer museKillTmuxSession(context.Background(), session)
	for {
		pane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(pane, "esc to interrupt") {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Explicit bounded callers still get their requested deadline.
	if _, err := museWaitTurnQuiescent(ctx, session, logPath, 100*time.Millisecond); err == nil {
		t.Fatal("explicit timeout ignored for a running tool")
	}
	// Readiness remains cancellable while a previous tool is running.
	readyCtx, readyCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err := museWaitAtPrompt(readyCtx, session, 0)
	readyCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness must honor caller deadline: %v", err)
	}
	turnCtx, turnCancel := context.WithCancel(ctx)
	turnCancel()
	if _, err := museWaitTurnQuiescent(turnCtx, session, logPath, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn wait must honor stop: %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := museWaitTurnQuiescent(ctx, session, logPath, 0)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("abandoned or completed an active tool: %v", err)
	case <-time.After(6 * time.Second):
	}
	if err := os.WriteFile(filepath.Join(dir, "done"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("did not recognize actual completion")
	}
}
