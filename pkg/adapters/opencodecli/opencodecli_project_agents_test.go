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

// TestBuildOpenCodeProjectConfigJSONDeniesBuiltinTools covers the
// replacement for the old JS plugin: when denyBuiltins=true, the
// generated opencode.jsonc must contain a "tools" block setting every
// filesystem/shell/network built-in to false, so the model is forced to
// reach those capabilities via MCP servers instead. Opencode's
// documented `{"tools": {"read": false, ...}}` mechanism (per
// opencode.ai/docs/config) replaces the earlier .opencode/plugins/
// deny-builtin.js approach, which never reliably fired in practice.
func TestBuildOpenCodeProjectConfigJSONDeniesBuiltinTools(t *testing.T) {
	out, err := buildOpenCodeProjectConfigJSON("", true)
	if err != nil {
		t.Fatalf("buildOpenCodeProjectConfigJSON: %v", err)
	}

	body := string(out)
	if !strings.Contains(body, `"tools"`) {
		t.Errorf("expected a tools block in generated opencode.jsonc; got:\n%s", body)
	}
	// "apply_patch" not "patch" — opencode docs explicitly call this out
	// as a common gotcha. If the docs change the tool ID, this test
	// forces an update.
	for _, tool := range []string{"read", "write", "edit", "bash", "grep", "glob", "lsp", "apply_patch", "webfetch", "websearch", "task", "skill"} {
		if !strings.Contains(body, `"`+tool+`": false`) {
			t.Errorf("expected %q disabled in tools block; got:\n%s", tool, body)
		}
	}
	if strings.Contains(body, `"patch": false`) {
		t.Errorf("must use 'apply_patch' not 'patch' per opencode docs; got:\n%s", body)
	}
}

// TestBuildOpenCodeProjectConfigJSONMCPOnly confirms the MCP-only path
// (denyBuiltins=false) still emits the merged {"mcp":{...}} shape that
// existing callers rely on — i.e. dropping the deny-plugin didn't
// regress the MCP wiring contract.
func TestBuildOpenCodeProjectConfigJSONMCPOnly(t *testing.T) {
	input := `{"mcpServers":{"bridge":{"command":["echo","hi"]}}}`
	out, err := buildOpenCodeProjectConfigJSON(input, false)
	if err != nil {
		t.Fatalf("buildOpenCodeProjectConfigJSON: %v", err)
	}

	body := string(out)
	if !strings.Contains(body, `"mcp"`) || !strings.Contains(body, `"bridge"`) {
		t.Errorf("MCP-only path must include mcp.bridge server; got:\n%s", body)
	}
	if strings.Contains(body, `"tools"`) {
		t.Errorf("MCP-only path must NOT include tools block when denyBuiltins=false; got:\n%s", body)
	}
	if !strings.Contains(body, `"type": "local"`) {
		t.Errorf("MCP-only path must inject default type=local; got:\n%s", body)
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
