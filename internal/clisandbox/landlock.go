package clisandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/internal/slotfs"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// landlockPolicy is the launcher's config format (workspace/security
// LandlockPolicy). The launcher reads it, deletes it, applies Landlock and
// execs the command, so the CLI is the pane's process as before.
type landlockPolicy struct {
	ReadPaths  []string `json:"read_paths"`
	WritePaths []string `json:"write_paths"`
	WorkDir    string   `json:"work_dir"`
	// ListPaths may be listed (folder names) but not read. A confined CLI
	// gets "/": Muse opens every folder from / down to its workspace at
	// start, and Landlock cannot grant one folder without the ones below it.
	ListPaths []string `json:"list_paths,omitempty"`
	// Agy native shells require PTYs; only grant an isolated devpts instance.
	PrivatePTS bool `json:"private_pts,omitempty"`
}

// credentialFile is one CLI login file: where it goes in the private home
// (Rel, which the sandbox's HOME/XDG/CODEX_HOME/CLAUDE_CONFIG_DIR layout also
// points at), and where the account keeps it: under the account's EnvKey
// folder when that is set (a server whose XDG_CONFIG_HOME is not ~/.config),
// else at Rel under the account home.
type credentialFile struct {
	Rel    string
	EnvKey string
	Sub    string
}

// credentialFiles are the only part of the account (or server) home a
// confined CLI can reach.
var credentialFiles = map[string][]credentialFile{
	"claude-code": {{Rel: ".claude/.credentials.json", EnvKey: "CLAUDE_CONFIG_DIR", Sub: ".credentials.json"}},
	"codex-cli":   {{Rel: ".codex/auth.json", EnvKey: "CODEX_HOME", Sub: "auth.json"}},
	"cursor-cli": {
		{Rel: ".config/cursor/auth.json", EnvKey: "XDG_CONFIG_HOME", Sub: "cursor/auth.json"},
		{Rel: ".cursor/cli-config.json"},
	},
	"muse-cli": {{Rel: ".config/muse/auth.json", EnvKey: "XDG_CONFIG_HOME", Sub: "muse/auth.json"}},
}

// credentialSource is where the account keeps one login file.
func credentialSource(file credentialFile, home string, env func(string) string) string {
	if file.EnvKey != "" {
		if root := strings.TrimSpace(env(file.EnvKey)); root != "" {
			return filepath.Join(root, file.Sub)
		}
	}
	return filepath.Join(home, file.Rel)
}

