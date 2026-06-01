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

// Regression for the spinner-cycling hang: agy leaves a "⣯ Generating…" status
// line cycling above its idle "> " input box even after the turn is done and the
// answer is printed. The completion loop compares pane stability, and the
// cycling Braille glyph used to mutate the raw bytes every ~100ms so the pane
// never looked "stable for 1.2s" — the turn hung forever even though the answer
// was right there. agySpinnerStableKey must normalize that line out so two
// frames that differ ONLY by the spinner produce the same key.
//
// The two panes below are the real captures from the stuck routing step
// (mlp-agy-cli-int-…-553dcce4), 1.5s apart — identical except the spinner glyph.
func TestAgySpinnerStableKeyIgnoresCyclingSpinner(t *testing.T) {
	frame := func(glyph string) string {
		return strings.Join([]string{
			"● api-bridge/execute_shell_command(Find json files) (ctrl+o to expand)",
			"",
			"  {",
			"  \"selected_route_id\": \"password-found\",",
			"  \"reasoning\": \"Based on credentials.json, 'has_password' is true.\"",
			"  }",
			glyph + " Generating...",
			"└ Tip: Start your message with ! to run a shell command.",
			"───────────────────────────────────────────────",
			">",
			"───────────────────────────────────────────────",
		}, "\n")
	}
	a := frame("⣯")
	b := frame("⣷")
	if a == b {
		t.Fatal("test setup error: frames should differ by the spinner glyph")
	}
	if agySpinnerStableKey(a) != agySpinnerStableKey(b) {
		t.Fatalf("agySpinnerStableKey must be identical for frames differing only by the cycling spinner;\nA=%q\nB=%q", agySpinnerStableKey(a), agySpinnerStableKey(b))
	}
	// The real answer must still be present in the key (we only strip the spinner).
	if !strings.Contains(agySpinnerStableKey(a), "password-found") {
		t.Fatalf("stable key dropped real content; got %q", agySpinnerStableKey(a))
	}
	// And the ready prompt must be detected as ready (so the loop reaches the
	// stability check at all).
	if !hasAgyReadyPrompt(strings.ToLower(a)) {
		t.Fatal("hasAgyReadyPrompt should be true for a finished pane with a visible '> ' box")
	}
}
