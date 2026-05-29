package geminicli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteGeminiProjectInstructionFileLifecycleNoPriorContent covers
// the fresh-workspace case: no GEMINI.md exists, the writer creates one
// containing the system prompt + orchestrator marker, cleanup removes it.
func TestWriteGeminiProjectInstructionFileLifecycleNoPriorContent(t *testing.T) {
	tmp := t.TempDir()
	prompt := "Prefer two-space indent.\nRun goimports before committing."

	cleanup, err := writeGeminiProjectInstructionFile(tmp, prompt, false)
	if err != nil {
		t.Fatalf("writeGeminiProjectInstructionFile: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup must be non-nil")
	}

	path := filepath.Join(tmp, "GEMINI.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected GEMINI.md after write: %v", err)
	}
	if !strings.Contains(string(body), prompt) {
		t.Errorf("GEMINI.md must contain the system prompt body; got:\n%s", body)
	}
	if !strings.Contains(string(body), "mlp-session-instructions") {
		t.Errorf("GEMINI.md must include the orchestrator marker so future auditors can tell it's not human-authored; got:\n%s", body)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup must remove GEMINI.md that we created from nothing; stat err=%v", err)
	}
}

// TestWriteGeminiProjectInstructionFileRestoresOperatorContent guards
// the explicit promise: if the operator already had GEMINI.md, cleanup
// must restore it byte-for-byte instead of deleting it. Without this
// guard the adapter would silently destroy user-owned project context
// on every successful session.
func TestWriteGeminiProjectInstructionFileRestoresOperatorContent(t *testing.T) {
	tmp := t.TempDir()
	operatorContent := []byte("# Operator's pre-existing GEMINI.md\n\nUse the in-house style guide.\n")
	path := filepath.Join(tmp, "GEMINI.md")
	if err := os.WriteFile(path, operatorContent, 0o600); err != nil {
		t.Fatalf("seed pre-existing GEMINI.md: %v", err)
	}

	cleanup, err := writeGeminiProjectInstructionFile(tmp, "session-only system prompt", true)
	if err != nil {
		t.Fatalf("writeGeminiProjectInstructionFile with pre-existing GEMINI.md: %v", err)
	}

	mid, _ := os.ReadFile(path)
	if strings.Contains(string(mid), "Operator's pre-existing") {
		t.Fatal("mid-session, the operator's pre-existing GEMINI.md content should NOT be visible — our session prompt is installed")
	}
	if !strings.Contains(string(mid), "session-only system prompt") {
		t.Errorf("mid-session, the session prompt should be visible; got:\n%s", mid)
	}

	cleanup()
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("after cleanup, GEMINI.md should exist (restored): %v", err)
	}
	if string(restored) != string(operatorContent) {
		t.Errorf("cleanup must restore pre-existing GEMINI.md byte-for-byte\n  want: %q\n  got:  %q", operatorContent, restored)
	}
}

// TestWriteGeminiProjectInstructionFileNoRestoreDefault verifies the
// DEFAULT (restorePrior=false): even with a pre-existing operator
// GEMINI.md, cleanup DELETES the freshly-written file rather than
// restoring the prior content — the "always write fresh, never restore"
// behavior.
func TestWriteGeminiProjectInstructionFileNoRestoreDefault(t *testing.T) {
	tmp := t.TempDir()
	operatorContent := []byte("# Operator's pre-existing GEMINI.md\n")
	path := filepath.Join(tmp, "GEMINI.md")
	if err := os.WriteFile(path, operatorContent, 0o600); err != nil {
		t.Fatalf("seed pre-existing GEMINI.md: %v", err)
	}

	cleanup, err := writeGeminiProjectInstructionFile(tmp, "session-only system prompt", false)
	if err != nil {
		t.Fatalf("writeGeminiProjectInstructionFile: %v", err)
	}

	mid, _ := os.ReadFile(path)
	if strings.Contains(string(mid), "Operator's pre-existing") {
		t.Fatal("mid-session, operator content must be overwritten by the session prompt")
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("default cleanup must DELETE the file, not restore prior content; stat err=%v", err)
	}
}

// TestWriteGeminiProjectInstructionFileEmptyWorkingDirNoOps guards
// against the adapter accidentally writing GEMINI.md into the
// orchestrator's own cwd when the caller forgot to set
// MetadataKeyWorkingDir. An empty workingDir must short-circuit cleanly.
func TestWriteGeminiProjectInstructionFileEmptyWorkingDirNoOps(t *testing.T) {
	cleanup, err := writeGeminiProjectInstructionFile("", "anything", false)
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup should be non-nil even for empty workingDir (no-op cleanup)")
	}
	cleanup() // must not panic
	if _, err := os.Stat("GEMINI.md"); err == nil {
		t.Errorf("GEMINI.md must NOT be created in process cwd when workingDir is empty")
		_ = os.Remove("GEMINI.md")
	}
}
