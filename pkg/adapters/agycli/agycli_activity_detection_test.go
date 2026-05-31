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

// hasAgyLiveGenerationActivity gates ready-prompt detection: while generation is
// actively in progress, the turn must not be considered complete. The reliable
// live signals are "composing"/"ctrl+c to stop"/"esc to interrupt" and an
// in-flight tool card ("○ …") shown WITHOUT a stable ready input prompt.
func TestAgyLiveGenerationActivityDetectsSpinner(t *testing.T) {
	// A genuinely-live pane: a running tool card and a stop hint, no stable
	// input prompt yet (agy has not drawn the idle "> " box).
	generating := strings.ToLower(strings.Join([]string{
		"○ api-bridge/slow_contract(Call slow_contract)",
		"⣾ Generating...",
		"└ Press ctrl+c to stop",
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

// Regression for the silent hang where a COMPLETED message-sequence turn never
// returned. agy leaves a finished tool card ("○ api-bridge/...(ctrl+o to
// expand)") in the scrollback. With the ready input prompt ("> ") visible and
// the pane byte-stable, that historical "○ " card must NOT be read as live
// generation — otherwise hasAgyReadyPrompt stays false forever and the response
// loop blocks indefinitely, so the step's completion notification never fires.
func TestAgyCompletedToolCardWithReadyPromptIsNotLive(t *testing.T) {
	// Mirrors the real hung pane: a completed tool card + final response text +
	// a stable "> " input box, with NO active spinner near the input.
	pane := strings.ToLower(strings.Join([]string{
		"○ api-bridge/execute_shell_command(Get raw response) (ctrl+o to expand)",
		"  ### Active Session Context",
		"  The login session remains active in the background context on the Dashboard.",
		"────────────────────────────────────────────────────────────",
		"> ",
		"────────────────────────────────────────────────────────────",
	}, "\n"))
	if hasAgyLiveGenerationActivity(pane) {
		t.Fatalf("hasAgyLiveGenerationActivity = true for a completed pane with a historical ○ tool card and a ready prompt; want false")
	}
	if !hasAgyReadyPrompt(pane) {
		t.Fatalf("hasAgyReadyPrompt = false for a completed, stable pane with a visible '> ' prompt; want true (turn is done)")
	}
}
