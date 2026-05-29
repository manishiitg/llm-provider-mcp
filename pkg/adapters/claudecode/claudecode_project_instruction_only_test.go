package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestBuildClaudeArgsProjectInstructionOnly covers WithProjectInstructionOnly:
// by default the prompt is injected via --system-prompt-file (and also
// projected to CLAUDE.md); with the flag on it is carried solely by CLAUDE.md;
// and when the CLAUDE.md projection cannot run the adapter falls back to
// --system-prompt-file so the prompt is never dropped.
func TestBuildClaudeArgsProjectInstructionOnly(t *testing.T) {
	const promptBody = "ORCHESTRATOR SYSTEM PROMPT BODY"

	t.Run("default: --system-prompt-file present and CLAUDE.md also written", func(t *testing.T) {
		adapter := NewClaudeCodeInteractiveAdapter("claude-sonnet-4-6", &MockLogger{})
		dir := t.TempDir()
		opts := &llmtypes.CallOptions{}
		WithWorkingDir(dir)(opts)

		args, tempFiles, err := adapter.buildClaudeArgs(opts, "", promptBody)
		if err != nil {
			t.Fatalf("buildClaudeArgs error = %v", err)
		}
		defer removeFiles(tempFiles)

		if argValue(args, "--system-prompt-file") == "" {
			t.Fatalf("default mode must pass --system-prompt-file, args=%v", args)
		}
		body, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
		if err != nil {
			t.Fatalf("default mode should also write CLAUDE.md: %v", err)
		}
		if !strings.Contains(string(body), promptBody) {
			t.Fatalf("CLAUDE.md missing prompt body, got %q", string(body))
		}
	})

	t.Run("project-instruction-only: drops --system-prompt-file, keeps CLAUDE.md", func(t *testing.T) {
		adapter := NewClaudeCodeInteractiveAdapter("claude-sonnet-4-6", &MockLogger{})
		dir := t.TempDir()
		opts := &llmtypes.CallOptions{}
		WithWorkingDir(dir)(opts)
		WithProjectInstructionOnly(true)(opts)

		args, tempFiles, err := adapter.buildClaudeArgs(opts, "", promptBody)
		if err != nil {
			t.Fatalf("buildClaudeArgs error = %v", err)
		}
		defer removeFiles(tempFiles)

		if containsArg(args, "--system-prompt-file") {
			t.Fatalf("project-instruction-only must NOT pass --system-prompt-file, args=%v", args)
		}
		body, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
		if err != nil {
			t.Fatalf("project-instruction-only must still write CLAUDE.md: %v", err)
		}
		if !strings.Contains(string(body), promptBody) {
			t.Fatalf("CLAUDE.md missing prompt body, got %q", string(body))
		}
	})

	t.Run("project-instruction-only with no working dir: falls back to --system-prompt-file", func(t *testing.T) {
		adapter := NewClaudeCodeInteractiveAdapter("claude-sonnet-4-6", &MockLogger{})
		opts := &llmtypes.CallOptions{}
		// No working dir -> CLAUDE.md projection is a no-op, so the flag must
		// not strand the session without a prompt.
		WithProjectInstructionOnly(true)(opts)

		args, tempFiles, err := adapter.buildClaudeArgs(opts, "", promptBody)
		if err != nil {
			t.Fatalf("buildClaudeArgs error = %v", err)
		}
		defer removeFiles(tempFiles)

		if argValue(args, "--system-prompt-file") == "" {
			t.Fatalf("must fall back to --system-prompt-file when CLAUDE.md projection is skipped, args=%v", args)
		}
	})

	t.Run("project-instruction-only off explicitly behaves like default", func(t *testing.T) {
		adapter := NewClaudeCodeInteractiveAdapter("claude-sonnet-4-6", &MockLogger{})
		dir := t.TempDir()
		opts := &llmtypes.CallOptions{}
		WithWorkingDir(dir)(opts)
		WithProjectInstructionOnly(false)(opts)

		args, tempFiles, err := adapter.buildClaudeArgs(opts, "", promptBody)
		if err != nil {
			t.Fatalf("buildClaudeArgs error = %v", err)
		}
		defer removeFiles(tempFiles)

		if argValue(args, "--system-prompt-file") == "" {
			t.Fatalf("explicit false must keep --system-prompt-file, args=%v", args)
		}
	})
}
