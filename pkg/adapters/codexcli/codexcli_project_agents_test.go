package codexcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteCodexProjectAgentsFileLifecycleNoPriorContent covers the
// fresh-workspace case: no AGENTS.md exists, the writer creates one
// with the system prompt + orchestrator marker, cleanup removes it.
func TestWriteCodexProjectAgentsFileLifecycleNoPriorContent(t *testing.T) {
	tmp := t.TempDir()
	prompt := "Use 4-space indentation.\nRun `cargo test` before committing."

	cleanup, err := writeCodexProjectAgentsFile(tmp, prompt)
	if err != nil {
		t.Fatalf("writeCodexProjectAgentsFile: %v", err)
	}

	path := filepath.Join(tmp, "AGENTS.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected AGENTS.md after write: %v", err)
	}
	if !strings.Contains(string(body), prompt) {
		t.Errorf("AGENTS.md must contain the system prompt body; got:\n%s", body)
	}
	if !strings.Contains(string(body), "mlp-session-instructions") {
		t.Errorf("AGENTS.md must include the orchestrator marker so future auditors can tell it's not human-authored; got:\n%s", body)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove AGENTS.md that we created from nothing; stat err=%v", err)
	}
}

// TestWriteCodexProjectAgentsFileRestoresOperatorContent guards the
// promise the comment makes: if the operator already had AGENTS.md
// in their workspace, cleanup must restore it byte-for-byte instead of
// removing it. Without this guard the adapter would silently destroy
// user-owned project instructions on every successful session.
func TestWriteCodexProjectAgentsFileRestoresOperatorContent(t *testing.T) {
	tmp := t.TempDir()
	operatorContent := []byte("# Operator's pre-existing AGENTS.md\n\nUse the in-house code-style guide.\n")
	path := filepath.Join(tmp, "AGENTS.md")
	if err := os.WriteFile(path, operatorContent, 0o600); err != nil {
		t.Fatalf("seed pre-existing AGENTS.md: %v", err)
	}

	cleanup, err := writeCodexProjectAgentsFile(tmp, "session-only system prompt")
	if err != nil {
		t.Fatalf("writeCodexProjectAgentsFile with pre-existing AGENTS.md: %v", err)
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

// TestWriteCodexProjectAgentsFileEmptyWorkingDirNoOps guards against
// the adapter accidentally writing AGENTS.md into the orchestrator's
// own cwd when the caller forgot to set MetadataKeyWorkingDir.
func TestWriteCodexProjectAgentsFileEmptyWorkingDirNoOps(t *testing.T) {
	cleanup, err := writeCodexProjectAgentsFile("", "anything")
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup should be non-nil even for empty workingDir (no-op cleanup)")
	}
	cleanup() // must not panic
	if _, err := os.Stat("AGENTS.md"); err == nil {
		t.Errorf("AGENTS.md must NOT be created in process cwd when workingDir is empty")
		_ = os.Remove("AGENTS.md")
	}
}
