package picli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePiRecords(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A turn the person interrupted with Escape ends with an empty assistant message and stopReason error; the retained
// turn must be able to settle on it (otherwise the chat stays Working), but a finished reply or a pending tool call
// must not count.
func TestPiTranscriptEndedWithoutReply(t *testing.T) {
	start := time.Date(2026, 10, 15, 19, 35, 0, 0, time.UTC)
	user := `{"type":"message","timestamp":"2026-10-15T19:35:14.884Z","message":{"role":"user","content":[{"type":"text","text":"run it"}]}}`
	toolCall := `{"type":"message","timestamp":"2026-10-15T19:35:17.271Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"mcp"}],"stopReason":"toolUse"}}`
	toolResult := `{"type":"message","timestamp":"2026-10-15T19:35:45.105Z","message":{"role":"toolResult","content":[{"type":"text","text":"MCP request aborted"}]}}`
	aborted := `{"type":"message","timestamp":"2026-10-15T19:35:45.109Z","message":{"role":"assistant","content":[],"stopReason":"error"}}`
	reply := `{"type":"message","timestamp":"2026-10-15T19:35:46.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stopReason":"stop"}}`

	if reason, ok := piTranscriptEndedWithoutReply(writePiRecords(t, user, toolCall, toolResult, aborted), start); !ok || reason != "error" {
		t.Fatalf("an aborted turn = %q, %v; want error, true", reason, ok)
	}
	if _, ok := piTranscriptEndedWithoutReply(writePiRecords(t, user, toolCall), start); ok {
		t.Fatal("a pending tool call is still work")
	}
	if _, ok := piTranscriptEndedWithoutReply(writePiRecords(t, user, toolCall, toolResult, aborted, reply), start); ok {
		t.Fatal("a later reply means the turn did not end empty")
	}
	if _, ok := piTranscriptEndedWithoutReply(writePiRecords(t, user, toolCall, toolResult, aborted), time.Date(2026, 10, 15, 19, 36, 0, 0, time.UTC)); ok {
		t.Fatal("an abort before this turn began is not this turn's")
	}
	// A long transcript: only the tail is read, and the verdict is the same.
	filler := `{"type":"message","timestamp":"2026-10-15T19:35:20.000Z","message":{"role":"toolResult","content":[{"type":"text","text":"` + strings.Repeat("x", 4000) + `"}]}}`
	long := []string{user, toolCall}
	for i := 0; i < 100; i++ {
		long = append(long, filler)
	}
	long = append(long, aborted)
	if reason, ok := piTranscriptEndedWithoutReply(writePiRecords(t, long...), start); !ok || reason != "error" {
		t.Fatalf("an aborted turn after a long transcript = %q, %v; want error, true", reason, ok)
	}
}
