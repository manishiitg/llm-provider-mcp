//go:build linux

package musecli

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The /proc reader against real processes: a shell with a child named mcpbridge, then the same shell without it.
func TestMuseReadProcessTableSeesARealBridgeChild(t *testing.T) {
	dir := t.TempDir()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	bridge := filepath.Join(dir, "mcpbridge")
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bridge, data, 0o755); err != nil {
		t.Fatal(err)
	}
	pane := exec.Command("/bin/sh", "-c", bridge+" 30 & wait")
	pane.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := pane.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pane.Process.Pid, syscall.SIGKILL); _ = pane.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		procs, ok := museReadProcessTable()
		if ok && museHasDescendantNamed(pane.Process.Pid, procs, "mcpbridge") {
			// kill only the bridge: the pane (shell) stays, as in a Muse whose bridge died
			for pid, p := range procs {
				if p.ppid == pane.Process.Pid && p.comm == "mcpbridge" {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
			for time.Now().Before(deadline) {
				after, _ := museReadProcessTable()
				if !museHasDescendantNamed(pane.Process.Pid, after, "mcpbridge") {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("the bridge was killed but is still reported below the pane")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the bridge child of a real pane was never found in the process table")
}
