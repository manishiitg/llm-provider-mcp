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
}
