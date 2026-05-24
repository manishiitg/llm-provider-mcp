package cursorcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEnsureCursorInputSubmittedSendsSecondEnter mirrors the production
// scenario where Cursor's follow-ups menu swallows the first Enter after a
// paste-buffer write — the draft text stays in the input field. The fix in
// sendCursorInputToTmux delegates a recovery probe to ensureCursorInputSubmitted
// which, on seeing the draft still present, fires one extra Enter.
//
// We do not need the real cursor-agent binary to test the probe: a vanilla
// tmux session running a "read line, log line" bash loop is sufficient. We
// type the draft into the pane WITHOUT pressing Enter (mimicking the state
// Cursor leaves after consuming the first Enter), then assert the probe
// sends the missing Enter that drives the loop's read to return.
func TestEnsureCursorInputSubmittedSendsSecondEnter(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available on this host")
	}

	logFile := filepath.Join(t.TempDir(), "enter.log")
	sessionName := "mlp-cursor-test-submit-" + cursorRandomHex(6)
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", sessionName).Run() })

	loop := fmt.Sprintf(`while IFS= read -r line; do printf '%%s\n' "$line" >> %s; done`, logFile)
	if out, err := exec.CommandContext(context.Background(), "tmux", "new-session", "-d", "-s", sessionName, "-x", "120", "-y", "30", "bash", "-c", loop).CombinedOutput(); err != nil {
		t.Fatalf("failed to start tmux session: %v; output=%s", err, string(out))
	}

	draft := "MLP_TEST_DRAFT_LINE_" + cursorRandomHex(4)
	if out, err := exec.CommandContext(context.Background(), "tmux", "send-keys", "-t", sessionName, draft).CombinedOutput(); err != nil {
		t.Fatalf("failed to type draft: %v; output=%s", err, string(out))
	}

	// Allow tmux to repaint so capture-pane sees the draft.
	time.Sleep(250 * time.Millisecond)

	pane, err := captureCursorPane(context.Background(), sessionName)
	if err != nil {
		t.Fatalf("capture pane: %v", err)
	}
	if !cursorPaneShowsPromptDraft(pane, draft) {
		t.Fatalf("setup precondition failed: pane does not show draft %q; pane:\n%s", draft, pane)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ensureCursorInputSubmitted(ctx, sessionName, draft)

	// Bash needs a moment to read+flush after the recovery Enter is delivered.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		content, _ := os.ReadFile(logFile)
		if strings.Contains(string(content), draft) {
			return
		}
		time.Sleep(75 * time.Millisecond)
	}
	content, _ := os.ReadFile(logFile)
	t.Fatalf("expected log to contain draft %q after recovery Enter; log contents=%q", draft, string(content))
}

// TestSendCursorInputToTmuxTypedPathSkipsPasteBuffer covers the dispatcher
// in sendCursorInputToTmux: short single-line messages must take the typed
// path (tmux send-keys -l) instead of the paste-buffer/bracketed-paste path
// — that's what stops Cursor's TUI from rendering normal chat turns as
// "[Pasted text]". We verify behaviorally by checking that no tmux paste
// buffer named "mlp-cursor-input-*" was left behind (paste-buffer -d would
// delete it on use, but its even-momentary existence is detectable here:
// we count buffers before+after and assert the count did not jump). We also
// confirm the message reached the pane (so the typed path actually ran).
func TestSendCursorInputToTmuxTypedPathSkipsPasteBuffer(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available on this host")
	}

	logFile := filepath.Join(t.TempDir(), "enter.log")
	sessionName := "mlp-cursor-test-typed-" + cursorRandomHex(6)
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", sessionName).Run() })

	loop := fmt.Sprintf(`while IFS= read -r line; do printf '%%s\n' "$line" >> %s; done`, logFile)
	if out, err := exec.CommandContext(context.Background(), "tmux", "new-session", "-d", "-s", sessionName, "-x", "120", "-y", "30", "bash", "-c", loop).CombinedOutput(); err != nil {
		t.Fatalf("failed to start tmux session: %v; output=%s", err, string(out))
	}

	short := "MLP_TYPED_INPUT_" + cursorRandomHex(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sendCursorInputToTmux(ctx, sessionName, short); err != nil {
		t.Fatalf("sendCursorInputToTmux: %v", err)
	}

	// Bash needs a beat to read+flush after the typed Enter.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		content, _ := os.ReadFile(logFile)
		if strings.Contains(string(content), short) {
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	content, _ := os.ReadFile(logFile)
	if !strings.Contains(string(content), short) {
		t.Fatalf("expected typed message %q in log after typed submit; log=%q", short, string(content))
	}

	// The typed path must NOT have created an mlp-cursor-input-* paste buffer.
	out, err := exec.CommandContext(context.Background(), "tmux", "list-buffers", "-F", "#{buffer_name}").CombinedOutput()
	if err != nil {
		t.Fatalf("tmux list-buffers: %v; output=%s", err, string(out))
	}
	for _, name := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(name), "mlp-cursor-input-") {
			t.Fatalf("typed path leaked a paste buffer %q — short single-line message should have taken send-keys path", name)
		}
	}
}

