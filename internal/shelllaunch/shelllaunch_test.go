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

func TestCommandWithEnvKeepsSecretsOutOfCommandString(t *testing.T) {
	t.Setenv(EnvShellMode, "direct")

	got, cleanup, err := CommandWithEnv(
		[]string{"gemini", "--model", "gemini-3.1"},
		"/tmp/user chat",
		[]string{"GEMINI_API_KEY=secret-value", "GOOGLE_API_KEY=google-secret"},
	)
	if err != nil {
		t.Fatalf("CommandWithEnv error = %v", err)
	}
	defer cleanup()

	if strings.Contains(got, "secret-value") || strings.Contains(got, "google-secret") {
		t.Fatalf("command string leaked secret: %q", got)
	}
	if strings.Contains(got, "GEMINI_API_KEY=") || strings.Contains(got, "GOOGLE_API_KEY=") {
		t.Fatalf("command string leaked env names/values: %q", got)
	}
	if !strings.Contains(got, Quote("/bin/sh")) {
		t.Fatalf("command = %q, want /bin/sh wrapper", got)
	}

	parts := strings.Split(got, "'")
	var scriptPath string
	for _, part := range parts {
		if strings.Contains(part, "mlp-coding-agent-launch-") {
			scriptPath = part
			break
		}
	}
	if scriptPath == "" {
		t.Fatalf("command = %q, missing launch script path", got)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat launch script: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("launch script mode = %#o, want 0600", gotMode)
	}
	body, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read launch script: %v", err)
	}
	if !strings.Contains(string(body), "secret-value") || !strings.Contains(string(body), "rm -f \"$0\"") {
		t.Fatalf("launch script missing expected secret export/self-delete: %s", string(body))
	}
}

func TestCommandWithEnvRejectsInvalidKey(t *testing.T) {
	_, cleanup, err := CommandWithEnv([]string{"cmd"}, "/tmp/work", []string{"BAD-KEY=value"})
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("CommandWithEnv error = nil, want invalid key error")
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
