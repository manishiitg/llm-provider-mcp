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

	gi, model := readClaudeTranscriptUsage(sessionID, "", turnStart)
	if gi == nil {
		t.Fatal("expected non-nil GenerationInfo")
	}
	// PromptTokens includes every input token, including cache-served input.
	if gi.PromptTokens == nil || *gi.PromptTokens != 216 {
		t.Fatalf("PromptTokens = %v, want 216", gi.PromptTokens)
	}
	// CompletionTokens = output(20+50) = 70
	if gi.CompletionTokens == nil || *gi.CompletionTokens != 70 {
		t.Fatalf("CompletionTokens = %v, want 70", gi.CompletionTokens)
	}
	// CachedContentTokens = cache_read(100+100) = 200
	if gi.CachedContentTokens == nil || *gi.CachedContentTokens != 200 {
		t.Fatalf("CachedContentTokens = %v, want 200", gi.CachedContentTokens)
	}
	// TotalTokens = prompt(216) + completion(70) = 286.
	if gi.TotalTokens == nil || *gi.TotalTokens != 286 {
		t.Fatalf("TotalTokens = %v, want 286", gi.TotalTokens)
	}
	if model != "claude-opus-4-7" {
		t.Fatalf("model = %q, want claude-opus-4-7 (latest in-turn assistant event)", model)
	}
}

// TestReadClaudeTranscriptUsageDedupesByMessageID proves that when the
// CLI writes the same LLM call as multiple JSONL rows (one per content
// block: text chunks + each tool_use), we count its usage exactly once.
// Real claude-code transcripts repeat the call's total usage on every
// row that shares a message.id; summing per-row inflates by 5-10x.
func TestReadClaudeTranscriptUsageDedupesByMessageID(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "cafef00d-1111-2222-3333-444455556666"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-fake")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")

	turnStart := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(1 * time.Second).Format(time.RFC3339Nano)

	// Three rows from ONE LLM call (msg_A) — same usage repeated — plus
	// one row from a SECOND LLM call (msg_B). Correct totals must reflect
	// msg_A once + msg_B once, not msg_A three times.
	usageA := `"usage":{"input_tokens":3,"output_tokens":100,"cache_creation_input_tokens":42339,"cache_read_input_tokens":0}`
	usageB := `"usage":{"input_tokens":1,"output_tokens":50,"cache_creation_input_tokens":1000,"cache_read_input_tokens":42442}`

	lines := []string{
		`{"type":"assistant","timestamp":"` + ts + `","requestId":"req_A","message":{"id":"msg_A","model":"claude-opus-4-7",` + usageA + `}}`,
		`{"type":"assistant","timestamp":"` + ts + `","requestId":"req_A","message":{"id":"msg_A","model":"claude-opus-4-7",` + usageA + `}}`,
		`{"type":"assistant","timestamp":"` + ts + `","requestId":"req_A","message":{"id":"msg_A","model":"claude-opus-4-7",` + usageA + `}}`,
		`{"type":"user","timestamp":"` + ts + `","message":{"content":[{"type":"tool_result"}]}}`,
		`{"type":"assistant","timestamp":"` + ts + `","requestId":"req_B","message":{"id":"msg_B","model":"claude-opus-4-7",` + usageB + `}}`,
	}
	if err := os.WriteFile(transcript, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	gi, _ := readClaudeTranscriptUsage(sessionID, "", turnStart)
	if gi == nil {
		t.Fatal("expected non-nil GenerationInfo")
	}
	// PromptTokens includes msg_B's 42442 cache-read tokens.
	if gi.PromptTokens == nil || *gi.PromptTokens != 85785 {
		t.Fatalf("PromptTokens = %v, want 85785 (msg_A counted once + msg_B)", gi.PromptTokens)
	}
	// CompletionTokens = 100 + 50 = 150
	if gi.CompletionTokens == nil || *gi.CompletionTokens != 150 {
		t.Fatalf("CompletionTokens = %v, want 150", gi.CompletionTokens)
	}
	// CachedContentTokens = 0 + 42442 = 42442
	if gi.CachedContentTokens == nil || *gi.CachedContentTokens != 42442 {
		t.Fatalf("CachedContentTokens = %v, want 42442", gi.CachedContentTokens)
	}
	// Cache fields must also surface in Additional under the raw
	// Anthropic key names — that is the contract the cost ledger's
	// extractCacheTokens relies on to populate CacheReadTokens /
	// CacheWriteTokens. Skipping either dropped cache tokens from
	// the per-turn ledger entry for real claude-code chats.
	if gi.Additional == nil {
		t.Fatalf("gi.Additional must surface raw cache keys; got nil")
	}
	if v, ok := gi.Additional["cache_read_input_tokens"]; !ok || v.(int) != 42442 {
		t.Fatalf("gi.Additional[cache_read_input_tokens] = %v, want 42442", v)
	}
	if v, ok := gi.Additional["cache_creation_input_tokens"]; !ok || v.(int) != 43339 {
		t.Fatalf("gi.Additional[cache_creation_input_tokens] = %v, want 43339 (42339 from msg_A + 1000 from msg_B)", v)
	}
}

func TestReadClaudeTranscriptUsagePromptIncludesCacheRead(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	workingDir := t.TempDir()
	sessionID := "11111111-2222-4333-8444-555555555555"
	transcriptDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-pricing")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","timestamp":"2026-08-03T10:00:00Z","message":{"id":"msg-1","model":"claude-opus-5","usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":30,"cache_read_input_tokens":400}}}` + "\n"
	if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	usage, model := readClaudeTranscriptUsage(sessionID, workingDir, time.Time{})
	if usage == nil || usage.PromptTokens == nil || *usage.PromptTokens != 530 {
		t.Fatalf("PromptTokens = %#v, want 530 (fresh + cache creation + cache read)", usage)
	}
	if usage.TotalTokens == nil || *usage.TotalTokens != 550 {
		t.Fatalf("TotalTokens = %#v, want 550", usage.TotalTokens)
	}
	if usage.CachedContentTokens == nil || *usage.CachedContentTokens != 400 {
		t.Fatalf("CachedContentTokens = %#v, want 400", usage.CachedContentTokens)
	}
	if inclusive, _ := usage.Additional["prompt_tokens_include_cache"].(bool); !inclusive {
		t.Fatalf("prompt_tokens_include_cache = %#v, want true", usage.Additional["prompt_tokens_include_cache"])
	}
	if model != "claude-opus-5" {
		t.Fatalf("model = %q, want claude-opus-5", model)
	}
}

// TestReadClaudeTranscriptUsageReturnsNilWhenMissing makes sure the
// extractor is silent (no panics, no logging) when the transcript
// doesn't exist.
func TestReadClaudeTranscriptUsageReturnsNilWhenMissing(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if gi, model := readClaudeTranscriptUsage("nonexistent-session-id", "", time.Time{}); gi != nil || model != "" {
		t.Fatalf("expected nil/empty for missing transcript; got gi=%+v model=%q", gi, model)
	}
}

func TestReadClaudeTranscriptUsageRejectsNonUUIDSessionID(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	for _, sessionID := range []string{"../secret", "--help", "deadbeef-1111-2222-3333-444455556666/extra"} {
		if gi, model := readClaudeTranscriptUsage(sessionID, "", time.Time{}); gi != nil || model != "" {
			t.Fatalf("session %q returned gi=%+v model=%q, want nil/empty", sessionID, gi, model)
		}
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