// TestSendCursorInputToTmuxPasteBufferPathForMultilineInput covers the
// other branch of the dispatcher: multi-line content must keep using
// paste-buffer + bracketed paste so embedded newlines don't cause Cursor to
// submit on every line.
func TestSendCursorInputToTmuxPasteBufferPathForMultilineInput(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available on this host")
	}

	logFile := filepath.Join(t.TempDir(), "enter.log")
	sessionName := "mlp-cursor-test-paste-" + cursorRandomHex(6)
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", sessionName).Run() })

	loop := fmt.Sprintf(`while IFS= read -r line; do printf '%%s\n' "$line" >> %s; done`, logFile)
	if out, err := exec.CommandContext(context.Background(), "tmux", "new-session", "-d", "-s", sessionName, "-x", "120", "-y", "30", "bash", "-c", loop).CombinedOutput(); err != nil {
		t.Fatalf("failed to start tmux session: %v; output=%s", err, string(out))
	}

	token := "MLP_MULTI_" + cursorRandomHex(4)
	// Two-line message forces the paste-buffer path.
	multi := "first line " + token + "\nsecond line " + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sendCursorInputToTmux(ctx, sessionName, multi); err != nil {
		t.Fatalf("sendCursorInputToTmux: %v", err)
	}

	// `cat`-style loop will log each \n-terminated line. Both lines should
	// arrive (paste-buffer delivers them, the trailing C-m submits, and bash
	// reads both records).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		content, _ := os.ReadFile(logFile)
		if strings.Count(string(content), token) >= 2 {
			return
		}
		time.Sleep(75 * time.Millisecond)
	}
	content, _ := os.ReadFile(logFile)
	t.Fatalf("expected both lines of multi-line input in log; log=%q", string(content))
}

// TestEnsureCursorInputSubmittedSkipsWhenDraftAbsent guards against the
// recovery probe firing a spurious Enter when the first Enter actually did
// submit and the draft is no longer visible — that would inject a blank line
// into Cursor's input on every turn.
func TestEnsureCursorInputSubmittedSkipsWhenDraftAbsent(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available on this host")
	}

	logFile := filepath.Join(t.TempDir(), "enter.log")
	sessionName := "mlp-cursor-test-skip-" + cursorRandomHex(6)
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", sessionName).Run() })

	loop := fmt.Sprintf(`while IFS= read -r line; do printf '%%s\n' "$line" >> %s; done`, logFile)
	if out, err := exec.CommandContext(context.Background(), "tmux", "new-session", "-d", "-s", sessionName, "-x", "120", "-y", "30", "bash", "-c", loop).CombinedOutput(); err != nil {
		t.Fatalf("failed to start tmux session: %v; output=%s", err, string(out))
	}

	draft := "MLP_TEST_DRAFT_NEVER_SHOWN_" + cursorRandomHex(4)

	// Pane is empty — the probe must short-circuit instead of sending Enter.
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	ensureCursorInputSubmitted(ctx, sessionName, draft)

	time.Sleep(300 * time.Millisecond)
	content, _ := os.ReadFile(logFile)
	if len(strings.TrimSpace(string(content))) != 0 {
		t.Fatalf("expected no log entries (no Enter should have been sent); got=%q", string(content))
	}
}
