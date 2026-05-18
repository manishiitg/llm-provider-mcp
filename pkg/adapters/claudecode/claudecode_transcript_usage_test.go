package claudecode

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadClaudeTranscriptUsageAggregatesTurn proves the extractor
//  1. globs the right path,
//  2. sums input/output/cache fields across multiple `assistant`
//     events in the same turn,
//  3. honors turnStart so prior-turn events from the same session
//     (a real possibility with --resume) are excluded.
func TestReadClaudeTranscriptUsageAggregatesTurn(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "deadbeef-1111-2222-3333-444455556666"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-fake")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")

	turnStart := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	beforeTurn := turnStart.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	earlyTurn := turnStart.Add(1 * time.Second).Format(time.RFC3339Nano)
	lateTurn := turnStart.Add(2 * time.Second).Format(time.RFC3339Nano)

	// Three assistant events: one BEFORE turnStart (should be ignored),
	// two AFTER (should be summed). Include `model` on each so we can
	// also exercise the latest-model capture.
	lines := []string{
		// previous turn — must be ignored
		`{"type":"assistant","timestamp":"` + beforeTurn + `","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":999,"output_tokens":999,"cache_creation_input_tokens":999,"cache_read_input_tokens":999}}}`,
		// non-assistant noise — must be ignored
		`{"type":"user","timestamp":"` + earlyTurn + `","message":{"content":"hi"}}`,
		// current turn iteration #1
		`{"type":"assistant","timestamp":"` + earlyTurn + `","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":5,"cache_read_input_tokens":100}}}`,
		// current turn iteration #2 (latest — model should match this one)
		`{"type":"assistant","timestamp":"` + lateTurn + `","message":{"model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":100}}}`,
	}
	if err := os.WriteFile(transcript, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	gi, model := readClaudeTranscriptUsage(sessionID, turnStart)
	if gi == nil {
		t.Fatal("expected non-nil GenerationInfo")
	}
	// PromptTokens = input(10+1) + cache_creation(5+0) = 16
	if gi.PromptTokens == nil || *gi.PromptTokens != 16 {
		t.Fatalf("PromptTokens = %v, want 16", gi.PromptTokens)
	}
	// CompletionTokens = output(20+50) = 70
	if gi.CompletionTokens == nil || *gi.CompletionTokens != 70 {
		t.Fatalf("CompletionTokens = %v, want 70", gi.CompletionTokens)
	}
	// CachedContentTokens = cache_read(100+100) = 200
	if gi.CachedContentTokens == nil || *gi.CachedContentTokens != 200 {
		t.Fatalf("CachedContentTokens = %v, want 200", gi.CachedContentTokens)
	}
	// TotalTokens = prompt(16) + completion(70) + cache_read(200) = 286
	if gi.TotalTokens == nil || *gi.TotalTokens != 286 {
		t.Fatalf("TotalTokens = %v, want 286", gi.TotalTokens)
	}
	if model != "claude-opus-4-7" {
		t.Fatalf("model = %q, want claude-opus-4-7 (latest in-turn assistant event)", model)
	}
}

// TestReadClaudeTranscriptUsageReturnsNilWhenMissing makes sure the
// extractor is silent (no panics, no logging) when the transcript
// doesn't exist.
func TestReadClaudeTranscriptUsageReturnsNilWhenMissing(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if gi, model := readClaudeTranscriptUsage("nonexistent-session-id", time.Time{}); gi != nil || model != "" {
		t.Fatalf("expected nil/empty for missing transcript; got gi=%+v model=%q", gi, model)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
