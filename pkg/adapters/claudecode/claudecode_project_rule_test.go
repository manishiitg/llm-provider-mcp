package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteClaudeCodeProjectRuleFileLifecycle covers the unit-level
// invariants of the opt-in project-rule writer: file lands at
// .claude/rules/mlp-session-<hex>.md with the system prompt body,
// includes the orchestrator marker comment, has restrictive perms,
// and uses a unique nonce so concurrent sessions don't collide.
func TestWriteClaudeCodeProjectRuleFileLifecycle(t *testing.T) {
	tmp := t.TempDir()
	prompt := "Always run gofmt before committing.\nNever push to main."

	path1, err := writeClaudeCodeProjectRuleFile(tmp, prompt)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if path1 == "" {
		t.Fatalf("expected non-empty path with non-empty workingDir")
	}
	expectedDir := filepath.Join(tmp, ".claude", "rules")
	if !strings.HasPrefix(path1, expectedDir+string(filepath.Separator)) {
		t.Errorf("rule path %q should sit under %q", path1, expectedDir)
	}
	base := filepath.Base(path1)
	if !strings.HasPrefix(base, "mlp-session-") || !strings.HasSuffix(base, ".md") {
		t.Errorf("rule filename %q must follow mlp-session-<hex>.md", base)
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

	// Second write with same prompt must produce a DIFFERENT path so
	// concurrent sessions in the same workspace don't fight over the
	// same filename.
	path2, err := writeClaudeCodeProjectRuleFile(tmp, prompt)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if path1 == path2 {
		t.Errorf("two writes must produce unique filenames so concurrent sessions don't collide; both got %q", path1)
	}
}

// TestWriteClaudeCodeProjectRuleFileEmptyWorkingDirNoOps guards against
// the adapter accidentally writing to ".claude/rules/" relative to the
// process cwd when the caller forgot to set MetadataKeyWorkingDir. An
// empty workingDir must short-circuit cleanly with no side effects.
func TestWriteClaudeCodeProjectRuleFileEmptyWorkingDirNoOps(t *testing.T) {
	path, err := writeClaudeCodeProjectRuleFile("", "anything")
	if err != nil {
		t.Errorf("empty workingDir should return nil error; got %v", err)
	}
	if path != "" {
		t.Errorf("empty workingDir should return empty path; got %q", path)
	}
	if _, err := os.Stat(".claude"); err == nil {
		t.Errorf(".claude directory must NOT be created in process cwd when workingDir is empty")
		_ = os.RemoveAll(".claude") // best-effort cleanup if the assert fired
	}
}
