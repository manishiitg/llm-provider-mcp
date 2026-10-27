package cursorcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-856-145: generated policy/instructions must remain private, with the
// account's pre-existing deny rules intact and no writes to the person's cwd.
func TestPrivateCursorRuntimePreservesProjectAndAccountPolicy(t *testing.T) {
	project, private, home := t.TempDir(), t.TempDir(), t.TempDir()
	originals := map[string]string{"AGENTS.md": "project instructions", ".cursor/cli.json": `{"permissions":{"allow":["Shell(*)"]}}`, ".cursor/rules/own.mdc": "project rule"}
	for name, data := range originals {
		p := filepath.Join(project, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	account := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(account, 0700); err != nil {
		t.Fatal(err)
	}
	originalConfig := `{"version":1,"permissions":{"deny":["Shell(private-command)"],"allow":[]}}`
	if err := os.WriteFile(filepath.Join(account, "cli-config.json"), []byte(originalConfig), 0600); err != nil {
		t.Fatal(err)
	}
	opts := &llmtypes.CallOptions{}
	llmtypes.WithCLIRuntimeDir(private)(opts)
	WithDenyBuiltinTools(true)(opts)
	cleanup, err := prepareCursorProjectFiles(llmtypes.CLIArtifactDirectory(opts, project), "private system instruction", opts, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	configDir, err := prepareCursorPrivateRuntime(opts, []string{"HOME=" + home})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(configDir, "cli-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"Shell(private-command)"}, cursorNativeDenyPermissions(false, true)...)
	if !reflect.DeepEqual(cfg.Permissions.Deny, want) {
		t.Fatalf("private policy lost deny rules: %v", cfg.Permissions.Deny)
	}
	for name, data := range originals {
		got, err := os.ReadFile(filepath.Join(project, name))
		if err != nil || string(got) != data {
			t.Fatalf("project file changed: %s", name)
		}
	}
	got, err := os.ReadFile(filepath.Join(account, "cli-config.json"))
	if err != nil || string(got) != originalConfig {
		t.Fatal("changed account configuration")
	}
	if _, err := os.Stat(filepath.Join(private, ".cursor-plugin", "plugin.json")); err != nil {
		t.Fatal(err)
	}
	// PLAT-856-145 review: changing selected HOME must not inherit credentials,
	// history, or XDG configuration from the previous/ambient account.
	if _, err := os.Stat(filepath.Join(private, ".git")); !os.IsNotExist(err) {
		t.Fatal("private runtime was git-initialized")
	}
	if err := os.WriteFile(filepath.Join(account, "auth.json"), []byte("fixture-A"), 0600); err != nil {
		t.Fatal(err)
	}
	configA, err := prepareCursorPrivateRuntime(opts, []string{"HOME=" + home, "XDG_CONFIG_HOME=" + t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(configA, "auth.json")); err != nil || string(raw) != "fixture-A" {
		t.Fatal("selected account overridden", err)
	}
	if err := os.MkdirAll(filepath.Join(configA, "acp-sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configA, "acp-sessions", "record-A"), []byte("fixture history"), 0600); err != nil {
		t.Fatal(err)
	}
	homeB := t.TempDir()
	configB, err := prepareCursorPrivateRuntime(opts, []string{"HOME=" + homeB, "XDG_CONFIG_HOME=" + filepath.Dir(account)})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "acp-sessions/record-A"} {
		if _, err := os.Stat(filepath.Join(configB, name)); !os.IsNotExist(err) {
			t.Fatalf("old account visible in B: %s %v", name, err)
		}
	}
	if current, err := filepath.EvalSymlinks(filepath.Join(private, "cursor-config")); err != nil || current != configB {
		t.Fatal("transcript account not updated", err)
	}
	if _, err := os.Stat(filepath.Join(configA, "acp-sessions", "record-A")); err != nil {
		t.Fatal("old history deleted", err)
	}
	if err := os.Remove(filepath.Join(private, ".cursor", "cli.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCursorPrivateRuntime(opts, []string{"HOME=" + homeB, "CURSOR_CONFIG_DIR=" + account}); err != nil {
		t.Fatalf("nonbridge runtime without generated permissions: %v", err)
	}

}
