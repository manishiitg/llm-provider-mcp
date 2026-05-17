package shelllaunch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandUsesLoginShellByDefault(t *testing.T) {
	shell := writeExecutableShell(t, "zsh")
	t.Setenv(EnvShellPath, shell)
	t.Setenv("SHELL", "")
	t.Setenv(EnvShellMode, "")

	got := Command([]string{"claude", "--flag", "value"}, "/tmp/user chat")

	if !strings.HasPrefix(got, Quote(shell)+" '-ilc' ") {
		t.Fatalf("command = %q, want login shell -ilc prefix", got)
	}
	for _, want := range []string{
		Quote(`cd "$1" || exit; shift; exec "$@"`),
		"'coding-agent'",
		"'/tmp/user chat'",
		"'claude'",
		"'--flag'",
		"'value'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("command = %q, missing %s", got, want)
		}
	}
}

func TestCommandUsesFishSyntaxForFishLoginShell(t *testing.T) {
	shell := writeExecutableShell(t, "fish")
	t.Setenv(EnvShellPath, shell)
	t.Setenv("SHELL", "")
	t.Setenv(EnvShellMode, "")

	got := Command([]string{"gemini"}, "/tmp/work")

	if !strings.HasPrefix(got, Quote(shell)+" '-lic' ") {
		t.Fatalf("command = %q, want fish -lic prefix", got)
	}
	if !strings.Contains(got, Quote("cd $argv[1]; or exit; exec $argv[2..-1]")) {
		t.Fatalf("command = %q, missing fish script", got)
	}
}

func TestCommandDirectModeBypassesLoginShell(t *testing.T) {
	shell := writeExecutableShell(t, "zsh")
	t.Setenv(EnvShellPath, shell)
	t.Setenv(EnvShellMode, "direct")

	got := Command([]string{"codex", "--no-alt-screen"}, "/tmp/user chat")

	if !strings.HasPrefix(got, "cd '/tmp/user chat' && exec ") {
		t.Fatalf("command = %q, want direct cd/exec form", got)
	}
	if strings.Contains(got, "-ilc") || strings.Contains(got, Quote(shell)) {
		t.Fatalf("command = %q, direct mode should not invoke login shell", got)
	}
}

func TestCommandSkipsUnsupportedShellOverride(t *testing.T) {
	unsupported := writeExecutableShell(t, "tcsh")
	supported := writeExecutableShell(t, "zsh")
	t.Setenv(EnvShellPath, unsupported)
	t.Setenv("SHELL", supported)
	t.Setenv(EnvShellMode, "")

	got := Command([]string{"claude"}, "/tmp/work")

	if strings.Contains(got, Quote(unsupported)) {
		t.Fatalf("command = %q, should not use unsupported shell", got)
	}
	if !strings.HasPrefix(got, Quote(supported)+" '-ilc' ") {
		t.Fatalf("command = %q, want supported shell fallback", got)
	}
}

func writeExecutableShell(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	return path
}
