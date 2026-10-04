package musecli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func runShellRedirectHook(t *testing.T, payload string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	path, err := museWriteShellRedirectHook()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "node", path)
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited with an error: %v", err)
	}
	return string(out)
}

// The bridge's credentials exist only in the bridge's own shell. A native bash call that targets the
// platform can only fail with "missing or invalid Authorization header"; the hook says so up front and
// names the tool that can make the call. Everything else in bash is left alone.
func TestShellRedirectHookRefusesOnlyPlatformCallsInNativeBash(t *testing.T) {
	refused := []string{
		`{"tool_name":"bash","tool_input":{"command":"curl -s -X POST \"$MCP_CUSTOM/get_contract_upgrades\" -H \"$MCP_AUTH\" --json '{}'"}}`,
		`{"tool_name":"bash","tool_input":{"command":"curl -s \"${MCP_CUSTOM}/x\""}}`,
		`{"tool_name":"bash","tool_input":{"command":"curl http://127.0.0.1:18743/tools/custom/get_contract_upgrades --json '{}'"}}`,
		`{"tool_name":"bash_input","tool_input":{"command":"curl http://localhost:1/tools/virtual/get_api_spec"}}`,
	}
	for _, payload := range refused {
		out := runShellRedirectHook(t, payload)
		var decision struct {
			Hook struct {
				Event    string `json:"hookEventName"`
				Decision string `json:"permissionDecision"`
				Reason   string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(out), &decision); err != nil {
			t.Fatalf("payload %s: output %q is not a decision: %v", payload, out, err)
		}
		if decision.Hook.Event != "PreToolUse" || decision.Hook.Decision != "deny" {
			t.Errorf("payload %s: decision = %+v, want a PreToolUse deny", payload, decision.Hook)
		}
		if !strings.Contains(decision.Hook.Reason, "mcp__api_bridge__execute_shell_command") {
			t.Errorf("payload %s: reason %q does not name the bridge shell tool", payload, decision.Hook.Reason)
		}
	}
	allowed := []string{
		`{"tool_name":"bash","tool_input":{"command":"ls -la && cat project/workflow.json"}}`,
		`{"tool_name":"bash","tool_input":{"command":"curl -s https://example.com/api/tools-list"}}`,
		`{"tool_name":"bash","tool_input":{"command":"echo $HOME"}}`,
		`{"tool_name":"mcp__api_bridge__execute_shell_command","tool_input":{"command":"curl \"$MCP_CUSTOM/get_contract_upgrades\" -H \"$MCP_AUTH\""}}`,
		`{"tool_name":"read_file","tool_input":{"path":"/tools/custom/x"}}`,
		`not json at all`,
		``,
	}
	for _, payload := range allowed {
		if out := runShellRedirectHook(t, payload); out != "" {
			t.Errorf("payload %q was refused: %s", payload, out)
		}
	}
}

// Full CLI mode has no native-tool allowlist, so the allowlist hook is absent; the redirect must still
// be installed whenever the bridge is mounted, next to whatever hooks the person already has.
func TestApplyMCPConfigInstallsShellRedirectWithoutAnAllowlist(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	path := redirectMuseConfigHome(t)
	if err := os.MkdirAll(strings.TrimSuffix(path, "/settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"hooks":{"PreToolUse":[{"matcher":"mine","hooks":[{"type":"command","command":"true"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bridge := `{"mcpServers":{"api-bridge":{"command":"/bin/true","env":{"MCP_API_URL":"http://127.0.0.1:1"}}}}`
	restore, err := museApplyMCPConfigAtPath(path, bridge, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	var mine, redirect int
	for _, entry := range settings.Hooks.PreToolUse {
		if entry.Matcher == "mine" {
			mine++
		}
		for _, h := range entry.Hooks {
			if strings.Contains(h.Command, "shell-redirect-") {
				redirect++
			}
		}
	}
	if mine != 1 || redirect != 1 {
		t.Errorf("PreToolUse has %d of the person's hooks and %d redirect hooks, want 1 and 1: %s", mine, redirect, raw)
	}
	restore()
	if back, _ := os.ReadFile(path); !strings.Contains(string(back), `"matcher":"mine"`) || strings.Contains(string(back), "shell-redirect-") {
		t.Errorf("restore did not put the person's settings back: %s", back)
	}

	// No bridge mounted: nothing is installed.
	restore2, err := museApplyMCPConfigAtPath(path, `{"mcpServers":{"other":{"command":"/bin/true"}}}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restore2()
	if raw2, _ := os.ReadFile(path); strings.Contains(string(raw2), "shell-redirect-") {
		t.Errorf("redirect hook installed without a bridge: %s", raw2)
	}
}
