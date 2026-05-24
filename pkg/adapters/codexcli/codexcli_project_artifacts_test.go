package codexcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderCodexMCPServersTOMLBasicShape locks in the exact TOML
// emitted for a typical MCP server map. Output must be deterministic
// (keys sorted) so byte-compare assertions in cleanup tests are stable.
func TestRenderCodexMCPServersTOMLBasicShape(t *testing.T) {
	startup := 12
	enabled := true
	servers := map[string]codexMCPServerSpec{
		"api-bridge": {
			Command:           "/usr/local/bin/mcpbridge",
			Args:              []string{"--port", "9000"},
			Env:               map[string]string{"MCP_API_URL": "http://localhost:9000", "TOKEN": "abc"},
			EnvVars:           []string{"LOCAL_TOKEN"},
			StartupTimeoutSec: &startup,
			Enabled:           &enabled,
		},
	}
	got := renderCodexMCPServersTOML(servers)
	expected := []string{
		"[mcp_servers.api-bridge]",
		`command = "/usr/local/bin/mcpbridge"`,
		`args = ["--port", "9000"]`,
		`env_vars = ["LOCAL_TOKEN"]`,
		"startup_timeout_sec = 12",
		"enabled = true",
		"[mcp_servers.api-bridge.env]",
		`MCP_API_URL = "http://localhost:9000"`,
		`TOKEN = "abc"`,
	}
	for _, want := range expected {
		if !strings.Contains(got, want) {
			t.Errorf("expected TOML to contain %q; got:\n%s", want, got)
		}
	}

	// Env keys must appear in sorted order so the byte output is
	// stable across goroutine schedules. MCP_API_URL < TOKEN
	// lexicographically. Use the assignment suffix as the search key
	// to avoid matching "TOKEN" inside the unrelated "LOCAL_TOKEN"
	// entry in env_vars above.
	mcpIdx := strings.Index(got, `MCP_API_URL = `)
	tokenIdx := strings.Index(got, `TOKEN = "abc"`)
	if mcpIdx == -1 || tokenIdx == -1 || mcpIdx > tokenIdx {
		t.Errorf("env keys must be sorted lexicographically (MCP_API_URL before TOKEN); mcpIdx=%d tokenIdx=%d\nfull TOML:\n%s", mcpIdx, tokenIdx, got)
	}
}

// TestTomlQuoteKeyBareVsQuoted covers the bare-key vs quoted-key
// decision boundary. Server names like "api-bridge" stay bare per
// TOML 1.0; names with dots, spaces, or other punctuation force a
// quoted-key form.
func TestTomlQuoteKeyBareVsQuoted(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"api-bridge", "api-bridge"},
		{"my_server", "my_server"},
		{"Server123", "Server123"},
		{"with.dot", `"with.dot"`},
		{"has space", `"has space"`},
		{"", `""`},
	}
	for _, c := range cases {
		if got := tomlQuoteKey(c.in); got != c.want {
			t.Errorf("tomlQuoteKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWriteCodexProjectMCPConfigTOMLRestoresOperatorContent guards the
// promise that pre-existing operator-owned .codex/config.toml is
// restored byte-for-byte at cleanup.
func TestWriteCodexProjectMCPConfigTOMLRestoresOperatorContent(t *testing.T) {
	tmp := t.TempDir()
	codexDir := filepath.Join(tmp, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("seed .codex dir: %v", err)
	}
	operatorContent := []byte("# operator's hand-tuned codex config\n[profile.work]\nmodel = \"o4\"\n")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configPath, operatorContent, 0o600); err != nil {
		t.Fatalf("seed pre-existing config.toml: %v", err)
	}

	mcpJSON := `{"api-bridge":{"command":"/usr/local/bin/mcpbridge"}}`
	cleanup, err := writeCodexProjectMCPConfigTOML(tmp, mcpJSON)
	if err != nil {
		t.Fatalf("writeCodexProjectMCPConfigTOML: %v", err)
	}

	mid, _ := os.ReadFile(configPath)
	if strings.Contains(string(mid), "profile.work") {
		t.Error("mid-session, the operator's prior config.toml content should NOT be visible — our MCP config is installed")
	}
	if !strings.Contains(string(mid), "[mcp_servers.api-bridge]") {
		t.Errorf("mid-session, the MCP server block must be present; got:\n%s", mid)
	}

	cleanup()
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("after cleanup, config.toml should exist (restored): %v", err)
	}
	if string(restored) != string(operatorContent) {
		t.Errorf("cleanup must restore pre-existing config.toml byte-for-byte\n  want: %q\n  got:  %q", operatorContent, restored)
	}
}

// TestWriteCodexProjectDenyBuiltinHooksLifecycleNoPriorContent covers
// the fresh-workspace case: no .codex/ exists, hooks.json +
// deny-builtin.sh both get created, cleanup removes them (and the
// directories we created when empty).
func TestWriteCodexProjectDenyBuiltinHooksLifecycleNoPriorContent(t *testing.T) {
	tmp := t.TempDir()
	cleanup, err := writeCodexProjectDenyBuiltinHooks(tmp)
	if err != nil {
		t.Fatalf("writeCodexProjectDenyBuiltinHooks: %v", err)
	}

	hooksPath := filepath.Join(tmp, ".codex", "hooks.json")
	scriptPath := filepath.Join(tmp, ".codex", "hooks", "deny-builtin.sh")

	hooksBody, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("hooks.json must exist after write: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(hooksBody, &parsed); err != nil {
		t.Errorf("hooks.json must be valid JSON; got %v\nbody:\n%s", err, hooksBody)
	}
	if !strings.Contains(string(hooksBody), `"PreToolUse"`) {
		t.Errorf("hooks.json must declare PreToolUse event; got:\n%s", hooksBody)
	}
	if !strings.Contains(string(hooksBody), `"^(Bash|apply_patch)$"`) {
		t.Errorf("hooks.json matcher must cover Bash + apply_patch (codex's built-in tool names); got:\n%s", hooksBody)
	}

	scriptBody, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("deny-builtin.sh must exist: %v", err)
	}
	if !strings.HasPrefix(string(scriptBody), "#!/bin/sh") {
		t.Errorf("deny-builtin.sh must start with a POSIX shebang; got:\n%s", scriptBody)
	}
	if !strings.Contains(string(scriptBody), "exit 2") {
		t.Errorf("deny-builtin.sh must exit 2 (codex System Block) on built-in tool calls; got:\n%s", scriptBody)
	}

	info, _ := os.Stat(scriptPath)
	if mode := info.Mode().Perm(); mode&0o100 == 0 {
		t.Errorf("deny-builtin.sh must be owner-executable (perm includes 0100); got %o", mode)
	}

	cleanup()
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove hooks.json; stat err=%v", err)
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove deny-builtin.sh; stat err=%v", err)
	}
}

