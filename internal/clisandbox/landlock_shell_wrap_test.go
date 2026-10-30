//go:build linux

package clisandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-872-84: a confined CLI keeps its login; the shells it starts get the same grants without the login file and
// without the login variables.
func TestConfinedLaunchWrapsShellsWithoutTheLogin(t *testing.T) {
	shellWrapEnabled = true
	t.Cleanup(func() { shellWrapEnabled = false })
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	if err := os.WriteFile(runner, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(dir, "account")
	login := filepath.Join(account, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(login), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(login, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := &llmtypes.CLISecurityPolicy{Mode: llmtypes.CLISecurityModeIsolated, Provider: "claude-code", LandlockRunner: runner,
		PrivateHome: filepath.Join(dir, "home"), CredentialHome: account}
	args, cleanup, err := LandlockArgs(policy, []string{"claude"}, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(args[2])
	if err != nil {
		t.Fatal(err)
	}
	var got landlockPolicy
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	src := canonical(login)
	if !slices.Contains(got.WritePaths, src) {
		t.Fatalf("the CLI lost its login: %s", raw)
	}
	if got.ShellWrap == nil {
		t.Fatalf("no shell wrap: %s", raw)
	}
	if slices.Contains(got.ShellWrap.Inner.WritePaths, src) || slices.Contains(got.ShellWrap.Inner.ReadPaths, src) {
		t.Fatalf("a shell may reach the login: %s", raw)
	}
	if !slices.Contains(got.ShellWrap.ScrubEnv, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("a shell keeps the login variable: %s", raw)
	}
}
