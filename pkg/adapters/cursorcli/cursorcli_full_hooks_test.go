package cursorcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Full CLI mode: Cursor's own shell, reads and edits are not denied, its shell
// is approved by the hook (never --force), and subagents, cloud/background
// agents and computer use stay denied. The generated hooks.json must be valid.
func TestCursorFullNativeHooks(t *testing.T) {
	cursorDir := filepath.Join(t.TempDir(), ".cursor")
	cleanup, err := writeCursorDenyBuiltinHooks(cursorDir, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(filepath.Join(cursorDir, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hooks struct {
		Hooks map[string][]struct {
			Command string `json:"command"`
			Matcher string `json:"matcher"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v\n%s", err, raw)
	}
	if _, ok := hooks.Hooks["beforeReadFile"]; ok {
		t.Error("full mode must not deny native reads")
	}
	shell := hooks.Hooks["beforeShellExecution"]
	if len(shell) != 1 || shell[0].Command != `echo '{"permission":"allow"}'` {
		t.Errorf("shell must be approved by the hook, got %+v", shell)
	}
	denied := map[string]bool{}
	for _, name := range strings.Split(hooks.Hooks["preToolUse"][0].Matcher, "|") {
		denied[name] = true
	}
	for _, allowed := range []string{"Shell", "Read", "Edit", "Write", "Delete", "Grep"} {
		if denied[allowed] {
			t.Errorf("%s is denied in full mode", allowed)
		}
	}
	for _, blocked := range []string{"CloudAgent", "BackgroundAgent", "Task", "Subagent", "ComputerUse"} {
		if !denied[blocked] {
			t.Errorf("%s must stay denied in full mode", blocked)
		}
	}
	if !strings.Contains(cursorBridgeOnlySystemPrompt("", true, true), "Shell, Read") {
		t.Error("the full-mode prompt must tell Cursor its own tools are on")
	}
}
