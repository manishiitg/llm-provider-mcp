package cursorcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const retainedWebApprovalPane = "Web Search: Notion MCP setup\nAllow this web search?\n → Allow search (y)\n Skip (esc or n)\n"

// A stateful tmux stand-in exercises the real broker, readiness parser, control
// handler and draft verification without making network requests or using an account.
func retainedControlsFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	pane := filepath.Join(dir, "pane")
	log := filepath.Join(dir, "keys")
	script := `#!/bin/sh
case "$1" in
capture-pane) cat "$CURSOR_TEST_PANE" ;;
send-keys)
  printf '%s\n' "$*" >> "$CURSOR_TEST_KEYS"
  case "$4" in
  y|Tab) printf '→ Add a follow-up\n' > "$CURSOR_TEST_PANE" ;;
  -l) printf '→ %s\n' "$6" > "$CURSOR_TEST_PANE" ;;
  C-m) printf 'Composing\nctrl+c to stop\n→ Add a follow-up\n' > "$CURSOR_TEST_PANE" ;;
  esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CURSOR_TEST_PANE", pane)
	t.Setenv("CURSOR_TEST_KEYS", log)
	if err := os.WriteFile(pane, []byte(retainedWebApprovalPane), 0o600); err != nil {
		t.Fatal(err)
	}
	return pane, log
}

func TestCursorRetainedControlsApproveWithoutResponseLoop(t *testing.T) {
	_, log := retainedControlsFixture(t)
	startCursorRetainedControls(t.Name(), true)
	t.Cleanup(func() { stopCursorRetainedControls(t.Name()) })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), "send-keys -t "+t.Name()+" y") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("retained web approval was never accepted without an active response loop")
}

func TestCursorRetainedApprovalDoesNotBlockLiveInput(t *testing.T) {
	_, log := retainedControlsFixture(t)
	startCursorRetainedControls(t.Name(), true)
	t.Cleanup(func() { stopCursorRetainedControls(t.Name()) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Start sending while the approval is still visible. The readiness wait
	// must leave the broker available for the watcher to press y.
	if err := sendCursorLiveInputToTmux(ctx, t.Name(), "Continue the setup"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	keys := string(data)
	approval := strings.Index(keys, " y\n")
	draft := strings.Index(keys, "-l -- Continue the setup")
	if approval < 0 || draft <= approval || strings.Count(keys, "-l -- Continue the setup") != 1 || strings.Count(keys, " C-m\n") != 1 {
		t.Fatalf("expected approval followed by exactly one submitted message, got %s", keys)
	}
}

func TestCursorRetainedControlsRespectPolicyAndStop(t *testing.T) {
	pane, log := retainedControlsFixture(t)
	startCursorRetainedControls(t.Name(), false)
	t.Cleanup(func() { stopCursorRetainedControls(t.Name()) })
	// Web approval must remain opt-in in both the handler and retained watcher.
	controls := cursorRuntimeControls{autoApproveWebSearch: false}
	if controls.handle(context.Background(), t.Name(), retainedWebApprovalPane) {
		t.Fatal("web approval handled without opt-in")
	}
	time.Sleep(600 * time.Millisecond)
	if data, _ := os.ReadFile(log); len(data) != 0 {
		t.Fatalf("unexpected approval: %s", data)
	}
	stopCursorRetainedControls(t.Name())
	if err := os.WriteFile(pane, []byte("→ Add a follow-up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startCursorRetainedControls(t.Name(), true)
	stopCursorRetainedControls(t.Name())
	if err := os.WriteFile(pane, []byte(retainedWebApprovalPane), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if data, _ := os.ReadFile(log); len(data) != 0 {
		t.Fatalf("stopped watcher injected keys: %s", data)
	}
}

func TestCursorLiveInputRechecksApprovalBeforeTyping(t *testing.T) {
	_, log := retainedControlsFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Simulate an approval appearing between the external readiness check
	// and acquiring the input broker. Only this pre-typing failure is retried.
	err := sendCursorInputToTmuxWithReadiness(ctx, t.Name(), "Continue", false, true)
	if !errors.Is(err, errCursorComposerChanged) {
		t.Fatalf("expected safe pre-typing retry, got %v", err)
	}
	if data, _ := os.ReadFile(log); len(data) != 0 {
		t.Fatalf("typed into an approval modal: %s", data)
	}
}
