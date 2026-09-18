package codexcli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexAccountProfileAndSessionRootIgnoreAmbientHome(t *testing.T) {
	global, account := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", global)
	name, cleanup, err := writeCodexSessionMCPProfile(`{"mcpServers":{"bridge":{"url":"http://localhost:1/mcp"}}}`, false, nil, account)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(account, name+".config.toml")); err != nil {
		t.Fatal("MCP profile not written in selected Codex account")
	}
	if _, err := os.Stat(filepath.Join(global, name+".config.toml")); !os.IsNotExist(err) {
		t.Fatal("MCP profile leaked to ambient Codex home")
	}
	if codexSessionsRoot(account) != filepath.Join(account, "sessions") {
		t.Fatal("wrong account session root")
	}
}
