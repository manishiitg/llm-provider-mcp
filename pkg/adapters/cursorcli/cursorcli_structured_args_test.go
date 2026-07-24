package cursorcli

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

// TestBuildCursorStructuredArgs pins the structured argv shape, including the
// native --resume flag and the bridge-only "--mode ask" containment.
func TestBuildCursorStructuredArgs(t *testing.T) {
	t.Run("full turn with resume", func(t *testing.T) {
		got := buildCursorStructuredArgs("/work", "gpt-5", "ask", "danger", true, "cur-sess-1", "hello")
		want := []string{
			"--print",
			"--output-format", "stream-json",
			"--stream-partial-output",
			"--trust",
			"--force",
			"--workspace", "/work",
			"--model", "gpt-5",
			"--mode", "ask",
			"--sandbox", "danger",
			"--approve-mcps",
			"--resume", "cur-sess-1",
			"hello",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("argv mismatch:\n got=%v\nwant=%v", got, want)
		}
	})

	t.Run("fresh turn: no --resume", func(t *testing.T) {
		got := buildCursorStructuredArgs("/work", "gpt-5", "", "", false, "", "hi")
		if has(got, "--resume") {
			t.Errorf("fresh turn must not carry --resume, got %v", got)
		}
		if has(got, "--mode") {
			t.Errorf("no mode => no --mode flag, got %v", got)
		}
		// The prompt is always the final positional arg.
		if got[len(got)-1] != "hi" {
			t.Errorf("prompt must be the last arg, got %v", got)
		}
	})
}
