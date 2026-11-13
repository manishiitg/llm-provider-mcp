//go:build linux

package musecli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/internal/slotfs"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-872-119: auth set makes new 0700/0600 entries after the empty root
// was shared. Use fake auth output, then a real other-uid read on the host.
func TestMuseStoredKeyReadableOnlyBySelectedSlot(t *testing.T) {
	if os.Getenv("AGENTWORKS_TEST_SLOT_KEY") != "1" || os.Geteuid() != 0 {
		t.Skip("build-host root fixture with existing slot05 required")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	home := filepath.Join(state, "slot05", "home")
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "slots.json")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(`{"slot_state_root":%q,"slot_run_root":%q}`, state, filepath.Join(root, "run"))), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(slotfs.EnvConfig, cfg)
	bin := filepath.Join(root, "muse")
	fake := `#!/bin/sh
set -eu
mkdir -p "$XDG_CONFIG_HOME/muse"
chmod 700 "$XDG_CONFIG_HOME/muse"
cat > "$XDG_CONFIG_HOME/muse/auth.json"
chmod 600 "$XDG_CONFIG_HOME/muse/auth.json"
`
	if err := os.WriteFile(bin, []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	opts := &llmtypes.CallOptions{CLISecurity: &llmtypes.CLISecurityPolicy{PrivateHome: home}}
	config := filepath.Join(root, "config")
	key, cleanup, err := NewMuseCLIAdapter("", "", nil).museMSPStoreKey(context.Background(), opts, root, config, "dummy-regression-key", os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, tc := range []struct {
		user    string
		allowed bool
	}{{"slot05", true}, {"slot06", false}} {
		err := exec.Command("runuser", "-u", tc.user, "--", "test", "-r", key).Run()
		if (err == nil) != tc.allowed {
			t.Fatalf("%s readable=%v, want %v", tc.user, err == nil, tc.allowed)
		}
	}
	if _, err := os.Stat(filepath.Join(config, "muse", "auth.json")); err != nil {
		t.Fatal(err)
	}
}
