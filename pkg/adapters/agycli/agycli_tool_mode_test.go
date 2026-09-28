package agycli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgyToolModeHookDecisions(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, tc := range []struct {
		mode, tool, want string
	}{
		{"mcp_only", "call_mcp_tool", "allow"},
		{"mcp_only", "view_file", "deny"},
		{"mcp_only", "write_to_file", "deny"},
		{"hybrid", "call_mcp_tool", "allow"},
		{"hybrid", "view_file", "allow"},
		{"hybrid", "grep_search", "allow"},
		{"hybrid", "write_to_file", "deny"},
		{"hybrid", "run_command", "deny"},
		{"hybrid", "invoke_subagent", "deny"},
		{"hybrid", "new_future_tool", "deny"},
	} {
		t.Run(tc.mode+"/"+tc.tool, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", agyToolModeHookCommand(python, tc.mode))
			payload, _ := json.Marshal(map[string]interface{}{"toolCall": map[string]string{"name": tc.tool}})
			cmd.Stdin = strings.NewReader(string(payload))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("hook command: %v: %s", err, out)
			}
			var decision struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(out, &decision); err != nil {
				t.Fatalf("hook JSON: %v: %s", err, out)
			}
			if decision.Decision != tc.want {
				t.Fatalf("decision = %q, want %q", decision.Decision, tc.want)
			}
		})
	}
}

func TestAgyToolModeHookFailsClosedOnParseOrInterpreterError(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	for _, tc := range []struct {
		name, python, input string
	}{
		{"malformed input", python, "{"},
		{"missing interpreter", filepath.Join(t.TempDir(), "missing-python"), `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", agyToolModeHookCommand(tc.python, "mcp_only"))
			cmd.Stdin = strings.NewReader(tc.input)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var decision struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(out, &decision); err != nil || decision.Decision != "deny" {
				t.Fatalf("failed gate output = %q, parse error = %v", out, err)
			}
		})
	}
}

func TestAgyWorkspaceToolHookPreservesUserHooks(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	workDir := t.TempDir()
	path := filepath.Join(workDir, ".agents", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"user-hook\": {\"enabled\": false}\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	release1, err := agyHoldToolModeHook(workDir, "hybrid")
	if err != nil {
		t.Fatal(err)
	}
	release2, err := agyHoldToolModeHook(workDir, "hybrid")
	if err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var activeHooks map[string]interface{}
	if err := json.Unmarshal(active, &activeHooks); err != nil {
		t.Fatal(err)
	}
	if len(activeHooks) != 1 || activeHooks[agyToolModeHookName] == nil {
		t.Fatalf("foreign hooks remained executable during hold: %v", activeHooks)
	}
	if _, err := agyHoldToolModeHook(workDir, "mcp_only"); err == nil {
		t.Fatal("conflicting workspace policy was accepted")
	}
	release1()
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), agyToolModeHookName) {
		t.Fatal("first holder removed the shared hook")
	}
	release2()
	data, err = os.ReadFile(path)
	if err != nil || string(data) != string(original) {
		t.Fatalf("original user hooks not restored: err=%v data=%q", err, data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("original hook mode not restored: info=%v err=%v", info, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	release3, err := agyHoldToolModeHook(workDir, "mcp_only")
	if err != nil {
		t.Fatal(err)
	}
	release3()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("hook file left behind: %v", err)
	}
	python, _ := exec.LookPath("python3")
	stale := map[string]interface{}{agyToolModeHookName: map[string]interface{}{
		"PreToolUse": []interface{}{map[string]interface{}{
			"matcher": "*", "hooks": []interface{}{map[string]interface{}{
				"type": "command", "command": agyToolModeHookCommand(python, "hybrid"),
			}},
		}},
	}}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := agyWriteHooksFile(path, stale); err != nil {
		t.Fatal(err)
	}
	release4, err := agyHoldToolModeHook(workDir, "mcp_only")
	if err != nil {
		t.Fatalf("recover stale managed hook: %v", err)
	}
	release4()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("recovered stale hook file left behind: %v", err)
	}
}

func TestAgyWorkspaceHookChildProcess(t *testing.T) {
	dir := os.Getenv("AGY_TEST_HOOK_CHILD_DIR")
	if dir == "" {
		return
	}
	release, err := agyHoldToolModeHook(dir, "mcp_only")
	switch os.Getenv("AGY_TEST_HOOK_CHILD_ACTION") {
	case "expect-locked":
		if err == nil {
			release()
			t.Fatal("second process acquired active workspace hook")
		}
	case "crash":
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Simulate backend death without running the release callback.
	default:
		t.Fatal("unknown child action")
	}
}

func TestAgyWorkspaceHookCrossProcessAndCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".agents", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n  \"user-hook\": {\"enabled\": true}\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	child := func(action string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgyWorkspaceHookChildProcess$")
		cmd.Env = append(os.Environ(), "AGY_TEST_HOOK_CHILD_DIR="+dir, "AGY_TEST_HOOK_CHILD_ACTION="+action)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child %s failed: %v: %s", action, err, out)
		}
	}
	release, err := agyHoldToolModeHook(dir, "mcp_only")
	if err != nil {
		t.Fatal(err)
	}
	child("expect-locked")
	release()
	child("crash")
	release, err = agyHoldToolModeHook(dir, "mcp_only")
	if err != nil {
		t.Fatalf("recover after crashed holder: %v", err)
	}
	release()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("original hooks not recovered: %v, %q", err, got)
	}
}

func TestAgyWorkspaceHookRejectsSymlinkedAgentsDir(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, ".agents")); err != nil {
		t.Fatal(err)
	}
	if _, err := agyHoldToolModeHook(dir, "mcp_only"); err == nil {
		t.Fatal("symlinked .agents directory was accepted")
	}
	if _, err := os.Lstat(filepath.Join(dir, ".agents")); err != nil {
		t.Fatalf("user symlink was changed: %v", err)
	}
}

func TestAgyWorkspaceHookRejectsAncestorHookFile(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "project")
	if err := os.MkdirAll(filepath.Join(parent, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".agents", "hooks.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agyHoldToolModeHook(child, "mcp_only"); err == nil {
		t.Fatal("nested AGY workspace accepted a parent hook")
	}
}

func TestAgyToolModeValidationAndFingerprint(t *testing.T) {
	for _, mode := range []string{"mcp_only", "hybrid"} {
		if got, err := agyToolMode(mode); err != nil || got != mode {
			t.Fatalf("mode %q = %q, %v", mode, got, err)
		}
	}
	if _, err := agyToolMode("native_everything"); err == nil {
		t.Fatal("unknown tool mode accepted")
	}
	if agyToolModeFingerprint("{}", "mcp_only") == agyToolModeFingerprint("{}", "hybrid") {
		t.Fatal("different tool policies reused the same mount fingerprint")
	}
}
