//go:build darwin

package clisandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// The real macOS sandbox, not just the profile text: a granted folder is
// readable and writable, the rest of the home is not, and a blocked path
// inside a granted folder is refused.
func TestSeatbeltConfinesUnderTheRealSandbox(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	root, err := os.MkdirTemp(home, ".agentworks-seatbelt-test-")
	if err != nil {
		t.Skipf("cannot create a folder under the home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	granted := filepath.Join(root, "project")
	outside := filepath.Join(root, "secret")
	blockedDir := filepath.Join(granted, "planning")
	for _, dir := range []string{granted, outside, blockedDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(granted, "ok.txt"), filepath.Join(outside, "key.txt"), filepath.Join(blockedDir, "plan.json")} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	policy := &llmtypes.CLISecurityPolicy{
		Mode:              llmtypes.CLISecurityModeIsolated,
		Provider:          "claude-code",
		Seatbelt:          true,
		PrivateHome:       filepath.Join(granted, ".sandbox", "cli-home"),
		BlockedWritePaths: []string{blockedDir},
	}
	run := func(script string) error {
		args, _, err := SeatbeltArgs(policy, []string{"/bin/sh", "-c", script}, granted, SeatbeltGrants{})
		if err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(context.Background(), args[0], args[1:]...).Run()
	}
	if err := run("cat " + filepath.Join(granted, "ok.txt")); err != nil {
		t.Errorf("reading the granted folder failed: %v", err)
	}
	if err := run("echo y > " + filepath.Join(granted, "new.txt")); err != nil {
		t.Errorf("writing the granted folder failed: %v", err)
	}
	if err := run("cat " + filepath.Join(outside, "key.txt")); err == nil {
		t.Error("read a file outside the grants")
	}
	if err := run("echo y > " + filepath.Join(outside, "new.txt")); err == nil {
		t.Error("wrote outside the grants")
	}
	if err := run("echo y > " + filepath.Join(blockedDir, "plan.json")); err == nil {
		t.Error("wrote a blocked path inside a granted folder")
	}
	if err := run("cat " + filepath.Join(blockedDir, "plan.json")); err != nil {
		t.Errorf("a write-blocked path must stay readable: %v", err)
	}
}

func TestSeatbeltArgsIsANoOpWithoutTheRequest(t *testing.T) {
	policy := &llmtypes.CLISecurityPolicy{Mode: llmtypes.CLISecurityModeIsolated, PrivateHome: t.TempDir()}
	args, _, err := SeatbeltArgs(policy, []string{"claude"}, t.TempDir(), SeatbeltGrants{})
	if err != nil || strings.Join(args, " ") != "claude" {
		t.Fatalf("args = %v, err = %v", args, err)
	}
}
