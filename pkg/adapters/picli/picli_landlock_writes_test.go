package picli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPiLandlockWritesTheLaunchFolder(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "launch-pi.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := piLandlockWrites([]string{"env", "--config", script})
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("writes = %v, want [%s]", got, dir)
	}
	if got := piLandlockWrites([]string{"pi", "--no-approve"}); len(got) != 0 {
		t.Fatalf("a launch without a script granted %v", got)
	}
}
