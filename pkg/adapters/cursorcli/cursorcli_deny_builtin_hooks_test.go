package cursorcli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestWriteCursorDenyBuiltinHooksLifecycle covers the unit-level invariants
// of the deny-builtin hooks installer: the .cursor/hooks.json + deny script
// are written with the right content, the script is executable, cleanup
// restores any prior hooks.json the operator had, and cleanup removes the
// dir tree we created.
func TestWriteCursorDenyBuiltinHooksLifecycle(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, ".cursor")

	cleanup, err := writeCursorDenyBuiltinHooks(cursorDir)
	if err != nil {
		t.Fatalf("writeCursorDenyBuiltinHooks: %v", err)
	}

	// hooks.json content + structure
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("expected hooks.json at %s: %v", hooksPath, err)
	}
	var parsed struct {
		Version int                                 `json:"version"`
		Hooks   map[string][]map[string]interface{} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("hooks.json must be valid JSON: %v\ncontent:\n%s", err, raw)
	}
	if parsed.Version != 1 {
		t.Errorf("hooks.json version = %d, want 1", parsed.Version)
	}
	for _, ev := range []string{"beforeShellExecution", "beforeReadFile"} {
		hooks, ok := parsed.Hooks[ev]
		if !ok || len(hooks) == 0 {
			t.Errorf("hooks.json missing %q event entry", ev)
			continue
		}
		cmd, _ := hooks[0]["command"].(string)
		if !strings.Contains(cmd, "mlp-deny-builtin.sh") {
			t.Errorf("hook %q command should reference mlp-deny-builtin.sh, got %q", ev, cmd)
		}
	}

	// deny script content + executability
	scriptPath := filepath.Join(cursorDir, "hooks", "mlp-deny-builtin.sh")
	scriptRaw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("expected deny script at %s: %v", scriptPath, err)
	}
	if !strings.Contains(string(scriptRaw), `"permission":"deny"`) {
		t.Errorf("deny script should emit permission=deny JSON; got:\n%s", scriptRaw)
	}
	if !strings.Contains(string(scriptRaw), "api-bridge") {
		t.Errorf("deny script user_message should point at api-bridge; got:\n%s", scriptRaw)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat deny script: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o100 == 0 {
		t.Errorf("deny script must be owner-executable; mode=%o", mode)
	}
	cmd := exec.CommandContext(context.Background(), "bash", scriptPath)
	cmd.Stdin = strings.NewReader(`{"tool_name":"Read","tool_input":{"path":"/tmp/secret.txt"}}`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deny script execution error = %v output=%q", err, string(output))
	}
	var decision struct {
		Permission   string `json:"permission"`
		UserMessage  string `json:"user_message"`
		AgentMessage string `json:"agent_message"`
	}
	if err := json.Unmarshal(output, &decision); err != nil {
		t.Fatalf("deny script output must be valid JSON: %v output=%q", err, string(output))
	}
	if decision.Permission != "deny" || !strings.Contains(decision.AgentMessage, "api-bridge") {
		t.Fatalf("deny script decision = %#v, want permission deny with bridge guidance", decision)
	}
	logPath := filepath.Join(cursorDir, "hooks", "mlp-deny-builtin-denials.jsonl")
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("deny script should log hook stdin to %s: %v", logPath, err)
	}
	if !json.Valid([]byte(strings.TrimSpace(string(logBody)))) || !strings.Contains(string(logBody), "Read") {
		t.Fatalf("deny script log = %q, want valid JSON line containing Read", string(logBody))
	}

	// Cleanup removes what we wrote and leaves the temp dir empty.
	cleanup()
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Errorf("cleanup should have removed hooks.json; stat err=%v", err)
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Errorf("cleanup should have removed deny script; stat err=%v", err)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) > 0 {
		t.Errorf("cleanup should have removed .cursor dir; remaining entries=%v", entries)
	}
}

// TestWriteCursorDenyBuiltinHooksRestoresPreExistingHooksJSON guards the
// promise the comment makes: if the operator already had .cursor/hooks.json
// in their workspace, cleanup must restore it byte-for-byte instead of
// removing it. Without this guard the adapter would silently destroy
// user-owned hook config every time the option was enabled.
func TestWriteCursorDenyBuiltinHooksRestoresPreExistingHooksJSON(t *testing.T) {
	tmp := t.TempDir()
	cursorDir := filepath.Join(tmp, ".cursor")
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		t.Fatalf("mkdir cursor dir: %v", err)
	}
	preExisting := []byte(`{"version":1,"hooks":{"sessionStart":[{"command":"./user-hook.sh"}]}}`)
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	if err := os.WriteFile(hooksPath, preExisting, 0o600); err != nil {
		t.Fatalf("seed pre-existing hooks.json: %v", err)
	}

	cleanup, err := writeCursorDenyBuiltinHooks(cursorDir)
	if err != nil {
		t.Fatalf("writeCursorDenyBuiltinHooks with pre-existing config: %v", err)
	}
	// Mid-session our deny config is active.
	active, _ := os.ReadFile(hooksPath)
	if strings.Contains(string(active), "user-hook.sh") {
		t.Fatal("mid-session, pre-existing user-hook content should not be visible — our deny config must be installed")
	}

	cleanup()
	restored, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("after cleanup, hooks.json should exist (restored from pre-existing): %v", err)
	}
	if string(restored) != string(preExisting) {
		t.Errorf("cleanup must restore pre-existing hooks.json byte-for-byte\n  want: %q\n  got:  %q", preExisting, restored)
	}
}

func TestPrepareCursorProjectFilesDenyBuiltinPermissionsCoversReadListSearch(t *testing.T) {
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithDenyBuiltinTools(true)(opts)

	cleanup, err := prepareCursorProjectFiles(workDir, "", opts, "test-session-cursor")
	if err != nil {
		t.Fatalf("prepareCursorProjectFiles: %v", err)
	}
	defer cleanup()

	body, err := os.ReadFile(filepath.Join(workDir, ".cursor", "cli.json"))
	if err != nil {
		t.Fatalf("read generated cli.json: %v", err)
	}
	for _, resource := range []string{"Shell(*)", "Read(*)", "ListDir(*)", "Glob(*)", "Grep(*)", "Search(*)", "WebSearch(*)"} {
		if !strings.Contains(string(body), resource) {
			t.Fatalf("cli.json = %s, want deny resource %s", string(body), resource)
		}
	}
}
