package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteClaudeCodeProjectInstructionFileLifecycle covers the
// invariants of the project-instruction writer: file lands at
// <workdir>/CLAUDE.md with the system prompt body, includes the
// orchestrator marker comment, has restrictive perms, and a second
// write to the same workdir overwrites in place (no nonce, no
// per-session uniqueness).
func TestWriteClaudeCodeProjectInstructionFileLifecycle(t *testing.T) {
	tmp := t.TempDir()
	prompt := "Always run gofmt before committing.\nNever push to main."

	path1, err := writeClaudeCodeProjectInstructionFile(tmp, prompt, false)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	expected := filepath.Join(tmp, "CLAUDE.md")
	if path1 != expected {
		t.Errorf("rule path %q should be %q (workdir-root CLAUDE.md)", path1, expected)
	}

	body, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("read rule file: %v", err)
	}
	if !strings.Contains(string(body), prompt) {
		t.Errorf("rule file must contain the system prompt body; got:\n%s", body)
	}
	if !strings.Contains(string(body), "mlp-session-instructions") {
		t.Errorf("rule file must include the orchestrator marker comment so future auditors can tell it's not human-authored; got:\n%s", body)
	}

	info, _ := os.Stat(path1)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("rule file should be 0o600 (operator-private); got %o", mode)
	}

	// Second write must hit the same fixed path — one chat owns the
	// workdir at a time, so CLAUDE.md is canonical and re-written in
	// place rather than disambiguated.
	path2, err := writeClaudeCodeProjectInstructionFile(tmp, prompt, false)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if path1 != path2 {
		t.Errorf("CLAUDE.md path must be stable across writes; got %q then %q", path1, path2)
	}
}

// TestWriteClaudeCodeProjectInstructionFileByteRestore verifies the
// OPT-IN byte-restore contract (restorePrior=true): when CLAUDE.md exists,
// the prior bytes are staged in claudeProjectFileRestores so removeFiles
// writes them back. When CLAUDE.md does not exist, no restore entry is
// registered so removeFiles deletes the file we created.
func TestWriteClaudeCodeProjectInstructionFileByteRestore(t *testing.T) {
	tmp := t.TempDir()
	priorBody := "operator-owned CLAUDE.md content\n"
	path := filepath.Join(tmp, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(priorBody), 0o600); err != nil {
		t.Fatalf("seed prior CLAUDE.md: %v", err)
	}

	written, err := writeClaudeCodeProjectInstructionFile(tmp, "session prompt", true)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written != path {
		t.Fatalf("written path mismatch: %q vs %q", written, path)
	}

	// Prior bytes must be registered for restore.
	restored, ok := claudeProjectFileRestores.Load(path)
	if !ok {
		t.Fatalf("prior bytes were not registered for byte-restore")
	}
	if bs, _ := restored.([]byte); string(bs) != priorBody {
		t.Errorf("registered restore payload mismatch: got %q want %q", bs, priorBody)
	}

	// Simulate cleanup via the existing removeFiles path.
	removeFiles([]string{path})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("CLAUDE.md should still exist (restored); got %v", err)
	}
	if string(got) != priorBody {
		t.Errorf("CLAUDE.md not restored to prior bytes; got %q want %q", got, priorBody)
	}

	// Second pass: no prior file, removeFiles must delete what we wrote.
	tmp2 := t.TempDir()
	path2 := filepath.Join(tmp2, "CLAUDE.md")
	if _, err := writeClaudeCodeProjectInstructionFile(tmp2, "another session", true); err != nil {
		t.Fatalf("write fresh: %v", err)
	}
	removeFiles([]string{path2})
	if _, err := os.Stat(path2); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md must be removed when no prior content existed; stat err=%v", err)
	}
}

// TestWriteClaudeCodeProjectInstructionFileNoRestoreDefault verifies the
// DEFAULT (restorePrior=false): even when a pre-existing operator CLAUDE.md
// is present, no restore entry is registered and removeFiles deletes the
// freshly-written file rather than resurrecting the prior content. This is
// the "always write fresh, never restore" behavior.
func TestWriteClaudeCodeProjectInstructionFileNoRestoreDefault(t *testing.T) {
	tmp := t.TempDir()
	priorBody := "operator-owned CLAUDE.md content\n"
	path := filepath.Join(tmp, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(priorBody), 0o600); err != nil {
		t.Fatalf("seed prior CLAUDE.md: %v", err)
	}

	written, err := writeClaudeCodeProjectInstructionFile(tmp, "session prompt", false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written != path {
		t.Fatalf("written path mismatch: %q vs %q", written, path)
	}

	// No restore entry may be registered when restorePrior is false.
	if _, ok := claudeProjectFileRestores.Load(path); ok {
		t.Fatalf("default (no-restore) must NOT register prior bytes for restore")
	}

	// The freshly written body must reflect the new session prompt, not
	// the operator's prior content.
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), priorBody) {
		t.Errorf("fresh write must overwrite prior operator content; got:\n%s", body)
	}

	// Cleanup must DELETE the file — prior content is gone for good.
	removeFiles([]string{path})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("default cleanup must delete the written file, not restore prior; stat err=%v", err)
	}
}

// TestWriteClaudeCodeProjectInstructionFileEmptyWorkingDirNoOps guards
// against the adapter accidentally writing CLAUDE.md to the process cwd
// when the caller forgot to set MetadataKeyWorkingDir. An empty
// workingDir must short-circuit cleanly with no side effects.
func TestWriteClaudeCodeProjectInstructionFileEmptyWorkingDirNoOps(t *testing.T) {
	path, err := writeClaudeCodeProjectInstructionFile("", "anything", false)
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if path != "" {
		t.Errorf("empty workingDir should return empty path; got %q", path)
	}
	if _, err := os.Stat("CLAUDE.md"); err == nil {
		t.Errorf("CLAUDE.md must NOT be created in process cwd when workingDir is empty")
		_ = os.Remove("CLAUDE.md") // best-effort cleanup if the assert fired
	}
}
