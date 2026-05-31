package cursorcli

import (
	"testing"
)

// The Cursor CLI renders its in-progress status with a leading animated Braille
// spinner glyph, e.g. "⣾ Generating…". A HasPrefix("generating") check misses it
// because of the leading glyph, which made hasCursorActivity report "no activity"
// mid-generation — so the response loop could complete early and capture
// "Generating…" as the answer (and a never-delivered prompt could hang).
func TestCursorActivityDetectsSpinnerPrefixedStatus(t *testing.T) {
	active := []string{
		"⣾ Generating...",
		"⠋ Generating…",
		"⣽ Thinking...",
		"  ⣻ Working on it",
		"generating...",     // no spinner, still active
		"calling some_tool", // bare keyword
	}
	for _, line := range active {
		if !hasCursorActivity(line) {
			t.Errorf("hasCursorActivity(%q) = false, want true", line)
		}
	}

	idle := []string{
		"",
		"> ",
		"▸ Thought for 5s",    // completed thought, not active
		"● Read(/tmp/x.json)", // completed tool call
		"here is the final answer",
	}
	for _, line := range idle {
		if hasCursorActivity(line) {
			t.Errorf("hasCursorActivity(%q) = true, want false", line)
		}
	}
}
