package opencodecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestConfigCleanupChainRestoresAllFiles models the structured adapter's
// configCleanups defer chain: when both MCP config and the tools-deny
// block land in opencode.jsonc AND AGENTS.md is written, the cleanup
// chain must leave the workspace byte-identical to its pre-call state.
//
// The adapter's chain today is a slice of `func()` appended in
// write-order and invoked in that same order in a single deferred
// loop. The two files are independent (different paths), so order
// doesn't matter for correctness — what matters is that *every*
// cleanup fires.
func TestConfigCleanupChainRestoresAllFiles(t *testing.T) {
	tmp := t.TempDir()
	jsoncPath := filepath.Join(tmp, "opencode.jsonc")
	agentsPath := filepath.Join(tmp, "AGENTS.md")

	priorJsonc := []byte("{\n  \"operator\": true\n}\n")
	priorAgents := []byte("# Operator's project notes\n")
	if err := os.WriteFile(jsoncPath, priorJsonc, 0o600); err != nil {
		t.Fatalf("seed opencode.jsonc: %v", err)
	}
	if err := os.WriteFile(agentsPath, priorAgents, 0o600); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	sessionJsonc, err := buildOpenCodeProjectConfigJSON("", true)
	if err != nil {
		t.Fatalf("buildOpenCodeProjectConfigJSON: %v", err)
	}
	cleanupJsonc, err := writeOpenCodeRestoredFile(jsoncPath, sessionJsonc, true)
	if err != nil {
		t.Fatalf("writeOpenCodeRestoredFile jsonc: %v", err)
	}
	cleanupAgents, err := writeOpenCodeRestoredFile(agentsPath, []byte("session-only AGENTS.md"), true)
	if err != nil {
		t.Fatalf("writeOpenCodeRestoredFile agents: %v", err)
	}

	// Mid-session: operator content is shadowed by our writes.
	midJsonc, _ := os.ReadFile(jsoncPath)
	if string(midJsonc) == string(priorJsonc) {
		t.Error("mid-session opencode.jsonc must NOT match operator content — our tools-deny block should be installed")
	}
	midAgents, _ := os.ReadFile(agentsPath)
	if string(midAgents) == string(priorAgents) {
		t.Error("mid-session AGENTS.md must NOT match operator content — our session prompt should be installed")
	}

	// Cleanup chain runs in append order — same as the adapter's
	// `for _, fn := range configCleanups { fn() }`.
	cleanupJsonc()
	cleanupAgents()

	gotJsonc, err := os.ReadFile(jsoncPath)
	if err != nil {
		t.Fatalf("opencode.jsonc must exist after cleanup (restored): %v", err)
	}
	if string(gotJsonc) != string(priorJsonc) {
		t.Errorf("cleanup must byte-restore opencode.jsonc\n  want: %q\n  got:  %q", priorJsonc, gotJsonc)
	}
	gotAgents, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md must exist after cleanup (restored): %v", err)
	}
	if string(gotAgents) != string(priorAgents) {
		t.Errorf("cleanup must byte-restore AGENTS.md\n  want: %q\n  got:  %q", priorAgents, gotAgents)
	}
}

// TestConfigCleanupSurvivesExternallyDeletedFile guards against the
// crash mode where the user (or an MCP tool, or another process)
// deletes the file we wrote between our write and our cleanup. The
// cleanup must not panic; in the no-prior-content case it should
// simply succeed silently (the goal was "remove our file" and the
// file is already gone).
func TestConfigCleanupSurvivesExternallyDeletedFile(t *testing.T) {
	tmp := t.TempDir()
	jsoncPath := filepath.Join(tmp, "opencode.jsonc")

	cleanup, err := writeOpenCodeRestoredFile(jsoncPath, []byte("{}"), false)
	if err != nil {
		t.Fatalf("writeOpenCodeRestoredFile: %v", err)
	}

	if err := os.Remove(jsoncPath); err != nil {
		t.Fatalf("external delete: %v", err)
	}

	// Must not panic.
	cleanup()

	if _, err := os.Stat(jsoncPath); !os.IsNotExist(err) {
		t.Errorf("cleanup against an already-deleted file should leave it deleted; stat err=%v", err)
	}
}

// TestConfigCleanupIsIdempotent calls cleanup twice. The adapter's
// defer pattern only invokes each fn() once today, but multiple
// callers re-using a cleanup (or future test seams) shouldn't panic
// or corrupt restored operator content the second time around.
func TestConfigCleanupIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	jsoncPath := filepath.Join(tmp, "opencode.jsonc")
	operator := []byte("{\n  \"operator\": true\n}\n")
	if err := os.WriteFile(jsoncPath, operator, 0o600); err != nil {
		t.Fatalf("seed operator opencode.jsonc: %v", err)
	}

	cleanup, err := writeOpenCodeRestoredFile(jsoncPath, []byte("{\"session\":true}"), true)
	if err != nil {
		t.Fatalf("writeOpenCodeRestoredFile: %v", err)
	}

	cleanup() // first call: restores operator content
	cleanup() // second call: must not panic, must not corrupt

	got, err := os.ReadFile(jsoncPath)
	if err != nil {
		t.Fatalf("opencode.jsonc must remain after double-cleanup: %v", err)
	}
	if string(got) != string(operator) {
		t.Errorf("double-cleanup must leave operator content intact\n  want: %q\n  got:  %q", operator, got)
	}
}

