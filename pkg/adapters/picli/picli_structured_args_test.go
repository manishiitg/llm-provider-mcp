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
		got := buildPiStructuredArgs("sess-1", true, true, "mcp-ext", true, "/work/.pi/skills")
		want := []string{
			"--print", "--mode", "json",
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

	t.Run("fresh and resume both carry --session-id (symmetry)", func(t *testing.T) {
		fresh := buildPiStructuredArgs("minted-id", false, false, "", false, "")
		resume := buildPiStructuredArgs("prior-id", false, false, "", false, "")
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
		got := buildPiStructuredArgs("s", true, false, "", true, "")
		if !has(got, "--no-builtin-tools") {
			t.Errorf("expected --no-builtin-tools, got %v", got)
		}
		if has(got, "-e") {
			t.Errorf("no mcp config => no -e, got %v", got)
		}
	})

	t.Run("no working dir, no skills: no --approve, no --skill", func(t *testing.T) {
		got := buildPiStructuredArgs("s", false, false, "", false, "")
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
