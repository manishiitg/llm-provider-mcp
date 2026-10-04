package musecli

import (
	"os"
	"path/filepath"
	"testing"
)

// The sweep prelude's `exec "$@"` runs where PATH is only the system folders; a bare "muse" there was not found and every confined launch died at once
// (Excellence, 2026-10-04). The launch must carry the absolute path this process resolves.
func TestPreludeArgvCarriesTheAbsoluteMuseBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "muse")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")

	argv := musePreludeArgv(nil, []string{"muse", "--trust-workspace", "--model", "m"})
	want := []string{"sh", "-c", museSweepScript, "muse-sweep"}
	for i, w := range want {
		if argv[i] != w {
			t.Fatalf("prelude arg %d = %q, want %q (%v)", i, argv[i], w, argv)
		}
	}
	// argv[4] is the temp folder; the command follows it.
	if argv[5] != bin || argv[6] != "--trust-workspace" || argv[7] != "--model" || argv[8] != "m" {
		t.Fatalf("command after the prelude = %v, want %s and the original flags", argv[5:], bin)
	}

	// Only the bare name is replaced: a flag value or another argument named like it, and a later "muse", stay.
	argv = museArgvWithAbsoluteBinary([]string{"env", "XDG_CONFIG_HOME=/x", "muse", "--provider", "muse"})
	if argv[2] != bin || argv[4] != "muse" {
		t.Fatalf("only the first bare muse is replaced: %v", argv)
	}
	// The caller's slice is not modified.
	in := []string{"muse"}
	_ = museArgvWithAbsoluteBinary(in)
	if in[0] != "muse" {
		t.Fatalf("input modified: %v", in)
	}
}

func TestPreludeArgvKeepsTheBareNameWhenMuseIsNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	argv := museArgvWithAbsoluteBinary([]string{"muse", "--x"})
	if argv[0] != "muse" || argv[1] != "--x" {
		t.Fatalf("unchanged expected: %v", argv)
	}
}
