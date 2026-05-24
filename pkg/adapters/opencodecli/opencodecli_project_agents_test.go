package opencodecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestOpenCodeWriteRestoredAGENTSMDLifecycleNoPriorContent exercises
// the AGENTS.md feature-flag wiring at the helper level: in OpenCode we
// reuse writeOpenCodeRestoredFile (already used for opencode.jsonc), so
// this test confirms that the underlying helper has the lifecycle
// guarantees the WriteProjectInstructionFile option depends on
// (create-on-write, remove-on-cleanup when no prior file).
func TestOpenCodeWriteRestoredAGENTSMDLifecycleNoPriorContent(t *testing.T) {
	tmp := t.TempDir()
	prompt := "Use 4-space indentation.\nRun lint before submitting."
	body := []byte("<!-- mlp-session-instructions -->\n\n" + prompt)

	cleanup, err := writeOpenCodeRestoredFile(filepath.Join(tmp, "AGENTS.md"), body)
	if err != nil {
		t.Fatalf("writeOpenCodeRestoredFile: %v", err)
	}

	path := filepath.Join(tmp, "AGENTS.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected AGENTS.md after write: %v", err)
	}
	if !strings.Contains(string(got), prompt) {
		t.Errorf("AGENTS.md must contain the system prompt body; got:\n%s", got)
	}
	if !strings.Contains(string(got), "mlp-session-instructions") {
		t.Errorf("AGENTS.md must include the orchestrator marker so future auditors can tell it's not human-authored; got:\n%s", got)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove AGENTS.md that we created from nothing; stat err=%v", err)
	}
}

// TestOpenCodeWriteRestoredAGENTSMDRestoresOperatorContent guards the
// promise the WriteProjectInstructionFile docs make: if the operator
// already had AGENTS.md, cleanup must restore it byte-for-byte instead
// of removing it. Without this guard the OpenCode adapter would
// silently destroy user-owned project instructions on every session
// where the flag is enabled.
func TestOpenCodeWriteRestoredAGENTSMDRestoresOperatorContent(t *testing.T) {
	tmp := t.TempDir()
	operatorContent := []byte("# Operator's pre-existing AGENTS.md\n\nFollow the team's lint policy.\n")
	path := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(path, operatorContent, 0o600); err != nil {
		t.Fatalf("seed pre-existing AGENTS.md: %v", err)
	}

	sessionBody := []byte("<!-- mlp-session-instructions -->\n\nsession-only system prompt")
	cleanup, err := writeOpenCodeRestoredFile(path, sessionBody)
	if err != nil {
		t.Fatalf("writeOpenCodeRestoredFile with pre-existing AGENTS.md: %v", err)
	}

	mid, _ := os.ReadFile(path)
	if strings.Contains(string(mid), "Operator's pre-existing") {
		t.Fatal("mid-session, the operator's pre-existing AGENTS.md content should NOT be visible — our session prompt is installed")
	}
	if !strings.Contains(string(mid), "session-only system prompt") {
		t.Errorf("mid-session, the session prompt should be visible; got:\n%s", mid)
	}

	cleanup()
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("after cleanup, AGENTS.md should exist (restored): %v", err)
	}
	if string(restored) != string(operatorContent) {
		t.Errorf("cleanup must restore pre-existing AGENTS.md byte-for-byte\n  want: %q\n  got:  %q", operatorContent, restored)
	}
}

