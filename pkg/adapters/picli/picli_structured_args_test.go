package picli

import (
	"reflect"
	"testing"
)

func has(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestBuildPiStructuredArgs pins the session-continuity + containment argv
// shape. The load-bearing invariant is that BOTH a fresh turn and a resume
// turn pass --session-id (only the value differs) — that symmetry is what
// lets turn 2 recall turn 1 instead of starting a blank session.
func TestBuildPiStructuredArgs(t *testing.T) {
	t.Run("full bridge-only turn with skills", func(t *testing.T) {
		got := buildPiStructuredArgs("google", "gemini-3.7-flash", "sess-1", true, true, "mcp-ext", true, "/work/.pi/skills")
		want := []string{
			"--print", "--mode", "json",
			"--provider", "google", "--model", "gemini-3.7-flash",
			"--session-id", "sess-1",
			"--no-builtin-tools",
			"-e", "mcp-ext",
			"--approve",
			"--skill", "/work/.pi/skills",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("argv mismatch:\n got=%v\nwant=%v", got, want)
		}
	})

	// Without these, pi silently runs whatever provider/model its own local
	// settings last used -- a resolved google/gemini-3.7-flash turn actually
	// executed against amazon-bedrock/claude-opus-4-6 on a machine where pi
	// had been used with Bedrock before.
	t.Run("provider and model are always passed", func(t *testing.T) {
		got := buildPiStructuredArgs("zai", "glm-5.3", "s", false, false, "", false, "")
		provIdx := indexOfPi(got, "--provider")
		if provIdx == -1 || got[provIdx+1] != "zai" {
			t.Fatalf("expected --provider zai, got %v", got)
		}
		modelIdx := indexOfPi(got, "--model")
		if modelIdx == -1 || got[modelIdx+1] != "glm-5.3" {
			t.Fatalf("expected --model glm-5.3, got %v", got)
		}
	})

	t.Run("fresh and resume both carry --session-id (symmetry)", func(t *testing.T) {
		fresh := buildPiStructuredArgs("google", "gemini-3.7-flash", "minted-id", false, false, "", false, "")
		resume := buildPiStructuredArgs("google", "gemini-3.7-flash", "prior-id", false, false, "", false, "")
		for _, tc := range []struct {
			name string
			args []string
			id   string
		}{{"fresh", fresh, "minted-id"}, {"resume", resume, "prior-id"}} {
			idx := indexOfPi(tc.args, "--session-id")
			if idx == -1 || tc.args[idx+1] != tc.id {
				t.Errorf("%s: expected --session-id %q, got %v", tc.name, tc.id, tc.args)
			}
		}
	})

	t.Run("bridge-only without mcp config: --no-builtin-tools but no -e", func(t *testing.T) {
		got := buildPiStructuredArgs("google", "gemini-3.7-flash", "s", true, false, "", true, "")
		if !has(got, "--no-builtin-tools") {
			t.Errorf("expected --no-builtin-tools, got %v", got)
		}
		if has(got, "-e") {
			t.Errorf("no mcp config => no -e, got %v", got)
		}
	})

	t.Run("no working dir, no skills: no --approve, no --skill", func(t *testing.T) {
		got := buildPiStructuredArgs("google", "gemini-3.7-flash", "s", false, false, "", false, "")
		if has(got, "--approve") || has(got, "--skill") {
			t.Errorf("expected neither --approve nor --skill, got %v", got)
		}
	})
}

func indexOfPi(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
