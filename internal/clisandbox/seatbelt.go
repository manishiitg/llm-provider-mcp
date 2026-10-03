package clisandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// seatbeltExec is macOS's sandbox launcher (a variable for tests).
var seatbeltExec = "/usr/bin/sandbox-exec"

// SeatbeltGrants are what one CLI needs beyond the policy's folders: files the
// adapter prepared for this launch, and the CLI's own config under the real
// home (its login and settings live there on a Mac). WritePatterns are
// regular expressions for files the CLI replaces by write-and-rename next to
// its config (e.g. ~/.claude.json.tmp.123), which a folder grant cannot name.
type SeatbeltGrants struct {
	ReadPaths     []string
	WritePaths    []string
	WritePatterns []string
}

// SeatbeltArgs wraps a coding CLI's argv so it starts under macOS sandbox-exec.
// It returns args unchanged when the policy does not ask for Seatbelt on this
// host. Everything outside /Users, /Volumes and /Network stays as it is (dyld,
// networking, TTYs and system services have undocumented dependencies there);
// under them only the granted folders can be read or written, and blocked
// paths are refused even inside a granted folder.
func SeatbeltArgs(policy *llmtypes.CLISecurityPolicy, args []string, workingDir string, grants SeatbeltGrants) ([]string, func(), error) {
	noop := func() {}
	if !policy.SeatbeltEnforced() {
		return args, noop, nil
	}
	if len(args) == 0 {
		return nil, noop, errors.New("coding CLI launch command is empty")
	}
	if _, err := os.Stat(seatbeltExec); err != nil {
		return nil, noop, fmt.Errorf("%w: sandbox-exec unavailable: %w", ErrUnsupported, err)
	}
	home := canonical(policy.PrivateHome)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, noop, fmt.Errorf("create CLI sandbox folder: %w", err)
	}
	profile := seatbeltProfile(policy, args, workingDir, grants)
	path := filepath.Join(home, "agentworks-cli-seatbelt.sb")
	// A stable file, replaced atomically: sandbox-exec reads it as the CLI
	// starts, which may be after a tmux launch returns, so it is never deleted.
	tmp, err := os.CreateTemp(home, ".agentworks-cli-seatbelt-*")
	if err != nil {
		return nil, noop, fmt.Errorf("create CLI Seatbelt profile: %w", err)
	}
	_, err = tmp.WriteString(profile)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return nil, noop, fmt.Errorf("write CLI Seatbelt profile: %w", err)
	}
	wrapped := append([]string{seatbeltExec, "-f", path, "/usr/bin/env"}, args...)
	return wrapped, noop, nil
}

// SeatbeltCmd confines a structured-transport launch (exec.Cmd) the same way.
func SeatbeltCmd(policy *llmtypes.CLISecurityPolicy, cmd *exec.Cmd, workingDir string, grants SeatbeltGrants) (func(), error) {
	if !policy.SeatbeltEnforced() {
		return func() {}, nil
	}
	if cmd == nil || len(cmd.Args) == 0 {
		return func() {}, errors.New("coding CLI command is empty")
	}
	if workingDir == "" {
		workingDir = cmd.Dir
	}
	args := append([]string{cmd.Path}, cmd.Args[1:]...)
	grants.ReadPaths = append(grants.ReadPaths, ArgFilePaths(args)...)
	wrapped, cleanup, err := SeatbeltArgs(policy, args, workingDir, grants)
	if err != nil {
		return func() {}, err
	}
	cmd.Path = wrapped[0]
	cmd.Args = wrapped
	return cleanup, nil
}

func seatbeltProfile(policy *llmtypes.CLISecurityPolicy, args []string, workingDir string, grants SeatbeltGrants) string {
	write := []string{workingDir, policy.PrivateHome}
	write = append(write, policy.WorkspaceWritePaths...)
	write = append(write, policy.HostWritePaths...)
	write = append(write, grants.WritePaths...)
	read := append([]string{}, write...)
	read = append(read, policy.WorkspaceReadPaths...)
	read = append(read, policy.HostReadPaths...)
	read = append(read, grants.ReadPaths...)
	read = append(read, executableDirs(args)...)
	read, write = canonicalUnique(read), canonicalUnique(write)

	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	// User data roots are closed; the grants below reopen exactly what this
	// launch may use. Later rules win, so the order matters.
	if userHome, err := os.UserHomeDir(); err == nil && strings.TrimSpace(userHome) != "" {
		b.WriteString(`(deny file-read* file-write* (subpath "` + sandboxQuote(canonical(userHome)) + "\"))\n")
	}
	for _, root := range []string{"/Users", "/Volumes", "/Network"} {
		b.WriteString(`(deny file-read* file-write* (subpath "` + root + "\"))\n")
	}
	b.WriteString("(allow file-read-metadata\n")
	for _, path := range ancestorPaths(append(append([]string(nil), read...), write...)) {
		b.WriteString(`  (literal "` + sandboxQuote(path) + "\")\n")
	}
	b.WriteString(")\n")
	if len(read) > 0 {
		b.WriteString("(allow file-read*\n")
		for _, path := range read {
			b.WriteString(`  (subpath "` + sandboxQuote(path) + "\")\n")
		}
		b.WriteString(")\n")
	}
	if len(write) > 0 || len(grants.WritePatterns) > 0 {
		b.WriteString("(allow file-read* file-write*\n")
		for _, path := range write {
			b.WriteString(`  (subpath "` + sandboxQuote(path) + "\")\n")
		}
		for _, pattern := range grants.WritePatterns {
			b.WriteString(`  (regex #"` + strings.ReplaceAll(pattern, `"`, `\"`) + "\")\n")
		}
		b.WriteString(")\n")
	}
	b.WriteString("(allow file-read* file-write* (subpath \"/dev\"))\n")
	// Blocked paths last, so they win over the folder grants above.
	for _, path := range canonicalUnique(policy.BlockedWritePaths) {
		b.WriteString(`(deny file-write* (subpath "` + sandboxQuote(path) + "\"))\n")
	}
	for _, path := range canonicalUnique(policy.BlockedPaths) {
		b.WriteString(`(deny file-read* file-write* (subpath "` + sandboxQuote(path) + "\"))\n")
	}
	return b.String()
}
