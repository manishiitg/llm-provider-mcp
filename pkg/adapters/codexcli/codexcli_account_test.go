package codexcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
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

// PLAT-872-19: account-specific profiles must not read another login's config.
func TestCodexAppServerConfigUsesSelectedAccount(t *testing.T) {
	ambient, account := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", ambient)
	for home, marker := range map[string]string{ambient: "ambient", account: "selected"} {
		if err := os.WriteFile(filepath.Join(home, "chosen.config.toml"), []byte("model = \""+marker+"\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts := &llmtypes.CallOptions{}
	llmtypes.WithProviderAccountEnvironment(map[string]string{"CODEX_HOME": account})(opts)
	WithConfigProfile("chosen")(opts)
	config, err := codexAppThreadConfig(opts, "never")
	if err != nil {
		t.Fatal(err)
	}
	if config["model"] != "selected" {
		t.Fatal("app-server read the ambient account profile")
	}
}
