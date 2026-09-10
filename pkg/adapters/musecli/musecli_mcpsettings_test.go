package musecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// redirectMuseConfigHome points XDG_CONFIG_HOME at a temp dir for the test.
func redirectMuseConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// Fail loudly if the resolved path escapes the temp dir.
	path, err := museSettingsPath()
	if err != nil {
		t.Fatalf("settings path: %v", err)
	}
	want := filepath.Join(dir, "muse", "settings.json")
	if path != want {
		t.Fatalf("settings path = %q, want %q (config home redirect not honored)", path, want)
	}
	return path
}

func readMCPServerNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	out := map[string]bool{}
	for name := range settings.Servers {
		out[name] = true
	}
	return out
}

func TestMuseApplyMCPConfigMergesAndRestores(t *testing.T) {
	path := redirectMuseConfigHome(t)

	// Pre-existing settings with one server and an unrelated key.
	before := `{"schema_version": 1, "theme": "dark", "mcpServers": {"keep": {"url": "http://127.0.0.1:9/x"}}}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	restore, err := museApplyMCPConfig(`{"mcpServers": {"api-bridge": {"url": "http://127.0.0.1:9/bridge"}, "keep": {"url": "http://127.0.0.1:9/override"}}}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if restore == nil {
		t.Fatal("expected a restore func for a non-empty config")
	}
	names := readMCPServerNames(t, path)
	if !names["api-bridge"] || !names["keep"] {
		t.Fatalf("merged servers = %v, want api-bridge + keep", names)
	}

	restore()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after restore: %v", err)
	}
	var after struct {
		Theme   string                     `json:"theme"`
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatalf("parse after restore: %v", err)
	}
	if after.Theme != "dark" {
		t.Fatalf("unrelated key lost: %s", raw)
	}
	if len(after.Servers) != 1 {
		t.Fatalf("servers after restore = %v, want only keep", after.Servers)
	}
	var kept struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(after.Servers["keep"], &kept); err != nil || kept.URL != "http://127.0.0.1:9/x" {
		t.Fatalf("pre-existing server not reinstated byte-fair: %s", after.Servers["keep"])
	}
}

func TestMuseApplyMCPConfigCreatesAndRemovesFile(t *testing.T) {
	path := redirectMuseConfigHome(t)

	restore, err := museApplyMCPConfig(`{"mcpServers": {"api-bridge": {"url": "http://127.0.0.1:9/bridge"}}}`)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if names := readMCPServerNames(t, path); !names["api-bridge"] {
		t.Fatalf("servers = %v, want api-bridge", names)
	}
	restore()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created settings.json not removed by restore")
	}
}

func TestMuseApplyMCPConfigRejectsBadInput(t *testing.T) {
	redirectMuseConfigHome(t)
	for _, bad := range []string{
		`not json`,
		`{"mcpServers": {"": {"url": "http://x"}}}`,
	} {
		if _, err := museApplyMCPConfig(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
	// Empty server map: no-op, no restore, no file.
	restore, err := museApplyMCPConfig(`{"mcpServers": {}}`)
	if err != nil || restore != nil {
		t.Fatalf("empty config: restore=%v err=%v, want nil,nil", restore != nil, err)
	}
}

func TestMuseWithMCPConfigOptionRoundTrip(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers": {"a": {}}}`)(opts)
	if got := museMCPConfigFromOptions(opts); got != `{"mcpServers": {"a": {}}}` {
		t.Fatalf("option round trip = %q", got)
	}
	if got := museMCPConfigFromOptions(nil); got != "" {
		t.Fatalf("nil options = %q, want empty", got)
	}
}
