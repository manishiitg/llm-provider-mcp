//go:build !windows

package claudecode

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// PLAT-746-859: losing the in-memory registry must not leave a second CLI for
// the same chat. Use real tmux panes and processes, with no model or login.
func TestClaudeColdRegistryClosesOnlySameChatTmuxCopies(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux required")
	}
	dir, err := os.MkdirTemp("/tmp", "claude-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// This server was created only for this test, never the user's tmux.
	defer exec.Command("tmux", "kill-server").Run()
	owner := "test-chat"
	prefix := claudePersistentTmuxOwnerPrefix(owner)
	other := claudePersistentTmuxOwnerPrefix("another-chat") + "live"
	start := func(name string) int {
		t.Helper()
		run("new-session", "-d", "-s", name, "exec sleep 60")
		pid, err := strconv.Atoi(run("list-panes", "-t", "="+name, "-F", "#{pane_pid}"))
		if err != nil {
			t.Fatal(err)
		}
		return pid
	}
	otherPID := start(other)
	oldPIDs := []int{start(prefix + "first"), start(prefix + "second")}
	for i := 0; i < 2; i++ {
		// No persistent registry entries exist, as after a backend restart.
		closePreviousClaudeTmuxCopies(ctx, owner)
		for _, pid := range oldPIDs {
			if err := syscall.Kill(pid, 0); err == nil {
				t.Fatalf("previous chat process %d survived cold acquisition", pid)
			}
		}
		if err := syscall.Kill(otherPID, 0); err != nil {
			t.Fatalf("another chat's process was stopped: %v", err)
		}
		oldPIDs = []int{start(prefix + randomHex(8))}
		names := strings.Fields(run("list-sessions", "-F", "#{session_name}"))
		if len(names) != 2 {
			t.Fatalf("reopen %d left %d sessions; want one per chat: %v", i, len(names), names)
		}
	}
}
