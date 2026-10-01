package codexcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedCodexAPIKeyLogin(t *testing.T) {
	home := t.TempDir()
	env := []string{"PATH=/bin", "CODEX_API_KEY=sk-test-123", "CODEX_HOME=" + home}
	if err := seedCodexAPIKeyLogin(env, "", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || !strings.Contains(string(raw), `"auth_mode": "apikey"`) || !strings.Contains(string(raw), "sk-test-123") {
		t.Fatalf("auth.json = %q, %v", raw, err)
	}
	info, _ := os.Stat(filepath.Join(home, "auth.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	// a changed key replaces the saved one; no key leaves the login alone
	_ = seedCodexAPIKeyLogin([]string{"CODEX_API_KEY=sk-new", "CODEX_HOME=" + home}, "", "")
	raw, _ = os.ReadFile(filepath.Join(home, "auth.json"))
	if !strings.Contains(string(raw), "sk-new") {
		t.Fatal("key not replaced")
	}
	// the key removed from the configuration: the saved key login goes too
	_ = seedCodexAPIKeyLogin([]string{"CODEX_HOME=" + home}, "", "")
	if _, err := os.Stat(filepath.Join(home, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("stale key login kept")
	}
	// a key the user pasted into Codex themselves (no marker) is never touched
	own := `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-theirs"}`
	_ = os.WriteFile(filepath.Join(home, "auth.json"), []byte(own), 0o600)
	_ = seedCodexAPIKeyLogin([]string{"CODEX_HOME=" + home}, "", "")
	if raw, _ := os.ReadFile(filepath.Join(home, "auth.json")); string(raw) != own {
		t.Fatal("the user's own key login changed")
	}
	// a browser login is never touched
	oauth := `{"auth_mode":"chatgpt","tokens":{"access_token":"x"}}`
	_ = os.WriteFile(filepath.Join(home, "auth.json"), []byte(oauth), 0o600)
	_ = seedCodexAPIKeyLogin([]string{"CODEX_HOME=" + home}, "", "")
	if raw, _ := os.ReadFile(filepath.Join(home, "auth.json")); string(raw) != oauth {
		t.Fatal("browser login changed")
	}
}
