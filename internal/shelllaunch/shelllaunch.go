package shelllaunch

import (
	"context"
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
