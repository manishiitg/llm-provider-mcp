//go:build linux

package codexcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCodexDetachedLaunchKeepsPolicyUntilPaneStarts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("HOME", root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_LAUNCH", filepath.Join(root, "launch.sh"))
	// tmux acknowledges the detached session before its pane executes. Keep
	// the captured command unexecuted until startCodexTmuxSession returns.
	fakeTmux := `#!/bin/sh
case " $* " in *" new-session "*)
  for arg do last="$arg"; done
  printf '%s\n' "$last" > "$TMUX_TEST_LAUNCH"
esac
exit 0
`
	fakeRunner := `#!/bin/sh
test -r "$2" || { echo "missing launch policy"; exit 125; }
rm "$2"
shift 3
exec "$@"
`
	for name, source := range map[string]string{"tmux": fakeTmux, "runner": fakeRunner} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	policy := &llmtypes.CLISecurityPolicy{Mode: llmtypes.CLISecurityModeIsolated, Provider: "codex-cli", LandlockRunner: filepath.Join(root, "runner"), PrivateHome: filepath.Join(root, "home"), CredentialHome: root}
	cleanup, err := startCodexTmuxSession(context.Background(), "delayed-pane", []string{"/bin/sh", "-c", "printf launch-ok"}, root, policy, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	command, err := os.ReadFile(filepath.Join(root, "launch.sh"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", string(command)).Output()
	if err != nil || strings.TrimSpace(string(out)) != "launch-ok" {
		t.Fatalf("detached pane could not consume its policy after startup returned: %s (%v)", out, err)
	}
}

func TestCodexFailedDetachedLaunchRemovesPolicy(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("HOME", root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, source := range map[string]string{"tmux": "#!/bin/sh\nexit 1\n", "runner": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	policy := &llmtypes.CLISecurityPolicy{Mode: llmtypes.CLISecurityModeIsolated, Provider: "codex-cli", LandlockRunner: filepath.Join(root, "runner"), PrivateHome: filepath.Join(root, "home"), CredentialHome: root}
	_, err := startCodexTmuxSession(context.Background(), "failed-pane", []string{"codex"}, root, policy, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("failed launch returned success")
	}
	files, err := filepath.Glob(filepath.Join(root, "agentworks-cli-landlock-*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("failed launch leaked policy files: %v (%v)", files, err)
	}
}
