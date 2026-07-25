package cursorcli

import (
	"strings"
	"testing"
)

// TestCursorPaneExtractionIsWrappedByDesign pins WHY the final answer no longer
// comes from the pane alone.
//
// The pane is a terminal rendering: the CLI wraps at the pane width, and a soft
// wrap is byte-identical to a newline the model typed, so the author's structure
// cannot be recovered from it. This test asserts that pane extraction still
// behaves that way — it is not a bug to fix here, it is the reason
// llmtypes.ReconcileFinalAnswer exists and prefers cursor's store.db transcript
// (see TestCursorFinalAnswerPrefersUnwrappedTranscript, and
// CertReplyFormattingFidelity).
//
// Keeping it asserted means nobody "fixes" wrapping by adding a heuristic
// dewrap step to the pane parser, which would corrupt deliberate structure —
// lists worst of all — while appearing to help.
func TestCursorPaneExtractionIsWrappedByDesign(t *testing.T) {
	// One logical paragraph, as a 60-column pane would hold it.
	pane := strings.Join([]string{
		"Assistant: Let's do this one step at a time. First we need to",
		"borrow, because 1/6 is smaller than 5/6, so **5 1/6 becomes 4",
		"7/6**. Can you see why that works?",
	}, "\n")

	got := parseCursorInteractiveResponse(pane, "", "", nil)

	if !strings.Contains(got, "\n") {
		t.Fatalf("pane extraction stopped splitting a wrapped paragraph. If a dewrap step "+
			"was added, remove it — the transcript is the supported fix (see "+
			"llmtypes.ReconcileFinalAnswer). If cursor changed its rendering, update this test.\ngot: %q", got)
	}

	// Whatever the line structure, extraction must never corrupt the inline
	// markdown itself — that would break the transcript comparison in
	// ReconcileFinalAnswer, which matches on words.
	if !strings.Contains(got, "**5 1/6 becomes 4") {
		t.Errorf("inline markdown was altered by pane extraction; got: %q", got)
	}
	if !strings.Contains(got, "Can you see why that works?") {
		t.Errorf("pane extraction dropped the tail of the reply; got: %q", got)
	}
}