// LandlockArgs wraps a coding CLI's argv so it starts confined: by the host's
// Landlock launcher on Linux, or by Seatbelt on a Mac. It returns args
// unchanged when the policy does not confine the CLI on this host.
// runtimeReadPaths/runtimeWritePaths are files the adapter prepared for this
// launch (MCP config, settings, hooks, status files) that live outside the
// granted folders.
func LandlockArgs(policy *llmtypes.CLISecurityPolicy, args []string, workingDir string, runtimeReadPaths, runtimeWritePaths []string) ([]string, func(), error) {
	noop := func() {}
	if policy.SeatbeltEnforced() {
		return SeatbeltArgs(policy, args, workingDir, SeatbeltGrants{ReadPaths: runtimeReadPaths, WritePaths: runtimeWritePaths})
	}
	if !policy.LandlockEnforced() {
		return args, noop, nil
	}
	if len(args) == 0 {
		return nil, noop, errors.New("coding CLI launch command is empty")
	}
	runner := strings.TrimSpace(policy.LandlockRunner)
	if !filepath.IsAbs(runner) {
		return nil, noop, fmt.Errorf("%w: Landlock launcher path must be absolute", ErrUnsupported)
	}
	if _, err := os.Stat(runner); err != nil {
		return nil, noop, fmt.Errorf("%w: Landlock launcher unavailable: %w", ErrUnsupported, err)
	}
	home := canonical(policy.PrivateHome)
	// A launch the application declared for a slot the host does not confirm is refused here, before any file is
	// prepared: it must not run as the app account instead (PLAT-451).
	if err := slotfs.CheckLaunch(home); err != nil {
		return nil, noop, err
	}
	for _, dir := range []string{home, filepath.Join(home, ".config"), filepath.Join(home, ".local", "share"), filepath.Join(home, ".local", "state"), filepath.Join(home, ".cache"), filepath.Join(home, "tmp")} {
		if err := os.MkdirAll(dir, slotfs.Mode(home, 0o700)); err != nil {
			return nil, noop, fmt.Errorf("create private CLI home: %w", err)
		}
	}
	adoptResumedSession(policy, args)
	credentials, err := linkCredentialFiles(policy, home)
	if err != nil {
		return nil, noop, err
	}

	if slot, ok := slotfs.SlotOf(home); ok {
		// The CLI runs as the user's own account: give it the files the adapter prepared for this launch.
		for _, path := range runtimeReadPaths {
			if err := slotfs.GrantRuntime(slot, path, false); err != nil {
				return nil, noop, fmt.Errorf("give the user's account launch files: %w", err)
			}
		}
		for _, path := range runtimeWritePaths {
			// A per-launch folder the platform built (an isolated CLI home, a config root) is made the
			// slot group's to use, whatever mode its adapter gave it.
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				if own, owned := slotfs.SlotOf(path); !owned || own != slot {
					if err := slotfs.ShareTree(home, path); err != nil {
						return nil, noop, fmt.Errorf("give the user's account launch files: %w", err)
					}
				}
			}
			if err := slotfs.GrantRuntime(slot, path, true); err != nil {
				return nil, noop, fmt.Errorf("give the user's account launch files: %w", err)
			}
		}
		for _, path := range executableDirs(args) {
			if err := slotfs.GrantRuntime(slot, path, false); err != nil {
				return nil, noop, fmt.Errorf("give the user's account its CLI install: %w", err)
			}
		}
		// Folders other people share with this user (a shared Code, a Crew): the launch policy already
		// lists exactly what this user may reach, so give this slot the same access at the file level.
		// The user's own folders belong to the slot already.
		for _, shared := range []struct {
			paths []string
			write bool
		}{{policy.WorkspaceReadPaths, false}, {policy.WorkspaceWritePaths, true}} {
			for _, path := range shared.paths {
				if own, ok := slotfs.SlotOf(path); ok && own == slot {
					continue
				}
				if err := slotfs.GrantRuntime(slot, path, shared.write); err != nil {
					return nil, noop, fmt.Errorf("give the user's account a shared folder: %w", err)
				}
			}
		}
	}

	read := append([]string{}, policy.WorkspaceReadPaths...)
	read = append(read, policy.HostReadPaths...)
	read = append(read, runtimeReadPaths...)
	read = append(read, executableDirs(args)...)
	write := []string{workingDir, home}
	// Only Cursor gets the shared /tmp: it keeps sockets at fixed /tmp paths
	// (cursor-askpass-*.sock, and .cursor/<project> when its home path is too long for a socket).
	// Every other CLI uses the private TMPDIR from CLIHomeEnvironment and starts without it (Muse,
	// Pi and Agy verified under the real launcher; Claude and Codex ran confined without it), so
	// one CLI's temp files are not readable by another. See docs/DECISIONS.md in the builder.
	if strings.EqualFold(strings.TrimSpace(policy.Provider), "cursor-cli") {
		write = append(write, "/tmp")
	}
	write = append(write, policy.WorkspaceWritePaths...)
	write = append(write, policy.HostWritePaths...)
	write = append(write, runtimeWritePaths...)
	write = append(write, credentials...)
	// Blocked paths inside a grant (planning/, the raw database, AGENTS.md):
	// Landlock cannot take them back, so the grants are split around them.
	// The CLI's own working folder is never split: for Code it is the project,
	// and splitting it would stop the CLI creating files there. Its blocked
	// entries are the CLI's managed instruction files, which the bridge guards.
	cwd := canonical(workingDir)
	read, write = splitAroundBlocked(canonicalUnique(read), canonicalUnique(write),
		outside(canonicalUnique(policy.BlockedPaths), cwd), outside(canonicalUnique(policy.BlockedWritePaths), cwd))
	config := landlockPolicy{
		ReadPaths:  existing(canonicalUnique(read)),
		WritePaths: existing(canonicalUnique(write)),
		WorkDir:    canonical(workingDir),
		ListPaths:  []string{"/"},
		PrivatePTS: strings.TrimSpace(policy.Provider) == "agy-cli",
	}
	file, err := slotfs.CreateTemp(home, "agentworks-cli-landlock-*.json")
	if err != nil {
		return nil, noop, fmt.Errorf("create CLI Landlock policy: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	err = file.Chmod(slotfs.Mode(home, 0o600))
	if err == nil {
		err = json.NewEncoder(file).Encode(config)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return nil, noop, fmt.Errorf("write CLI Landlock policy: %w", err)
	}
	// The launcher execs argv[0] without a PATH search; /usr/bin/env resolves
	// the CLI (and `env VAR=… cli` forms) inside the sandbox.
	wrapped := []string{runner, "--config", path, "--", "/usr/bin/env"}
	if slotfs.IsSlotLaunch(home) {
		// The folders belong to the platform account with the slot's group, so git refuses them as
		// "dubious ownership" (and a CLI such as Muse fails to find its project root). The folders a
		// confined CLI can reach are only the ones it was granted.
		// safe.directory=* (the folders are platform-owned), and the two repository settings that run code
		// unasked, hooks and fsmonitor, switched off: a shared folder's author cannot get code run as the
		// user through git (same set as the shell tool's; docs/DECISIONS.md in the builder).
		wrapped = append(wrapped,
			"GIT_CONFIG_COUNT=3",
			"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*",
			"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null",
			"GIT_CONFIG_KEY_2=core.fsmonitor", "GIT_CONFIG_VALUE_2=false")
	}
	wrapped = append(wrapped, args...)
	return wrapped, cleanup, nil
}

// linkCredentialFiles gives the private home a link to each of the CLI's
// login files in CredentialHome, never a copy: logins rotate their refresh
// tokens, so every session on one account must share the one file. If the
// CLI replaced the link with its own file (write-and-rename), that newer
// file is moved back to the account first so no session loses a refresh.
// It returns the credential files to grant.
func linkCredentialFiles(policy *llmtypes.CLISecurityPolicy, home string) ([]string, error) {
	source := canonical(policy.CredentialHome)
	if source == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			source = canonical(userHome)
		}
	}
	if source == "" || source == home {
		return nil, nil
	}
	var granted []string
	// The server account's logins are where this process's environment says;
	// an account's are where its own path environment says.
	env := os.Getenv
	if strings.TrimSpace(policy.CredentialHome) != "" {
		env = func(key string) string { return policy.CredentialEnv[key] }
	}
	for _, file := range credentialFiles[strings.TrimSpace(policy.Provider)] {
		src := credentialSource(file, source, env)
		dst := filepath.Join(home, file.Rel)
		if info, err := os.Lstat(dst); err == nil && info.Mode().IsRegular() {
			if srcInfo, err := os.Stat(src); err != nil || info.ModTime().After(srcInfo.ModTime()) {
				if err := copyFileAtomic(dst, src, home); err != nil {
					return nil, fmt.Errorf("return refreshed CLI login to its account: %w", err)
				}
			}
		}
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if target, err := os.Readlink(dst); err != nil || target != src {
			if err := os.MkdirAll(filepath.Dir(dst), slotfs.Mode(home, 0o700)); err != nil {
				return nil, err
			}
			_ = os.Remove(dst)
			if err := os.Symlink(src, dst); err != nil {
				return nil, fmt.Errorf("link CLI login into private home: %w", err)
			}
		}
		if slot, ok := slotfs.SlotOf(home); ok {
			if err := slotfs.GrantAccess(slot, src); err != nil {
				return nil, fmt.Errorf("give the user's account access to the CLI login: %w", err)
			}
		}
		granted = append(granted, src)
	}
	return granted, nil
}

