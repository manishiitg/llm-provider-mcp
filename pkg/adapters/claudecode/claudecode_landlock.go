package claudecode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// claudeLandlockArgs starts Claude Code confined to its folder when the
// policy asks for it. Beyond the policy's folders, a launch needs the files
// the adapter prepared outside them: every file named in argv (MCP config,
// settings, system prompt, status-line helper), the permission hooks, and the
// status-line file Claude writes for this session.
func claudeLandlockArgs(opts *llmtypes.CallOptions, args []string, workingDir, sessionName string) ([]string, func(), error) {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return args, func() {}, nil
	}
	read := claudeLandlockReads(args, workingDir)
	if err := claudeMirrorLandlockReads(opts.CLISecurity, workingDir); err != nil {
		return nil, func() {}, err
	}
	statusline := claudeStatuslinePath(sessionName)
	if f, err := os.OpenFile(statusline, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
	return clisandbox.LandlockArgs(opts.CLISecurity, args, workingDir, read, []string{statusline})
}

// claudeMirrorLandlockReads makes Claude's own read rule match the Landlock
// grants, in the private home's user settings (Claude reads this rule from
// user settings only). A fresh home has no saved answer, so Claude would
// otherwise stop the turn to ask "Allow this read outside the working
// directories?". Blocking outside reads, with the granted folders as
// additional directories, answers it: granted folders read normally, anything
// else is refused by Claude, and the kernel refuses it underneath regardless.
func claudeMirrorLandlockReads(policy *llmtypes.CLISecurityPolicy, workingDir string) error {
	path := filepath.Join(policy.PrivateHome, ".claude", "settings.json")
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	permissions, _ := settings["permissions"].(map[string]any)
	if permissions == nil {
		permissions = map[string]any{}
	}
	var dirs []any
	seen := map[string]bool{workingDir: true}
	for _, list := range [][]string{policy.WorkspaceReadPaths, policy.WorkspaceWritePaths, policy.HostReadPaths, policy.HostWritePaths} {
		for _, dir := range list {
			if info, err := os.Stat(dir); err == nil && info.IsDir() && !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
	}
	permissions["blockReadsOutsideWorkingDirectories"] = true
	permissions["additionalDirectories"] = dirs
	settings["permissions"] = permissions
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// claudeLandlockReads are the files a confined Claude launch needs outside
// its granted folders: files named in argv, the MCP servers and the
// status-line helper and hook scripts those configs name, and the hooks.
func claudeLandlockReads(args []string, workingDir string) []string {
	read := clisandbox.ArgFilePaths(args)
	configs := append(append([]string(nil), read...), filepath.Join(workingDir, ".mcp.json"))
	read = append(read, clisandbox.MCPCommandPaths(configs)...)
	read = append(read, clisandbox.JSONFilePaths(configs)...)
	return append(read, filepath.Join(os.TempDir(), "claude-code-hooks"))
}

// claudeLandlockCmd confines a structured (stream-json) Claude launch.
func claudeLandlockCmd(opts *llmtypes.CallOptions, cmd *exec.Cmd, workingDir string) (func(), error) {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return func() {}, nil
	}
	if err := claudeMirrorLandlockReads(opts.CLISecurity, workingDir); err != nil {
		return func() {}, err
	}
	return clisandbox.LandlockCmd(opts.CLISecurity, cmd, workingDir, claudeLandlockReads(cmd.Args, workingDir), nil)
}
