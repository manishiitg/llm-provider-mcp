package agycli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgyPrivateHomeTrustDoesNotChangeGlobalSettings(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	globalPath := filepath.Join(base, ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workingDir := filepath.Join(base, "workflow")
	home, cleanup, err := agyIsolatedHome(nil, workingDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		TrustedWorkspaces []string `json:"trustedWorkspaces"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.TrustedWorkspaces) != 1 || settings.TrustedWorkspaces[0] != workingDir {
		t.Fatalf("private trust = %v", settings.TrustedWorkspaces)
	}
	raw, err = os.ReadFile(globalPath)
	if err != nil || string(raw) != "{}\n" {
		t.Fatalf("global settings changed: %q, %v", raw, err)
	}
}