// TestBuildOpenCodeProjectConfigJSONEmitsValidJSON asserts the file
// we land at opencode.jsonc parses as JSON — the .jsonc extension
// allows comments but our generator emits plain JSON, so any malformed
// output is a regression. opencode silently ignores config it can't
// parse, which would re-introduce the original bug (deny not applied
// without a loud error).
func TestBuildOpenCodeProjectConfigJSONEmitsValidJSON(t *testing.T) {
	cases := []struct {
		name          string
		mcpJSON       string
		denyBuiltins  bool
		mustContain   []string
		mustNotParse  bool
		mustHaveKeys  []string
	}{
		{
			name:         "tools-only",
			mcpJSON:      "",
			denyBuiltins: true,
			mustHaveKeys: []string{"tools"},
		},
		{
			name:         "mcp-only",
			mcpJSON:      `{"mcpServers":{"bridge":{"command":["/bin/true"]}}}`,
			denyBuiltins: false,
			mustHaveKeys: []string{"mcp"},
		},
		{
			name:         "both",
			mcpJSON:      `{"mcpServers":{"bridge":{"command":["/bin/true"]}}}`,
			denyBuiltins: true,
			mustHaveKeys: []string{"mcp", "tools"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := buildOpenCodeProjectConfigJSON(tc.mcpJSON, tc.denyBuiltins)
			if err != nil {
				t.Fatalf("buildOpenCodeProjectConfigJSON: %v", err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("emitted opencode.jsonc must be valid JSON; got %v\ndata:\n%s", err, out)
			}
			for _, key := range tc.mustHaveKeys {
				if _, ok := parsed[key]; !ok {
					t.Errorf("expected top-level key %q in emitted config; got keys=%v", key, mapKeys(parsed))
				}
			}
		})
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBuildOpenCodeProjectConfigJSONMergesCommandAndArgs locks in the
// MCP schema translation from the caller-facing
// {"command":"<exe>","args":[<rest>]} shape (Claude Desktop / cline /
// mcpServers convention) to opencode 1.15.4's expected
// {"command":["<exe>",<rest>...]} single-array shape. Without this
// merge opencode silently drops the MCP server and presents an empty
// tool list — the symptom that broke TestOpenCodeCLIStructuredMCPBridge.
func TestBuildOpenCodeProjectConfigJSONMergesCommandAndArgs(t *testing.T) {
	input := `{"mcpServers":{"api-bridge":{"command":"node","args":["/tmp/server.js","--port","9999"]}}}`
	out, err := buildOpenCodeProjectConfigJSON(input, false)
	if err != nil {
		t.Fatalf("buildOpenCodeProjectConfigJSON: %v", err)
	}

	var parsed struct {
		MCP map[string]struct {
			Command []string    `json:"command"`
			Args    interface{} `json:"args,omitempty"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal generated config: %v\ndata:\n%s", err, out)
	}
	server, ok := parsed.MCP["api-bridge"]
	if !ok {
		t.Fatalf("api-bridge missing from generated mcp block; got: %s", out)
	}
	want := []string{"node", "/tmp/server.js", "--port", "9999"}
	if len(server.Command) != len(want) {
		t.Errorf("command length = %d, want %d. command=%v", len(server.Command), len(want), server.Command)
	}
	for i, w := range want {
		if i < len(server.Command) && server.Command[i] != w {
			t.Errorf("command[%d] = %q, want %q. command=%v", i, server.Command[i], w, server.Command)
		}
	}
	if server.Args != nil {
		t.Errorf("args field must be removed after merge; got %v", server.Args)
	}
}

// TestBuildOpenCodeProjectConfigJSONPreservesAlreadyArrayCommand
// guards the schema translation against double-wrapping: callers who
// already pass {"command":["exe","arg"]} (opencode-native shape)
// should see their command preserved verbatim, not nested into another
// array.
func TestBuildOpenCodeProjectConfigJSONPreservesAlreadyArrayCommand(t *testing.T) {
	input := `{"mcpServers":{"already-array":{"command":["already","an","array"]}}}`
	out, err := buildOpenCodeProjectConfigJSON(input, false)
	if err != nil {
		t.Fatalf("buildOpenCodeProjectConfigJSON: %v", err)
	}
	var parsed struct {
		MCP map[string]struct {
			Command []string `json:"command"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := parsed.MCP["already-array"].Command
	want := []string{"already", "an", "array"}
	if len(got) != len(want) {
		t.Fatalf("command length = %d, want %d. command=%v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("command[%d] = %q, want %q", i, got[i], w)
		}
	}
}
