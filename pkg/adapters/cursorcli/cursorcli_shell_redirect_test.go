package cursorcli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testBridgeMCP = `{"mcpServers":{"api-bridge":{"command":"x","env":{"MCP_API_URL":"http://127.0.0.1:1"}},"other":{"command":"y"}}}`

func runCursorShellHook(t *testing.T, script, command string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{"command": command, "cwd": "/work/tools/custom/x", "hook_event_name": "beforeShellExecution"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "/bin/bash", script)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestCursorShellRedirectHook(t *testing.T) {
	cursorDir := filepath.Join(t.TempDir(), ".cursor")
	cleanup, err := writeCursorDenyBuiltinHooksWithBridge(cursorDir, true, true, cursorBridgeServerName(testBridgeMCP))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	script := filepath.Join(cursorDir, "hooks", "mlp-allow-shell.sh")

	for _, command := range []string{
		`curl -s "$MCP_CUSTOM/get_contract_upgrades" -H "$MCP_AUTH" --json '{}'`,
		`curl -H "Authorization: Bearer ${MCP_API_TOKEN}" x`,
		`curl http://127.0.0.1:9/tools/custom/get_x`,
	} {
		out := runCursorShellHook(t, script, command)
		var verdict struct {
			Permission   string `json:"permission"`
			AgentMessage string `json:"agent_message"`
		}
		if err := json.Unmarshal([]byte(out), &verdict); err != nil {
			t.Fatalf("not JSON for %q: %q", command, out)
		}
		if verdict.Permission != "deny" || !strings.Contains(verdict.AgentMessage, "api-bridge-execute_shell_command") || !strings.Contains(verdict.AgentMessage, "missing or invalid Authorization header") {
			t.Errorf("%q not redirected: %q", command, out)
		}
	}
	for _, command := range []string{"echo hi", "ls -la", "curl -s https://example.com/api", "echo MCP_CUSTOMER"} {
		if out := runCursorShellHook(t, script, command); out != `{"permission":"allow"}` {
			t.Errorf("%q must stay allowed, got %q", command, out)
		}
	}
}

func TestCursorShellRedirectNeedsBridge(t *testing.T) {
	if name := cursorBridgeServerName(`{"mcpServers":{"other":{"command":"y"}}}`); name != "" {
		t.Fatalf("no bridge expected, got %q", name)
	}
	cursorDir := filepath.Join(t.TempDir(), ".cursor")
	cleanup, err := writeCursorDenyBuiltinHooksWithBridge(cursorDir, true, true, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(cursorDir, "hooks", "mlp-allow-shell.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "MCP_CUSTOM") {
		t.Error("no bridge: the shell script must not redirect")
	}
	if out := runCursorShellHook(t, filepath.Join(cursorDir, "hooks", "mlp-allow-shell.sh"), `curl "$MCP_CUSTOM/x"`); out != `{"permission":"allow"}` {
		t.Errorf("no bridge: got %q", out)
	}
}

func TestCursorShellRedirectKeepsPersonsHooks(t *testing.T) {
	cursorDir := filepath.Join(t.TempDir(), ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prior := `{"version":1,"hooks":{"stop":[{"command":"./mine.sh"}]}}`
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	if err := os.WriteFile(hooksPath, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanup, err := writeCursorDenyBuiltinHooksWithBridge(cursorDir, true, true, "api-bridge")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	got, err := os.ReadFile(hooksPath)
	if err != nil || string(got) != prior {
		t.Fatalf("person's hooks.json not restored: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(cursorDir, "hooks", "mlp-allow-shell.sh")); !os.IsNotExist(err) {
		t.Error("redirect script left behind")
	}
}
