package shelllaunch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	EnvShellMode = "CODING_AGENT_SHELL_MODE"
	EnvShellPath = "CODING_AGENT_LOGIN_SHELL"
)

// Command returns a tmux shell-command that starts a coding CLI from the
// caller's login shell. This lets GUI/DMG-launched servers pick up the same
// shell initialization a user expects when launching the CLI from Terminal.
func Command(args []string, workingDir string) string {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		workingDir = mustGetwd()
	}

	if directModeEnabled() {
		return DirectCommand(args, workingDir)
	}

	shellPath, kind, ok := resolveLoginShell()
	if !ok {
		return DirectCommand(args, workingDir)
	}

	switch kind {
	case "fish":
		return Join(append([]string{
			shellPath,
			"-lic",
			"cd $argv[1]; or exit; exec $argv[2..-1]",
			workingDir,
		}, args...))
	default:
		return Join(append([]string{
			shellPath,
			"-ilc",
			`cd "$1" || exit; shift; exec "$@"`,
			"coding-agent",
			workingDir,
		}, args...))
	}
}

func DirectCommand(args []string, workingDir string) string {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		workingDir = mustGetwd()
	}
	return "cd " + Quote(workingDir) + " && exec " + Join(args)
}

// CommandWithEnv returns a tmux shell-command that launches args with env
// variables without placing KEY=VALUE pairs in the tmux new-session argv. When
// env is non-empty it writes a 0600 self-deleting wrapper script containing the
// exports, then runs that script via /bin/sh. Call cleanup if tmux fails before
// the script starts; on successful launch the script removes itself.
func CommandWithEnv(args []string, workingDir string, env []string) (string, func(), error) {
	entries, err := parseEnvEntries(env)
	if err != nil {
		return "", nil, err
	}
	if len(entries) == 0 {
		return Command(args, workingDir), func() {}, nil
	}

	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		workingDir = mustGetwd()
	}

	script := launchScript(args, workingDir, entries)
	file, err := os.CreateTemp("", "mlp-coding-agent-launch-*.sh")
	if err != nil {
		return "", nil, fmt.Errorf("create launch script: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("chmod launch script: %w", err)
	}
	if _, err := file.WriteString(script); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write launch script: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close launch script: %w", err)
	}
	return Join([]string{"/bin/sh", path}), cleanup, nil
}

func Join(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = Quote(arg)
	}
	return strings.Join(quoted, " ")
}

func Quote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

type envEntry struct {
	key   string
	value string
}

func parseEnvEntries(env []string) ([]envEntry, error) {
	entries := make([]envEntry, 0, len(env))
	for _, raw := range env {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("invalid env entry %q: missing '='", key)
		}
		if !validEnvKey(key) {
			return nil, fmt.Errorf("invalid env key %q", key)
		}
		entries = append(entries, envEntry{key: key, value: value})
	}
	return entries, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		ch := key[i]
		if i == 0 {
			if !((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_') {
				return false
			}
			continue
		}
		if !((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return false
		}
	}
	return true
}

func launchScript(args []string, workingDir string, entries []envEntry) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("rm -f \"$0\"\n")
	for _, entry := range entries {
		b.WriteString("export ")
		b.WriteString(entry.key)
		b.WriteString("=")
		b.WriteString(Quote(entry.value))
		b.WriteString("\n")
	}

	if !directModeEnabled() {
		if shellPath, kind, ok := resolveLoginShell(); ok {
			switch kind {
			case "fish":
				b.WriteString("exec ")
				b.WriteString(Join(append([]string{
					shellPath,
					"-lic",
					"cd $argv[1]; or exit; exec $argv[2..-1]",
					workingDir,
				}, args...)))
				b.WriteString("\n")
				return b.String()
			default:
				b.WriteString("exec ")
				b.WriteString(Join(append([]string{
					shellPath,
					"-ilc",
					`cd "$1" || exit; shift; exec "$@"`,
					"coding-agent",
					workingDir,
				}, args...)))
				b.WriteString("\n")
				return b.String()
			}
		}
	}

	b.WriteString("cd ")
	b.WriteString(Quote(workingDir))
	b.WriteString(" || exit\n")
	b.WriteString("exec ")
	b.WriteString(Join(args))
	b.WriteString("\n")
	return b.String()
}

func directModeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvShellMode))) {
	case "direct", "none", "off", "false", "0":
		return true
	default:
		return false
	}
}

func resolveLoginShell() (string, string, bool) {
	candidates := []string{
		os.Getenv(EnvShellPath),
		os.Getenv("SHELL"),
		darwinUserShell(),
		passwdUserShell(),
		"/bin/zsh",
		"/bin/bash",
		"/bin/sh",
	}

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if !isExecutableAbsolutePath(candidate) {
			continue
		}
		kind := shellKind(candidate)
		if kind == "" {
			continue
		}
		return candidate, kind, true
	}
	return "", "", false
}

func shellKind(shellPath string) string {
	name := filepath.Base(shellPath)
	switch {
	case name == "fish":
		return "fish"
	case name == "zsh", name == "bash", name == "sh", name == "dash", name == "ksh":
		return "posix"
	default:
		return ""
	}
}

func isExecutableAbsolutePath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func darwinUserShell() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	current, err := user.Current()
	if err != nil || strings.TrimSpace(current.Username) == "" {
		return ""
	}
	username := strings.TrimPrefix(current.Username, "uid:")
	if idx := strings.LastIndex(username, `\`); idx >= 0 {
		username = username[idx+1:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dscl", ".", "-read", "/Users/"+username, "UserShell").Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	return strings.TrimSpace(strings.TrimPrefix(line, "UserShell:"))
}

func passwdUserShell() string {
	current, err := user.Current()
	if err != nil || strings.TrimSpace(current.Username) == "" {
		return ""
	}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	username := current.Username
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, username+":") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) >= 7 {
			return strings.TrimSpace(fields[6])
		}
	}
	return ""
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
