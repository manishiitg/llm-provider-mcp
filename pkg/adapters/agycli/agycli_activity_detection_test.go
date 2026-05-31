package agycli

import (
	"strings"
	"testing"
)

// Agy 1.0.3 renders its in-progress status with a leading animated Braille
// spinner glyph, e.g. "⣾ Generating…". A HasPrefix("generating") check misses
// it because of the leading glyph, which used to make hasAgyActivity /
// hasAgyLiveGenerationActivity report "no activity" mid-generation — so the
// response loop could complete early and capture "Generating…" as the answer.
func TestAgyActivityDetectsSpinnerPrefixedStatus(t *testing.T) {
	active := []string{
		"⣾ Generating...",
		"⠋ Generating…",
		"⣽ Thinking...",
		"  ⣻ Working on it",
		"generating...",     // no spinner, still active
		"calling some_tool", // bare keyword
	}
	for _, line := range active {
		if !hasAgyActivity(line) {
			t.Errorf("hasAgyActivity(%q) = false, want true", line)
		}
	}

	idle := []string{
		"",
		"> ",
		"▸ Thought for 5s, 282 tokens", // completed thought, not active
		"● Read(/tmp/x.json)",          // completed tool call
		"here is the final answer",
	}
	for _, line := range idle {
		if hasAgyActivity(line) {
			t.Errorf("hasAgyActivity(%q) = true, want false", line)
		}
	}
}

// hasAgyLiveGenerationActivity gates ready-prompt detection: while a spinner is
// animating, the turn must not be considered complete even though agy already
// draws the "> " input box at the bottom of the pane.
func TestAgyLiveGenerationActivityDetectsSpinner(t *testing.T) {
	// Mirrors the real failing pane: spinner + "Generating…" above a ready "> ".
	generating := strings.ToLower(strings.Join([]string{
		"○ api-bridge/slow_contract(Call slow_contract)",
		"⣾ Generating...",
		"└ Tip: Press ctrl+g to open an external editor for long prompts.",
		"────────",
		"> ",
		"────────",
	}, "\n"))
	if !hasAgyLiveGenerationActivity(generating) {
		t.Fatalf("hasAgyLiveGenerationActivity = false for an actively-generating pane; want true")
	}
	if hasAgyReadyPrompt(generating) {
		t.Fatalf("hasAgyReadyPrompt = true while spinner is animating; want false (turn still in progress)")
	}

	// A settled pane (no spinner) with the response and a ready prompt must be
	// recognized as complete.
	done := strings.ToLower(strings.Join([]string{
		"▸ Thought for 5s, 282 tokens",
		"AGY_TOKEN_OK_123",
		"────────",
		"> ",
		"────────",
	}, "\n"))
	if hasAgyLiveGenerationActivity(done) {
		t.Fatalf("hasAgyLiveGenerationActivity = true for a settled pane; want false")
	}
}
