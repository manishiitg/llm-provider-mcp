package cursorcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// cursor-agent only reads MCP servers from .cursor/mcp.json in the agent's
// working folder, which workflow readers, co-owners and backups can see. The
// bridge's per-session token must not be written there: each server env's
// MCP_API_TOKEN (and MCP_AUTH) is moved to a private 0600 file outside the
// workspace, and the config names it with MCP_API_TOKEN_FILE, which mcpbridge
// reads at startup (AgentWorks PLAT-362 D2).

// cursorBridgeTokenRoot holds one folder per backend process: the user's
// cache directory, never the workspace. Overridable in tests.
var cursorBridgeTokenRoot = func() string {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "agentworks-bridge-tokens")
}

// cursorBridgeTokenDir is this backend process's folder. Several backends can
// share a machine, so each keeps its own and only its own are swept.
func cursorBridgeTokenDir() string {
	return filepath.Join(cursorBridgeTokenRoot(), strconv.Itoa(os.Getpid()))
}

// SweepStaleBridgeTokenFiles removes the token folders of backend processes
// that are no longer running (their tokens stopped verifying when they
// exited) and returns how many it removed. Call it at backend startup, next
// to the tmux orphan sweep.
func SweepStaleBridgeTokenFiles() int {
	root := cursorBridgeTokenRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() || pid == os.Getpid() || processAlive(pid) {
			continue
		}
		if os.RemoveAll(filepath.Join(root, entry.Name())) == nil {
			removed++
		}
	}
	return removed
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// externalizeCursorBridgeTokens returns mcpJSON with every bridge token moved
// to a private file, and the paths of the files it wrote. A config it cannot
// parse, or one with no token, is returned unchanged.
func externalizeCursorBridgeTokens(mcpJSON string) (string, []string, error) {
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(mcpJSON), &decoded); err != nil {
		return mcpJSON, nil, nil
	}
	servers, _ := decoded["mcpServers"].(map[string]interface{})
	var written []string
	for _, raw := range servers {
		server, _ := raw.(map[string]interface{})
		env, _ := server["env"].(map[string]interface{})
		token, _ := env["MCP_API_TOKEN"].(string)
		if strings.TrimSpace(token) == "" {
			continue
		}
		path, err := writeCursorBridgeTokenFile(token)
		if err != nil {
			return "", written, err
		}
		written = append(written, path)
		delete(env, "MCP_API_TOKEN")
		delete(env, "MCP_AUTH")
		env["MCP_API_TOKEN_FILE"] = path
	}
	if len(written) == 0 {
		return mcpJSON, nil, nil
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		return "", written, err
	}
	return string(out), written, nil
}

// writeCursorBridgeTokenFile stores token in a 0600 file named by its hash, so
// repeated launches of one session reuse the same file.
func writeCursorBridgeTokenFile(token string) (string, error) {
	dir := cursorBridgeTokenDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("bridge token dir: %w", err)
	}
	_ = os.Chmod(cursorBridgeTokenRoot(), 0o700)
	_ = os.Chmod(dir, 0o700)
	sum := sha256.Sum256([]byte(token))
	path := filepath.Join(dir, "cursor-"+hex.EncodeToString(sum[:12]))
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("bridge token file: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}