func copyFileAtomic(from, to, hint string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), slotfs.Mode(hint, 0o700)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(to), ".agentworks-login-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(slotfs.Mode(hint, 0o600)); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), to)
}

// executableDirs grants the CLI's install folder read-only when it lives
// outside the launcher's system baseline (e.g. ~/.local/bin/cursor-agent ->
// ~/.local/share/cursor-agent/versions/…): the link's folder and the real
// binary's folder tree.
func executableDirs(args []string) []string {
	var dirs []string
	// A launch may start with a shell prelude, `sh -c <script> <name> <arg> <real command...>` (Muse sweeps its probe folders first): the CLI is
	// the command after it, not the shell, whose folder the baseline already grants.
	if len(args) > 5 && args[0] == "sh" && args[1] == "-c" {
		args = args[5:]
	}
	for _, arg := range args {
		if arg == "env" || strings.Contains(arg, "=") {
			continue
		}
		resolved, err := exec.LookPath(arg)
		if err != nil {
			return dirs
		}
		dirs = append(dirs, filepath.Dir(resolved))
		if real, err := filepath.EvalSymlinks(resolved); err == nil {
			dirs = append(dirs, installRoot(real))
		}
		return dirs
	}
	return dirs
}

// installRoot widens a versioned binary path to its package folder so the
// CLI can load files next to it (node_modules, versions/<v>/…).
func installRoot(binary string) string {
	dir := filepath.Dir(binary)
	for current := dir; current != "/" && current != "."; current = filepath.Dir(current) {
		base := filepath.Base(current)
		if base == "versions" || base == "node_modules" {
			return filepath.Dir(current)
		}
	}
	return dir
}

