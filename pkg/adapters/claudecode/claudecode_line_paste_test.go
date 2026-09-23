package claudecode

import (
	"strings"
	"testing"
)

func TestClaudeLineChunks(t *testing.T) {
	if got := claudeLineChunks("", 5); len(got) != 0 {
		t.Fatalf("empty line chunks = %q", got)
	}
	line := strings.Repeat("é", 9) // 18 bytes, 2 per rune
	got := claudeLineChunks(line, 5)
	if strings.Join(got, "") != line {
		t.Fatalf("chunks do not rejoin: %q", got)
	}
	for _, c := range got {
		if len(c) > 5 || !strings.HasPrefix(c, "é") {
			t.Fatalf("chunk %q exceeds limit or splits a rune", c)
		}
	}
}
