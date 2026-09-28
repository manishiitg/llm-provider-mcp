package agycli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

const agyToolModeHookName = "agentworks-native-tool-mode"

// agyToolMode validates the optional product setting. An absent setting
// retains the adapter's legacy behavior for direct SDK callers.
func agyToolMode(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "", "mcp_only", "hybrid":
		return mode, nil
	default:
		return "", fmt.Errorf("agy native tools mode %q: want mcp_only or hybrid", raw)
	}
}

func agyToolModeFingerprint(mcpJSON, mode string) string {
	base := agyMountFingerprint(mcpJSON)
	if mode == "" {
		return base
	}
	return base + ":" + mode
}

func agyShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// AGY has no --tools allowlist. Its PreToolUse hook is the execution gate:
// all unknown native tools fail closed, including newly added AGY tools.
// Native writes, commands, browser actuation and subagents remain on the MCP
// bridge; hybrid admits only known read/search tools.
func agyToolModeHookCommand(python, mode string) string {
	readEnabled := "False"
	if mode == "hybrid" {
		readEnabled = "True"
	}
	program := `import json,sys
try:
    name=json.load(sys.stdin).get("toolCall",{}).get("name","")
except Exception:
    name=""
bridge=name=="call_mcp_tool" or name.startswith("mcp__")
read=name in {"view_file","list_dir","find_by_name","grep_search","search_web","read_url_content"}
allowed=bridge or (` + readEnabled + ` and read)
print(json.dumps({"decision":"allow" if allowed else "deny","reason":"Use the AgentWorks MCP bridge for this tool" if not allowed else ""}))`
	return agyShellQuote(python) + " -c " + agyShellQuote(program)
}

type agyWorkspaceHookHold struct {
	mode         string
	holders      int
	hadFile      bool
	original     []byte
	originalMode os.FileMode
	entry        interface{}
}

var agyWorkspaceHooks = struct {
	sync.Mutex
	active map[string]*agyWorkspaceHookHold
}{active: make(map[string]*agyWorkspaceHookHold)}

// agyHoldToolModeHook installs a workspace-level hook before CLI boot and
// removes only its own entry after the last turn/session releases it. AGY
// 1.2.12 demonstrably runs workspace hooks with --dangerously-skip-permissions;
// its global settings hooks were ignored in the same live probe.
func agyHoldToolModeHook(workingDir, mode string) (func(), error) {
	if mode == "" {
		return func() {}, nil
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return nil, fmt.Errorf("agy native tools mode requires python3 for the PreToolUse gate: %w", err)
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return nil, err
	}
	agyWorkspaceHooks.Lock()
	defer agyWorkspaceHooks.Unlock()
	if hold := agyWorkspaceHooks.active[workingDir]; hold != nil {
		if hold.mode != mode {
			return nil, fmt.Errorf("agy workspace %q already holds native tool mode %q", workingDir, hold.mode)
		}
		hold.holders++
		return agyToolModeReleaseFunc(workingDir), nil
	}
	path := filepath.Join(workingDir, ".agents", "hooks.json")
	original, err := os.ReadFile(path)
	hadFile := err == nil
	originalMode := os.FileMode(0o600)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if hadFile {
		if info, statErr := os.Stat(path); statErr == nil {
			originalMode = info.Mode().Perm()
		}
	}
	hooks := map[string]interface{}{}
	if hadFile {
		if err := json.Unmarshal(original, &hooks); err != nil {
			return nil, fmt.Errorf("parse agy workspace hooks: %w", err)
		}
		if hooks == nil {
			return nil, fmt.Errorf("agy workspace hooks must be an object")
		}
	}
	if previous, exists := hooks[agyToolModeHookName]; exists {
		if !agyIsManagedToolHook(previous) {
			return nil, fmt.Errorf("agy workspace hook %q already exists; refusing to replace it", agyToolModeHookName)
		}
		// Recover the managed entry left by a killed backend process.
		delete(hooks, agyToolModeHookName)
		hadFile = len(hooks) > 0
		if hadFile {
			original, _ = json.MarshalIndent(hooks, "", "  ")
			original = append(original, '\n')
		} else {
			original = nil
		}
	}
	entry := map[string]interface{}{
		"PreToolUse": []interface{}{map[string]interface{}{
			"matcher": "*",
			"hooks": []interface{}{map[string]interface{}{
				"type": "command", "command": agyToolModeHookCommand(python, mode), "timeout": 5,
			}},
		}},
	}
	hooks[agyToolModeHookName] = entry
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := agyWriteHooksFile(path, hooks); err != nil {
		return nil, err
	}
	agyWorkspaceHooks.active[workingDir] = &agyWorkspaceHookHold{mode: mode, holders: 1, hadFile: hadFile, original: original, originalMode: originalMode, entry: entry}
	return agyToolModeReleaseFunc(workingDir), nil
}

func agyIsManagedToolHook(value interface{}) bool {
	entry, ok := value.(map[string]interface{})
	if !ok {
		return false
	}
	pre, ok := entry["PreToolUse"].([]interface{})
	if !ok || len(pre) != 1 {
		return false
	}
	rule, ok := pre[0].(map[string]interface{})
	if !ok || rule["matcher"] != "*" {
		return false
	}
	handlers, ok := rule["hooks"].([]interface{})
	if !ok || len(handlers) != 1 {
		return false
	}
	handler, ok := handlers[0].(map[string]interface{})
	if !ok {
		return false
	}
	command, _ := handler["command"].(string)
	return handler["type"] == "command" && strings.Contains(command, "Use the AgentWorks MCP bridge for this tool")
}

func agyToolModeReleaseFunc(workingDir string) func() {
	var once sync.Once
	return func() { once.Do(func() { agyReleaseToolModeHook(workingDir) }) }
}

func agyReleaseToolModeHook(workingDir string) {
	agyWorkspaceHooks.Lock()
	defer agyWorkspaceHooks.Unlock()
	hold := agyWorkspaceHooks.active[workingDir]
	if hold == nil {
		return
	}
	hold.holders--
	if hold.holders > 0 {
		return
	}
	delete(agyWorkspaceHooks.active, workingDir)
	path := filepath.Join(workingDir, ".agents", "hooks.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	hooks := map[string]interface{}{}
	if json.Unmarshal(raw, &hooks) != nil || !agySameHookEntry(hooks[agyToolModeHookName], hold.entry) {
		return // A user changed the entry; leave their edit intact.
	}
	delete(hooks, agyToolModeHookName)
	if hold.hadFile {
		var originalHooks map[string]interface{}
		if json.Unmarshal(hold.original, &originalHooks) == nil && reflect.DeepEqual(hooks, originalHooks) {
			_ = os.WriteFile(path, hold.original, 0o600)
			_ = os.Chmod(path, hold.originalMode)
			return
		}
	}
	if len(hooks) == 0 && !hold.hadFile {
		_ = os.Remove(path)
		_ = os.Remove(filepath.Dir(path))
		return
	}
	_ = agyWriteHooksFile(path, hooks)
	if hold.hadFile {
		_ = os.Chmod(path, hold.originalMode)
	}
}

func agySameHookEntry(a, b interface{}) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(left) == string(right)
}

func agyWriteHooksFile(path string, hooks map[string]interface{}) error {
	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentworks-hooks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