// TestWriteOpenCodeDenyBuiltinPluginLifecycleNoPriorContent covers
// the fresh-workspace case: no .opencode/ exists, the plugin file is
// dropped at .opencode/plugins/deny-builtin.js with the JS that throws
// on built-in tool names, cleanup removes it (plus the dirs we created
// when empty).
func TestWriteOpenCodeDenyBuiltinPluginLifecycleNoPriorContent(t *testing.T) {
	tmp := t.TempDir()
	cleanup, err := writeOpenCodeDenyBuiltinPlugin(tmp)
	if err != nil {
		t.Fatalf("writeOpenCodeDenyBuiltinPlugin: %v", err)
	}

	pluginPath := filepath.Join(tmp, ".opencode", "plugins", "deny-builtin.js")
	body, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("expected deny-builtin.js after write: %v", err)
	}
	if !strings.Contains(string(body), "export default") {
		t.Errorf("plugin file must use ES-module export default per opencode plugin schema; got:\n%s", body)
	}
	if !strings.Contains(string(body), "tool.execute.before") {
		t.Errorf("plugin file must hook tool.execute.before; got:\n%s", body)
	}
	// Built-in tool names must be in the deny set. If opencode renames
	// any of these in a future release, this test forces an update.
	for _, tool := range []string{"read", "write", "edit", "bash", "grep", "webfetch"} {
		if !strings.Contains(string(body), `"`+tool+`"`) {
			t.Errorf("plugin file must include built-in %q in the deny set; got:\n%s", tool, body)
		}
	}
	if !strings.Contains(string(body), "throw new Error") {
		t.Errorf("plugin file must throw on denied tool calls (opencode's documented deny mechanism); got:\n%s", body)
	}

	cleanup()
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove deny-builtin.js; stat err=%v", err)
	}
}

// TestWriteOpenCodeDenyBuiltinPluginRestoresOperatorContent guards
// the byte-restore promise: if the operator already had their own
// .opencode/plugins/deny-builtin.js, cleanup must restore it.
func TestWriteOpenCodeDenyBuiltinPluginRestoresOperatorContent(t *testing.T) {
	tmp := t.TempDir()
	pluginsDir := filepath.Join(tmp, ".opencode", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("seed .opencode/plugins dir: %v", err)
	}
	operatorBody := []byte("// operator's own deny-builtin plugin\nexport default async () => ({});\n")
	pluginPath := filepath.Join(pluginsDir, "deny-builtin.js")
	if err := os.WriteFile(pluginPath, operatorBody, 0o600); err != nil {
		t.Fatalf("seed operator deny-builtin.js: %v", err)
	}

	cleanup, err := writeOpenCodeDenyBuiltinPlugin(tmp)
	if err != nil {
		t.Fatalf("writeOpenCodeDenyBuiltinPlugin: %v", err)
	}

	mid, _ := os.ReadFile(pluginPath)
	if string(mid) == string(operatorBody) {
		t.Error("mid-session, the operator's plugin should NOT be visible — our deny plugin is installed")
	}
	if !strings.Contains(string(mid), "mlp-session") {
		t.Errorf("mid-session, our orchestrator deny plugin must be installed; got:\n%s", mid)
	}

	cleanup()
	restored, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("after cleanup, deny-builtin.js should exist (restored): %v", err)
	}
	if string(restored) != string(operatorBody) {
		t.Errorf("cleanup must restore operator deny-builtin.js byte-for-byte\n  want: %q\n  got:  %q", operatorBody, restored)
	}
}

// TestWriteOpenCodeDenyBuiltinPluginEmptyWorkingDirNoOp guards
// against polluting the orchestrator's own cwd when the caller forgot
// to set MetadataKeyWorkingDir.
func TestWriteOpenCodeDenyBuiltinPluginEmptyWorkingDirNoOp(t *testing.T) {
	cleanup, err := writeOpenCodeDenyBuiltinPlugin("")
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil even for empty workingDir (no-op cleanup)")
	}
	cleanup() // must not panic
	if _, err := os.Stat(".opencode"); err == nil {
		t.Errorf(".opencode dir must NOT be created in process cwd when workingDir is empty")
		_ = os.RemoveAll(".opencode")
	}
}

// TestOpenCodeWriteProjectInstructionFileOptionThreadsThroughMetadata
// asserts the public option puts the bool on the metadata under the
// expected key so the structured adapter's `if enabled, _ :=
// opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile].(bool);
// enabled` branch actually fires.
func TestOpenCodeWriteProjectInstructionFileOptionThreadsThroughMetadata(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithWriteProjectInstructionFile(true)(opts)
	if opts.Metadata == nil || opts.Metadata.Custom == nil {
		t.Fatal("WithWriteProjectInstructionFile must initialize metadata")
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile].(bool)
	if !ok || !enabled {
		t.Errorf("expected MetadataKeyWriteProjectInstructionFile=true on metadata; got %v ok=%v", opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile], ok)
	}
}
