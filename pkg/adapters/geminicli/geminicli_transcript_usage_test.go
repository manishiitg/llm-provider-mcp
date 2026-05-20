package geminicli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReadGeminiTranscriptUsageAggregatesTurn proves the extractor
//  1. resolves the chats dir from the projectDirID,
//  2. picks the most-recently-modified session jsonl,
//  3. sums tokens across `type=gemini` events at or after turnStart,
//  4. ignores prior-turn events.
func TestReadGeminiTranscriptUsageAggregatesTurn(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const projectDirID = "interactive-fake-test-123"
	chatsDir := filepath.Join(tmpHome, ".gemini", "tmp", "gemini-cli-project-"+projectDirID, "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(chatsDir, "session-2026-05-19T12-00-abcdef.jsonl")

	turnStart := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	beforeTurn := turnStart.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	earlyTurn := turnStart.Add(1 * time.Second).Format(time.RFC3339Nano)
	lateTurn := turnStart.Add(2 * time.Second).Format(time.RFC3339Nano)

	lines := []string{
		// session header (no tokens, no type=gemini)
		`{"sessionId":"abc","projectHash":"x","startTime":"` + beforeTurn + `","lastUpdated":"` + lateTurn + `","kind":"main"}`,
		// previous-turn assistant — must be ignored
		`{"type":"gemini","timestamp":"` + beforeTurn + `","tokens":{"input":9999,"output":9999,"cached":9999,"thoughts":9999,"tool":0,"total":1}}`,
		// non-gemini noise
		`{"type":"user","timestamp":"` + earlyTurn + `","content":"hi"}`,
		// current turn iteration #1
		`{"type":"gemini","timestamp":"` + earlyTurn + `","model":"gemini-3.5-flash","tokens":{"input":100,"output":20,"cached":30,"thoughts":15,"tool":5,"total":170}}`,
		// current turn iteration #2 (latest — model should be this one)
		`{"type":"gemini","timestamp":"` + lateTurn + `","model":"gemini-3.1-flash-lite","tokens":{"input":50,"output":10,"cached":0,"thoughts":0,"tool":0,"total":60}}`,
		// $set upsert — must not break parsing
		`{"$set":{"lastUpdated":"` + lateTurn + `"}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	gi, model := readGeminiTranscriptUsage(projectDirID, turnStart)
	if gi == nil {
		t.Fatal("expected non-nil GenerationInfo")
	}
	// PromptTokens = input(100+50) + tool(5+0) = 155
	if gi.PromptTokens == nil || *gi.PromptTokens != 155 {
		t.Fatalf("PromptTokens = %v, want 155", gi.PromptTokens)
	}
	// CompletionTokens = output(20+10) = 30
	if gi.CompletionTokens == nil || *gi.CompletionTokens != 30 {
		t.Fatalf("CompletionTokens = %v, want 30", gi.CompletionTokens)
	}
	// CachedContentTokens = cached(30+0) = 30
	if gi.CachedContentTokens == nil || *gi.CachedContentTokens != 30 {
		t.Fatalf("CachedContentTokens = %v, want 30", gi.CachedContentTokens)
	}
	// ThoughtsTokens = thoughts(15+0) = 15
	if gi.ThoughtsTokens == nil || *gi.ThoughtsTokens != 15 {
		t.Fatalf("ThoughtsTokens = %v, want 15", gi.ThoughtsTokens)
	}
	// TotalTokens = prompt(155) + completion(30) + cached(30) + thoughts(15) = 230
	if gi.TotalTokens == nil || *gi.TotalTokens != 230 {
		t.Fatalf("TotalTokens = %v, want 230", gi.TotalTokens)
	}
	if model != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite (latest in-turn event)", model)
	}
}

func TestReadGeminiTranscriptUsageReturnsNilWhenMissing(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if gi, model := readGeminiTranscriptUsage("nonexistent-project", time.Time{}); gi != nil || model != "" {
		t.Fatalf("expected nil/empty for missing chats dir; got gi=%+v model=%q", gi, model)
	}
}
