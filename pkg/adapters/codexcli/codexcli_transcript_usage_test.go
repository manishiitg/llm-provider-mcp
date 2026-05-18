package codexcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReadCodexTranscriptUsageTakesLatestEventInTurn proves the
// extractor picks the freshest rollout file in ~/.codex/sessions and
// returns the LAST token_count event whose timestamp is at-or-after
// turnStart. Prior-turn token_count events are ignored.
func TestReadCodexTranscriptUsageTakesLatestEventInTurn(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dayDir := filepath.Join(tmpHome, ".codex", "sessions", "2026", "05", "19")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rollout := filepath.Join(dayDir, "rollout-2026-05-19T12-00-00-aaaabbbb-cccc-dddd-eeee-ffff00001111.jsonl")

	turnStart := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	beforeTurn := turnStart.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	earlyTurn := turnStart.Add(2 * time.Second).Format(time.RFC3339Nano)
	lateTurn := turnStart.Add(4 * time.Second).Format(time.RFC3339Nano)

	lines := []string{
		// session_meta — must be ignored
		`{"type":"session_meta","timestamp":"` + beforeTurn + `","payload":{"id":"aaaa","cwd":"/x"}}`,
		// prior-turn token snapshot — must be ignored
		`{"type":"event_msg","timestamp":"` + beforeTurn + `","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":999,"cached_input_tokens":999,"output_tokens":999,"reasoning_output_tokens":999,"total_tokens":1}}}}`,
		// turn-iteration #1 (early, NOT what we want)
		`{"type":"event_msg","timestamp":"` + earlyTurn + `","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":300,"cached_input_tokens":50,"output_tokens":40,"reasoning_output_tokens":10,"total_tokens":340}}}}`,
		// non-token noise
		`{"type":"response_item","timestamp":"` + earlyTurn + `","payload":{"role":"assistant","content":"hello"}}`,
		// turn-iteration #2 (LATEST — what we want)
		`{"type":"event_msg","timestamp":"` + lateTurn + `","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":500,"cached_input_tokens":120,"output_tokens":80,"reasoning_output_tokens":20,"total_tokens":580}}}}`,
	}
	if err := os.WriteFile(rollout, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	// Set mtime AFTER turnStart so the (turnStart-30s) freshness cutoff
	// in the extractor admits the file. Using time.Now() would fail
	// when the test's frozen turnStart is in the future relative to the
	// real clock.
	mtime := turnStart.Add(5 * time.Second)
	if err := os.Chtimes(rollout, mtime, mtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	gi := readCodexTranscriptUsage(turnStart)
	if gi == nil {
		t.Fatal("expected non-nil GenerationInfo")
	}
	// Expected from the LATE event: input_tokens=500, cached=120,
	// output=80, reasoning=20, total=580.
	// PromptTokens = uncached prompt = 500 - 120 = 380
	if gi.PromptTokens == nil || *gi.PromptTokens != 380 {
		t.Fatalf("PromptTokens = %v, want 380", gi.PromptTokens)
	}
	if gi.CompletionTokens == nil || *gi.CompletionTokens != 80 {
		t.Fatalf("CompletionTokens = %v, want 80", gi.CompletionTokens)
	}
	if gi.CachedContentTokens == nil || *gi.CachedContentTokens != 120 {
		t.Fatalf("CachedContentTokens = %v, want 120", gi.CachedContentTokens)
	}
	if gi.ReasoningTokens == nil || *gi.ReasoningTokens != 20 {
		t.Fatalf("ReasoningTokens = %v, want 20", gi.ReasoningTokens)
	}
	if gi.TotalTokens == nil || *gi.TotalTokens != 580 {
		t.Fatalf("TotalTokens = %v, want 580", gi.TotalTokens)
	}
}

func TestReadCodexTranscriptUsageReturnsNilWhenMissing(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if gi := readCodexTranscriptUsage(time.Now()); gi != nil {
		t.Fatalf("expected nil for missing rollout dir; got %+v", gi)
	}
}
