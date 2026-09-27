package cursorcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cursor-agent only reads MCP servers from .cursor/mcp.json in the agent's
// working folder, which workflow readers, co-owners and backups can see. The
// bridge's per-session token must not be written there: each server env's
// MCP_API_TOKEN (and MCP_AUTH) is moved to a private 0600 file outside the
// workspace, and the config names it with MCP_API_TOKEN_FILE, which mcpbridge
// reads at startup (AgentWorks PLAT-362 D2).

// cursorBridgeTokenDir is where token files go: the user's cache directory,
// never the workspace. Overridable in tests.
var cursorBridgeTokenDir = func() string {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "agentworks-bridge-tokens")
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
	_ = os.Chmod(dir, 0o700)
	sum := sha256.Sum256([]byte(token))
	path := filepath.Join(dir, "cursor-"+hex.EncodeToString(sum[:12]))
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("bridge token file: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}