// TestWriteCodexProjectDenyBuiltinHooksRestoresOperatorScript guards
// against destroying an operator-owned deny-builtin.sh: if they had a
// custom script at that path, cleanup must restore it.
func TestWriteCodexProjectDenyBuiltinHooksRestoresOperatorScript(t *testing.T) {
	tmp := t.TempDir()
	hooksDir := filepath.Join(tmp, ".codex", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("seed hooks dir: %v", err)
	}
	priorScript := []byte("#!/bin/sh\n# operator's own deny script\nexit 1\n")
	scriptPath := filepath.Join(hooksDir, "deny-builtin.sh")
	if err := os.WriteFile(scriptPath, priorScript, 0o700); err != nil {
		t.Fatalf("seed pre-existing deny-builtin.sh: %v", err)
	}

	cleanup, err := writeCodexProjectDenyBuiltinHooks(tmp)
	if err != nil {
		t.Fatalf("writeCodexProjectDenyBuiltinHooks: %v", err)
	}

	mid, _ := os.ReadFile(scriptPath)
	if string(mid) == string(priorScript) {
		t.Error("mid-session, our orchestrator deny script should be installed, not the operator's prior script")
	}

	cleanup()
	restored, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("after cleanup, operator deny-builtin.sh should exist (restored): %v", err)
	}
	if string(restored) != string(priorScript) {
		t.Errorf("cleanup must restore operator deny-builtin.sh byte-for-byte\n  want: %q\n  got:  %q", priorScript, restored)
	}
}

// TestWriteCodexProjectArtifactsComposesAllArtifacts exercises the
// top-level composite writer: AGENTS.md + .codex/config.toml +
// .codex/hooks.json + .codex/hooks/deny-builtin.sh all land, and a
// single cleanup tears all four down in LIFO order.
func TestWriteCodexProjectArtifactsComposesAllArtifacts(t *testing.T) {
	tmp := t.TempDir()
	prompt := "Run cargo test before committing."
	mcpJSON := `{"api-bridge":{"command":"/opt/mcpbridge"}}`

	cleanup, err := writeCodexProjectArtifacts(tmp, prompt, mcpJSON, true)
	if err != nil {
		t.Fatalf("writeCodexProjectArtifacts: %v", err)
	}

	expected := []string{
		filepath.Join(tmp, "AGENTS.md"),
		filepath.Join(tmp, ".codex", "config.toml"),
		filepath.Join(tmp, ".codex", "hooks.json"),
		filepath.Join(tmp, ".codex", "hooks", "deny-builtin.sh"),
	}
	for _, path := range expected {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected artifact %q to exist after write: %v", path, err)
		}
	}

	cleanup()
	for _, path := range expected {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("cleanup must remove artifact %q created from nothing; stat err=%v", path, err)
		}
	}
}

// TestWriteCodexProjectArtifactsEmptyWorkingDirNoOps guards the
// orchestrator-cwd safety: empty workingDir must short-circuit.
func TestWriteCodexProjectArtifactsEmptyWorkingDirNoOps(t *testing.T) {
	cleanup, err := writeCodexProjectArtifacts("", "anything", `{"x":{"command":"y"}}`, true)
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil even for empty workingDir (no-op cleanup)")
	}
	cleanup() // must not panic
	for _, leak := range []string{"AGENTS.md", ".codex"} {
		if _, err := os.Stat(leak); err == nil {
			t.Errorf("%q must NOT be created in process cwd when workingDir is empty", leak)
			_ = os.RemoveAll(leak)
		}
	}
}
