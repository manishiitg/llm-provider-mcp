package cursorcli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The per-session bridge token never lands in the workspace .cursor/mcp.json;
// the config names a private 0600 file the bridge reads instead.
func TestCursorMCPConfigKeepsTheBridgeTokenOutOfTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	old := cursorBridgeTokenDir
	cursorBridgeTokenDir = func() string { return dir }
	t.Cleanup(func() { cursorBridgeTokenDir = old })

	in := `{"mcpServers":{"api-bridge":{"command":"mcpbridge","env":{"MCP_API_URL":"http://h/s/a","MCP_API_TOKEN":"mcps1.secret.value","MCP_AUTH":"Authorization: Bearer mcps1.secret.value","MCP_TOOLS":"[]"}}}}`
	out, files, err := externalizeCursorBridgeTokens(in)
	if err != nil || len(files) != 1 {
		t.Fatalf("externalize: files=%v err=%v", files, err)
	}
	if strings.Contains(out, "mcps1.secret.value") {
		t.Fatalf("the token is still in the workspace config: %s", out)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	env := cfg.MCPServers["api-bridge"].Env
	if env["MCP_API_TOKEN_FILE"] != files[0] || env["MCP_API_URL"] != "http://h/s/a" || env["MCP_TOOLS"] != "[]" {
		t.Fatalf("config env: %v", env)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || string(raw) != "mcps1.secret.value" {
		t.Fatalf("token file: %q %v", raw, err)
	}
	if info, _ := os.Stat(files[0]); info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", info.Mode().Perm())
	}
	// No token: unchanged.
	plain := `{"mcpServers":{"x":{"command":"y","env":{"A":"b"}}}}`
	if got, files, _ := externalizeCursorBridgeTokens(plain); got != plain || len(files) != 0 {
		t.Fatalf("a config without a token must be unchanged: %s %v", got, files)
	}
}
