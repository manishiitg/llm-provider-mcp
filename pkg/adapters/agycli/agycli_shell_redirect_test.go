package agycli

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func agyRunHook(t *testing.T, python, mode string, payload map[string]interface{}) (decision, reason string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", agyToolModeHookCommand(python, mode))
	cmd.Stdin = strings.NewReader(string(raw))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook command: %v: %s", err, out)
	}
	var got struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("hook JSON: %v: %s", err, out)
	}
	return got.Decision, got.Reason
}

func TestAgyShellRedirectHook(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	shell := func(command string) map[string]interface{} {
		// AGY transcripts carry run_command args as JSON-encoded strings.
		quoted, _ := json.Marshal(command)
		return map[string]interface{}{"toolCall": map[string]interface{}{
			"name": "run_command", "args": map[string]string{"CommandLine": string(quoted), "Cwd": `"/tmp"`},
		}}
	}
	for _, command := range []string{
		`curl -s "$MCP_CUSTOM/get_contract_upgrades" -H "$MCP_AUTH" --json '{}'`,
		`curl -s ${MCP_API_TOKEN}`,
		`curl -s http://127.0.0.1:1/tools/custom/x`,
		`curl http://h/tools/virtual/y`,
		`curl http://h/tools/mcp/z`,
	} {
		decision, reason := agyRunHook(t, python, "full", shell(command))
		if decision != "deny" {
			t.Fatalf("%q: decision = %q, want deny", command, decision)
		}
		for _, want := range []string{"missing or invalid Authorization header", "execute_shell_command", "call_mcp_tool"} {
			if !strings.Contains(reason, want) {
				t.Fatalf("%q: reason %q lacks %q", command, reason, want)
			}
		}
	}
	for _, command := range []string{`echo hello`, `ls -la`, `echo $MCP_CUSTOMER`, `git status`} {
		if decision, _ := agyRunHook(t, python, "full", shell(command)); decision != "allow" {
			t.Fatalf("%q: decision = %q, want allow", command, decision)
		}
	}
	// Only the native shell is redirected; other tools and the bridge pass.
	other := map[string]interface{}{"toolCall": map[string]interface{}{"name": "call_mcp_tool", "args": map[string]string{"Arguments": `{"command":"curl $MCP_CUSTOM/x"}`}}}
	if decision, _ := agyRunHook(t, python, "full", other); decision != "allow" {
		t.Fatalf("bridge call decision = %q, want allow", decision)
	}
	// mcp_only already denies every native tool; the redirect never grants.
	if decision, _ := agyRunHook(t, python, "mcp_only", shell(`echo hello`)); decision != "deny" {
		t.Fatalf("mcp_only native shell decision = %q, want deny", decision)
	}
}

func TestAgyShellRedirectKeepsManagedHookRestore(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	// The redirect lives inside the managed gate, so ownership detection (used for
	// stale-backup recovery and release) still recognises the entry in both modes.
	for _, mode := range []string{"mcp_only", "full"} {
		if !agyIsManagedToolHook(agyManagedToolHookEntry(python, mode)) {
			t.Fatalf("mode %s: managed hook entry not recognised", mode)
		}
	}
}
