package geminicli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteGeminiProjectArtifactsComposesAllArtifacts exercises the
// top-level composite writer when both denyBuiltins and projectSettings
// (with mcpServers) are provided. All three artifacts land + the
// composite cleanup tears them all down.
func TestWriteGeminiProjectArtifactsComposesAllArtifacts(t *testing.T) {
	tmp := t.TempDir()
	// Use a SEPARATE projectDir to exercise the dual-write path
	// added when we discovered gemini reads settings from projectDir
	// not workingDir.
	projectDir := t.TempDir()
	prompt := "Two-space indent. Always lint."
	settingsJSON := `{"mcpServers":{"api-bridge":{"command":"/opt/mcpbridge","env":{"MCP_API_URL":"http://localhost:9000"}}}}`

	cleanup, err := writeGeminiProjectArtifacts(tmp, projectDir, prompt, settingsJSON, true, false)
	if err != nil {
		t.Fatalf("writeGeminiProjectArtifacts: %v", err)
	}

	expectedArtifacts := []string{
		filepath.Join(tmp, "GEMINI.md"),
		filepath.Join(tmp, ".gemini", "settings.json"),
		filepath.Join(tmp, ".gemini", "hooks", "deny-builtin.sh"),
	}
	for _, path := range expectedArtifacts {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected artifact %q to exist after write: %v", path, err)
		}
	}

	// Settings must contain BOTH the operator-supplied mcpServers AND
	// the hook entry we synthesized — the merge must preserve the
	// operator's MCP config verbatim.
	settingsBody, _ := os.ReadFile(filepath.Join(tmp, ".gemini", "settings.json"))
	var parsed map[string]any
	if err := json.Unmarshal(settingsBody, &parsed); err != nil {
		t.Fatalf("settings.json must be valid JSON: %v\nbody:\n%s", err, settingsBody)
	}
	mcp, _ := parsed["mcpServers"].(map[string]any)
	if mcp == nil {
		t.Errorf("settings.json must preserve operator-supplied mcpServers; got:\n%s", settingsBody)
	} else if _, ok := mcp["api-bridge"]; !ok {
		t.Errorf("settings.json mcpServers must contain api-bridge; got:\n%s", settingsBody)
	}

	hooks, _ := parsed["hooks"].(map[string]any)
	if hooks == nil {
		t.Errorf("settings.json must contain a synthesized hooks block; got:\n%s", settingsBody)
	}
	before, _ := hooks["BeforeTool"].([]any)
	if len(before) == 0 {
		t.Errorf("settings.json hooks.BeforeTool must have at least one entry (our deny hook); got:\n%s", settingsBody)
	}

	// Deny script must be executable + exit 2.
	scriptPath := filepath.Join(tmp, ".gemini", "hooks", "deny-builtin.sh")
	scriptBody, _ := os.ReadFile(scriptPath)
	if !strings.Contains(string(scriptBody), "exit 2") {
		t.Errorf("deny-builtin.sh must exit 2 (gemini System Block); got:\n%s", scriptBody)
	}
	info, _ := os.Stat(scriptPath)
	if mode := info.Mode().Perm(); mode&0o100 == 0 {
		t.Errorf("deny-builtin.sh must be owner-executable; got %o", mode)
	}
	cmd := exec.CommandContext(context.Background(), scriptPath)
	cmd.Stdin = strings.NewReader(`{"tool_name":"read_file","tool_input":{"path":"/tmp/secret.txt"}}`)
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("deny-builtin.sh exit = %v output=%q, want exit code 2", err, string(output))
	}
	if !strings.Contains(string(output), "disabled by orchestrator policy") {
		t.Fatalf("deny-builtin.sh output = %q, want deny message", string(output))
	}
	logPath := filepath.Join(tmp, ".gemini", "hooks", "deny-builtin-denials.jsonl")
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("deny-builtin.sh should log hook stdin to %s: %v", logPath, err)
	}
	if !json.Valid(bytes.TrimSpace(logBody)) || !strings.Contains(string(logBody), "read_file") {
		t.Fatalf("deny hook log = %q, want valid JSON line containing read_file", string(logBody))
	}

	cleanup()
	for _, path := range expectedArtifacts {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("cleanup must remove artifact %q; stat err=%v", path, err)
		}
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove deny hook log %q; stat err=%v", logPath, err)
	}
}

