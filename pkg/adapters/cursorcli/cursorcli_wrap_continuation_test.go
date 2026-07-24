package cursorcli

import (
	"strings"
	"testing"
)

// TestCursorHardWrappedPromptEchoDoesNotLeak is a fast, deterministic tripwire
// for a real bug found live (2026-07-24, real cursor-agent 2026.07.23): a
// single long prompt line that Cursor's TUI word-wraps across several pane
// rows used to leak into the extracted answer IN FULL. The real e2e proof
// (live cursor-agent, before/after) is
// TestCursorCLIRealInteractiveWrappedSingleLineSubmitContract in
// cursorcli_real_contract_test.go — this test does not replace that evidence
// (per this project's standing rule that only real-CLI/real-LLM e2e counts as
// coverage for coding-agent code), it just catches an argv/string-logic
// regression fast without a live CLI.
//
// Root cause: stripCursorEchoedUserPrompt's matcher (cursorPromptLinesEqual)
// requires an exact WHOLE-LINE match. A prompt that is genuinely one long
// logical line, once word-wrapped into several captured pane lines, can never
// satisfy that — none of the fragments equals the full original line — so the
// function gave up entirely (bestLen stayed 0) and returned the text
// unstripped, leaking the ENTIRE echoed prompt (not just a fragment, unlike
// the analogous Codex bug this session also fixed).
func TestCursorHardWrappedPromptEchoDoesNotLeak(t *testing.T) {
	prompt := "This sentence only makes the single Cursor input line long enough to wrap inside the terminal composer and includes a leak fragment in the middle so we can detect a leak. Reply exactly: TOKEN123"
	text := "This sentence only makes the single Cursor input line long enough to wrap inside the terminal composer and\n" +
		"includes a leak fragment in the middle so we can detect a leak. Reply exactly: TOKEN123\n" +
		"\n" +
		"TOKEN123"

	got := stripCursorEchoedUserPrompt(text, prompt)
	if strings.Contains(got, "leak fragment") || strings.Contains(got, "wrap inside") {
		t.Fatalf("wrapped prompt-echo leaked: %q", got)
	}
	if !strings.Contains(got, "TOKEN123") {
		t.Fatalf("real answer was lost while fixing the leak: %q", got)
	}
}

// TestCursorIndentedMultiLinePromptStillStrips guards the pre-existing,
// already-working case (a genuine multi-line prompt where each line matches
// exactly) after adding the word-wrap reconstruction fallback above it.
func TestCursorIndentedMultiLinePromptStillStrips(t *testing.T) {
	prompt := "line one\nline two\nline three"
	text := "line one\nline two\nline three\nThe answer is SIXTY_NINE."

	got := stripCursorEchoedUserPrompt(text, prompt)
	if strings.Contains(got, "line one") || strings.Contains(got, "line two") || strings.Contains(got, "line three") {
		t.Fatalf("multi-line prompt echo leaked: %q", got)
	}
	if !strings.Contains(got, "SIXTY_NINE") {
		t.Fatalf("real answer was lost: %q", got)
	}
}

// TestCursorFullVerbatimEchoIsNotEmptied is a regression test for a second,
// unrelated pre-existing bug found while testing the wrap fix above: when the
// model's answer legitimately reproduces the prompt verbatim (e.g. "reply with
// exactly: TOKEN" tasks), the old code stripped the WHOLE match with no guard
// against it covering 100% of the text — emptying the extraction entirely.
// stripCodexEchoedUserPrompt already had this guard; cursor's version was
// missing it.
func TestCursorFullVerbatimEchoIsNotEmptied(t *testing.T) {
	prompt := "return exactly this token: DONE_OK"
	text := "return exactly this token: DONE_OK"

	got := stripCursorEchoedUserPrompt(text, prompt)
	if got == "" {
		t.Fatalf("legitimate full-text answer was wrongly emptied")
	}
}
