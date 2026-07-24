package codexcli

import (
	"reflect"
	"testing"
)

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// TestBuildCodexStructuredArgs pins the resume-vs-fresh argv shape. The
// load-bearing case is resume: `codex exec resume` rejects subcommand-level
// --profile/--sandbox/-C, so those MUST appear as GLOBAL flags BEFORE "exec".
// A refactor that reorders them (or drops the -c sandbox override) silently
// breaks native resume, which this test catches without touching a CLI.
func TestBuildCodexStructuredArgs(t *testing.T) {
	t.Run("fresh turn: exec with subcommand-level flags after exec", func(t *testing.T) {
		got := buildCodexStructuredArgs("", "prof", "workspace-write", "/work", "gpt-5-codex", []string{"a=b"}, "hello")
		want := []string{
			"exec", "--json", "--skip-git-repo-check",
			"-C", "/work",
			"--model", "gpt-5-codex",
			"--sandbox", "workspace-write",
			"--profile", "prof",
			"-c", "a=b",
			"hello",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("fresh argv mismatch:\n got=%v\nwant=%v", got, want)
		}
	})

	t.Run("resume turn: global --profile and -c sandbox precede exec resume", func(t *testing.T) {
		got := buildCodexStructuredArgs("sess-123", "prof", "danger-full-access", "/work", "gpt-5-codex", []string{"a=b"}, "hello")
		want := []string{
			"--profile", "prof",
			"-c", `sandbox_mode="danger-full-access"`,
			"exec", "resume", "sess-123", "--json", "--skip-git-repo-check",
			"--model", "gpt-5-codex",
			"-c", "a=b",
			"hello",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resume argv mismatch:\n got=%v\nwant=%v", got, want)
		}
		// Explicit ordering invariant (independent of the exact slice above):
		// every global flag must come before the exec subcommand.
		execIdx := indexOf(got, "exec")
		if p := indexOf(got, "--profile"); p == -1 || p > execIdx {
			t.Errorf("--profile (%d) must precede exec (%d)", p, execIdx)
		}
		if got[execIdx+1] != "resume" || got[execIdx+2] != "sess-123" {
			t.Errorf("expected `exec resume <id>` sequence, got %v", got[execIdx:execIdx+3])
		}
	})

	t.Run("resume without a session profile: no --profile, still -c sandbox before exec", func(t *testing.T) {
		got := buildCodexStructuredArgs("sess-123", "", "workspace-write", "/work", "codex-cli", nil, "hi")
		if indexOf(got, "--profile") != -1 {
			t.Errorf("no profile => no --profile flag, got %v", got)
		}
		cIdx, execIdx := indexOf(got, "-c"), indexOf(got, "exec")
		if cIdx == -1 || cIdx > execIdx {
			t.Errorf("-c sandbox_mode must precede exec: -c=%d exec=%d (%v)", cIdx, execIdx, got)
		}
		// model "codex-cli" is the placeholder id and must be omitted.
		if indexOf(got, "--model") != -1 {
			t.Errorf("placeholder model codex-cli must not add --model, got %v", got)
		}
	})
}