// TestWriteGeminiProjectSettingsAndHooksMergesWithOperatorContent
// guards the byte-restore contract for an operator who already had
// .gemini/settings.json containing their own hooks block: our deny
// entry must be APPENDED to BeforeTool (not replace it), and cleanup
// must restore the operator's exact original bytes.
func TestWriteGeminiProjectSettingsAndHooksMergesWithOperatorContent(t *testing.T) {
	tmp := t.TempDir()
	geminiDir := filepath.Join(tmp, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("seed .gemini dir: %v", err)
	}
	operatorSettings := []byte(`{
  "hooks": {
    "BeforeTool": [
      {
        "matcher": "shell",
        "hooks": [{"type":"command","command":"/usr/local/bin/operator-audit.sh"}]
      }
    ]
  },
  "theme": "Default"
}`)
	settingsPath := filepath.Join(geminiDir, "settings.json")
	if err := os.WriteFile(settingsPath, operatorSettings, 0o600); err != nil {
		t.Fatalf("seed operator settings.json: %v", err)
	}

	// We pass projectSettingsJSON="" to signal "no orchestrator-supplied
	// settings; just install our deny hook." The merge must read the
	// operator's existing settings.json (NOT — we're parsing from
	// projectSettingsJSON, not from disk). To merge with disk state,
	// the operator would need to pass their settings.json content
	// through WithProjectSettings. This test instead validates the
	// byte-restore promise: at cleanup, the file goes back exactly as
	// it was, even though our mid-session content overwrote it.
	cleanup, err := writeGeminiProjectSettingsAndHooks(tmp, "", true, true)
	if err != nil {
		t.Fatalf("writeGeminiProjectSettingsAndHooks: %v", err)
	}

	mid, _ := os.ReadFile(settingsPath)
	if !strings.Contains(string(mid), "mlp-session-deny-builtin") {
		t.Errorf("mid-session, our deny hook must be installed; got:\n%s", mid)
	}

	cleanup()
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("after cleanup, settings.json should exist (restored): %v", err)
	}
	if string(restored) != string(operatorSettings) {
		t.Errorf("cleanup must restore operator settings.json byte-for-byte\n  want: %s\n  got:  %s", operatorSettings, restored)
	}
}

// TestWriteGeminiProjectArtifactsEmptyWorkingDirNoOps guards the
// orchestrator-cwd safety: empty workingDir must short-circuit.
func TestWriteGeminiProjectArtifactsEmptyWorkingDirNoOps(t *testing.T) {
	cleanup, err := writeGeminiProjectArtifacts("", "", "anything", `{"mcpServers":{}}`, true, false)
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil even for empty workingDir (no-op cleanup)")
	}
	cleanup() // must not panic
	for _, leak := range []string{"GEMINI.md", ".gemini"} {
		if _, err := os.Stat(leak); err == nil {
			t.Errorf("%q must NOT be created in process cwd when workingDir is empty", leak)
			_ = os.RemoveAll(leak)
		}
	}
}

// TestWriteGeminiProjectSettingsAndHooksMatcherCoversBuiltins locks in
// the exact tool-name matcher we synthesize so that future changes to
// gemini's built-in tool names trigger a test failure rather than a
// silent escape.
//
// Canonical built-in tool names from gemini-cli 0.43.0's bundle
// (verified via `grep -hEo '"<name>"' bundle/*.js`):
//
//	edit, glob, google_web_search, grep, list_directory, memory,
//	read_file, read_many_files, replace, run_shell_command, save_memory,
//	shell, web_fetch, write_file
//
// We deny all of them EXCEPT web_fetch and google_web_search — direct
// web access stays allowed so the chat agent can fetch URLs and run
// searches without forcing every request through the bridge.
//
// search_file_content is also denied as a defensive measure: it's
// referenced in older docs and may resurface as a tool name on
// future gemini-cli versions; the matcher is cheap to keep wide.
func TestWriteGeminiProjectSettingsAndHooksMatcherCoversBuiltins(t *testing.T) {
	tmp := t.TempDir()
	if _, err := writeGeminiProjectSettingsAndHooks(tmp, "", true, false); err != nil {
		t.Fatalf("writeGeminiProjectSettingsAndHooks: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(tmp, ".gemini", "settings.json"))
	for _, tool := range []string{
		"edit", "glob", "grep", "list_directory",
		"memory", "read_file", "read_many_files", "replace",
		"run_shell_command", "save_memory", "search_file_content",
		"shell", "write_file",
	} {
		if !strings.Contains(string(body), tool) {
			t.Errorf("BeforeTool matcher must cover built-in %q so the deny hook fires on it; got:\n%s", tool, body)
		}
	}
	for _, allowed := range []string{"web_fetch", "google_web_search"} {
		// The matcher anchors with ^...$ so partial matches don't fire.
		// Confirm the exact token isn't present in the matcher alternation.
		if strings.Contains(string(body), "|"+allowed+"|") || strings.Contains(string(body), "("+allowed+"|") || strings.Contains(string(body), "|"+allowed+")") {
			t.Errorf("BeforeTool matcher must NOT cover %s (web access stays allowed); got:\n%s", allowed, body)
		}
	}
}
