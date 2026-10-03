package claudecode

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/pathidentity"
)

// claudeLandlockArgs starts Claude Code confined to its folder when the
// policy asks for it. Beyond the policy's folders, a launch needs the files
// the adapter prepared outside them: every file named in argv (MCP config,
// settings, system prompt, status-line helper), the permission hooks, and the
// status-line file Claude writes for this session.
func claudeLandlockArgs(opts *llmtypes.CallOptions, args []string, workingDir, sessionName string) ([]string, func(), error) {
	if opts != nil && opts.CLISecurity.SeatbeltEnforced() {
		statusline := claudeStatuslinePath(sessionName)
		if f, err := os.OpenFile(statusline, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_ = f.Close()
		}
		grants := claudeSeatbeltGrants(args, workingDir)
		grants.WritePaths = append(grants.WritePaths, statusline)
		return clisandbox.SeatbeltArgs(opts.CLISecurity, args, workingDir, grants)
	}
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return args, func() {}, nil
	}
	read := claudeLandlockReads(args, workingDir)
	claudeAdoptResumedConversation(opts.CLISecurity, args, workingDir)
	if err := claudeMirrorLandlockReads(opts.CLISecurity, workingDir); err != nil {
		return nil, func() {}, err
	}
	statusline := claudeStatuslinePath(sessionName)
	if f, err := os.OpenFile(statusline, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
	return clisandbox.LandlockArgs(opts.CLISecurity, args, workingDir, read, []string{statusline})
}

// claudeAdoptResumedConversation copies the conversation a confined launch resumes into the
// private home when only the unconfined home has it. A chat started before its CLI was confined
// kept its conversation in the server's (or account's) ~/.claude; the confined launch reads the
// private home, so --resume failed with "No conversation found" and the pane exited at once
// (RTS 2026-09-30, a Crew chat after the lock was turned on). Only this folder's own
// conversation is copied, never the rest of that home; an existing private copy is kept.
func claudeAdoptResumedConversation(policy *llmtypes.CLISecurityPolicy, args []string, workingDir string) {
	resumeID := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--resume" {
			resumeID = strings.TrimSpace(args[i+1])
		}
	}
	if resumeID == "" || strings.ContainsAny(resumeID, `/\`) || policy == nil {
		return
	}
	source := strings.TrimSpace(policy.CredentialHome)
	if source == "" {
		source, _ = os.UserHomeDir()
	}
	if source == "" || filepath.Clean(source) == filepath.Clean(policy.PrivateHome) {
		return
	}
	for _, dir := range pathidentity.Candidates(workingDir) {
		slug := claudeTranscriptProjectSlug(dir)
		if slug == "" {
			continue
		}
		target := filepath.Join(policy.PrivateHome, ".claude", "projects", slug, resumeID+".jsonl")
		if _, err := os.Stat(target); err == nil {
			return
		}
		from := filepath.Join(source, ".claude", "projects", slug, resumeID+".jsonl")
		data, err := os.ReadFile(from)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return
		}
		_ = os.WriteFile(target, data, 0o600)
		// Subagent transcripts of this conversation, when Claude kept them beside it.
		_ = copyDirIfMissing(filepath.Join(source, ".claude", "projects", slug, resumeID), filepath.Join(filepath.Dir(target), resumeID))
		return
	}
}

func copyDirIfMissing(from, to string) error {
	if _, err := os.Stat(to); err == nil {
		return nil
	}
	info, err := os.Stat(from)
	if err != nil || !info.IsDir() {
		return err
	}
	return filepath.Walk(from, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dest := filepath.Join(to, rel)
		if fi.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o600)
	})
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
	dirs := []any{} // Claude rejects the whole settings file when this is null
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
	if opts != nil && opts.CLISecurity.SeatbeltEnforced() {
		return clisandbox.SeatbeltCmd(opts.CLISecurity, cmd, workingDir, claudeSeatbeltGrants(cmd.Args, workingDir))
	}
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return func() {}, nil
	}
	if err := claudeMirrorLandlockReads(opts.CLISecurity, workingDir); err != nil {
		return func() {}, err
	}
	return clisandbox.LandlockCmd(opts.CLISecurity, cmd, workingDir, claudeLandlockReads(cmd.Args, workingDir), nil)
}

// claudeSeatbeltGrants are what Claude Code needs on a Mac beyond its folders.
// It keeps its real home there: its login is a Keychain entry named after its
// config folder, so moving the folder would sign it out. It gets its own
// config (~/.claude and ~/.claude.json, which it replaces by write-and-rename),
// its cache, and read access to the Keychain files; nothing else in the home.
func claudeSeatbeltGrants(args []string, workingDir string) clisandbox.SeatbeltGrants {
	grants := clisandbox.SeatbeltGrants{ReadPaths: claudeLandlockReads(args, workingDir)}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return grants
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	configDir := filepath.Join(home, ".claude")
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		configDir = dir
	}
	grants.WritePaths = append(grants.WritePaths,
		configDir,
		filepath.Join(home, ".local", "state", "claude"),
		filepath.Join(home, "Library", "Caches", "claude-cli-nodejs"),
	)
	grants.ReadPaths = append(grants.ReadPaths,
		filepath.Join(home, ".local", "share", "claude"),
		filepath.Join(home, "Library", "Keychains"),
	)
	grants.WritePatterns = append(grants.WritePatterns, "^"+regexp.QuoteMeta(filepath.Join(home, ".claude.json"))+".*$")
	return grants
}
