package clisandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// A launch may begin with `sh -c <script> <name> <arg>` (Muse's probe sweep): the folder to grant is the real command's, not the shell's. Before this
// the sweep made the lookup stop at `sh`, so a CLI installed outside the system baseline lost its install-folder grant.
func TestExecutableDirsLooksPastAShellPrelude(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakecli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := executableDirs([]string{"env", "FOO=1", bin, "--flag"})
	if len(plain) == 0 || plain[0] != dir {
		t.Fatalf("plain launch: %v, want %s first", plain, dir)
	}
	withPrelude := executableDirs([]string{"sh", "-c", "exec \"$@\"", "sweep", "/tmp/x", bin, "--flag"})
	if len(withPrelude) == 0 || withPrelude[0] != dir {
		t.Fatalf("prelude launch: %v, want %s first", withPrelude, dir)
	}
	// A plain `sh -c` command (no prelude shape) is left alone.
	if got := executableDirs([]string{"sh", "-c", "echo hi"}); len(got) == 0 {
		t.Fatalf("sh itself should still resolve: %v", got)
	}
}
