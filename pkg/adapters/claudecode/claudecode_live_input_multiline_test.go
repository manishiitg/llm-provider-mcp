package claudecode

import (
	"strings"
	"testing"
)

func TestClaudeComposerHoldsMessageTailForOverflowingMultilineInput(t *testing.T) {
	var lines []string
	for i := 1; i <= 16; i++ {
		lines = append(lines, strings.Repeat("step detail ", 8)+"number "+string(rune('A'+i)))
	}
	lines = append(lines, "5. Set is_new_comment = 1 only for tasks whose latest_comment_at is strictly newer than the previous sync.")
	message := strings.Join(lines, "\n")

	// The visible composer shows only the last lines, soft-wrapped mid-word.
	visible := strings.Join(lines[12:], "\n")
	wrapped := visible[:40] + "\n" + visible[40:]
	if !claudeComposerHoldsMessageTail(wrapped, message) {
		t.Fatal("the end of an overflowing message must count as landed")
	}
	if claudeComposerHoldsMessageTail(strings.Join(lines[:10], "\n"), message) {
		t.Fatal("a composer still missing the end of the message has not settled")
	}
	if claudeComposerHoldsMessageTail("short", "short") {
		t.Fatal("short input is left to the whole-message check")
	}
}
