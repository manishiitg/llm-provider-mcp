package clisandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// A login kept under an XDG_CONFIG_HOME other than ~/.config (the RTS server
// sets one) is linked into the private home; it used to be looked for under
// ~/.config only, so a confined Cursor on the server account had no login.
func TestLinkCredentialFilesFollowsTheAccountConfigHome(t *testing.T) {
	root := t.TempDir()
	accountHome := filepath.Join(root, "account")
	configHome := filepath.Join(root, "xdg-config")
	login := filepath.Join(configHome, "cursor", "auth.json")
	if err := os.MkdirAll(filepath.Dir(login), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(login, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "private")
	policy := &llmtypes.CLISecurityPolicy{
		Provider:       "cursor-cli",
		CredentialHome: accountHome,
		CredentialEnv:  map[string]string{"HOME": accountHome, "XDG_CONFIG_HOME": configHome},
	}
	granted, err := linkCredentialFiles(policy, private)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 1 || granted[0] != login {
		t.Fatalf("granted %v, want only %s", granted, login)
	}
	if target, err := os.Readlink(filepath.Join(private, ".config", "cursor", "auth.json")); err != nil || target != login {
		t.Fatalf("private login link = %q, %v", target, err)
	}
}

// The server account's logins are found through this process's environment.
func TestLinkCredentialFilesServerAccountUsesProcessEnvironment(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "xdg-config")
	login := filepath.Join(configHome, "muse", "auth.json")
	if err := os.MkdirAll(filepath.Dir(login), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(login, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "server-home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	granted, err := linkCredentialFiles(&llmtypes.CLISecurityPolicy{Provider: "muse-cli"}, filepath.Join(root, "private"))
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 1 || granted[0] != login {
		t.Fatalf("granted %v, want %s", granted, login)
	}
}