func existing(paths []string) []string {
	out := paths[:0]
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

// ArgFilePaths returns the absolute paths of existing files named in argv,
// either as an argument or as the value of a --flag=value argument. A launch
// reads these (configs, prompts, helpers) and they may live outside the
// granted folders.
func ArgFilePaths(args []string) []string {
	var paths []string
	for _, arg := range args {
		if _, value, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(arg, "-") {
			arg = value
		}
		arg = strings.Trim(arg, `'"`)
		if !filepath.IsAbs(arg) {
			continue
		}
		if info, err := os.Stat(arg); err == nil && info.Mode().IsRegular() {
			paths = append(paths, arg)
		}
	}
	return paths
}

// MCPCommandPaths returns the MCP server programs named in MCP config files
// (`{"mcpServers":{"x":{"command":"/abs/bin", "args":[…]}}}`), plus any
// absolute file arguments, so a confined CLI can still start its MCP
// servers — in production the AgentWorks bridge.
func MCPCommandPaths(configFiles []string) []string {
	var paths []string
	for _, file := range configFiles {
		data, err := os.ReadFile(file)
		if err != nil || len(data) > 1<<20 {
			continue
		}
		var config struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal(data, &config) != nil {
			continue
		}
		for _, server := range config.MCPServers {
			if command := strings.TrimSpace(server.Command); command != "" {
				if !filepath.IsAbs(command) {
					if resolved, err := exec.LookPath(command); err == nil {
						command = resolved
					}
				}
				if filepath.IsAbs(command) {
					paths = append(paths, command)
					if real, err := filepath.EvalSymlinks(command); err == nil {
						paths = append(paths, real)
					}
				}
			}
			paths = append(paths, ArgFilePaths(server.Args)...)
		}
	}
	return paths
}

// JSONFilePaths returns absolute paths of existing files mentioned anywhere
// in JSON config files, including inside command strings such as
// `sh /tmp/helper.sh` or `python3 /tmp/hooks/x.py`.
func JSONFilePaths(configFiles []string) []string {
	var paths []string
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for _, item := range v {
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		case string:
			paths = append(paths, ArgFilePaths(strings.Fields(v))...)
		}
	}
	for _, file := range configFiles {
		data, err := os.ReadFile(file)
		if err != nil || len(data) > 1<<20 {
			continue
		}
		var root any
		if json.Unmarshal(data, &root) == nil {
			walk(root)
		}
	}
	return paths
}

// LandlockCmd confines a structured-transport launch (exec.Cmd) the same way
// LandlockArgs confines a tmux launch. cmd.Env already carries the private
// home paths (MergeCodingAgentSecretEnvironment). It is a no-op when the
// policy does not confine the CLI on this host.
func LandlockCmd(policy *llmtypes.CLISecurityPolicy, cmd *exec.Cmd, workingDir string, runtimeReadPaths, runtimeWritePaths []string) (func(), error) {
	if policy.SeatbeltEnforced() {
		return SeatbeltCmd(policy, cmd, workingDir, SeatbeltGrants{ReadPaths: runtimeReadPaths, WritePaths: runtimeWritePaths})
	}
	if !policy.LandlockEnforced() {
		return func() {}, nil
	}
	if cmd == nil || len(cmd.Args) == 0 {
		return func() {}, errors.New("coding CLI command is empty")
	}
	if workingDir == "" {
		workingDir = cmd.Dir
	}
	args := append([]string{cmd.Path}, cmd.Args[1:]...)
	wrapped, cleanup, err := LandlockArgs(policy, args, workingDir, append(runtimeReadPaths, ArgFilePaths(args)...), runtimeWritePaths)
	if err != nil {
		return func() {}, err
	}
	cmd.Path = wrapped[0]
	cmd.Args = wrapped
	if slotfs.IsSlotLaunch(policy.PrivateHome) {
		// The user's own Linux account runs the CLI; the request travels in a file so the CLI keeps
		// its standard input and output.
		unwrap, wrapErr := slotfs.WrapCmd(cmd, policy.PrivateHome, cmd.Env)
		if wrapErr != nil {
			cleanup()
			return func() {}, wrapErr
		}
		inner := cleanup
		return func() { unwrap(); inner() }, nil
	}
	return cleanup, nil
}

// ConfigDirFiles lists the JSON files directly inside dir (e.g. a working
// directory's .cursor/ or .claude/), for grants derived from project configs.
func ConfigDirFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files
}
