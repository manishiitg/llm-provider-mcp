package codexcli

import (
	"os"
	"path/filepath"
	"strconv"
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
		// session_meta before the turn still carries the session id we need for resume.
		`{"type":"session_meta","timestamp":"` + beforeTurn + `","payload":{"id":"aaaabbbb-cccc-dddd-eeee-ffff00001111","cwd":"/x"}}`,
		// prior-turn token snapshot — must be ignored
		`{"type":"event_msg","timestamp":"` + beforeTurn + `","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":999,"cached_input_tokens":999,"output_tokens":999,"reasoning_output_tokens":999,"total_tokens":1}}}}`,
		// turn_context: in-turn, captures model name
		`{"type":"turn_context","timestamp":"` + earlyTurn + `","payload":{"model":"gpt-5.4","reasoning_effort":"high"}}`,
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

	gi, model, threadID := readCodexTranscriptUsage(turnStart, "/x")
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
	if model != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", model)
	}
	if threadID != "aaaabbbb-cccc-dddd-eeee-ffff00001111" {
		t.Fatalf("threadID = %q, want rollout session id", threadID)
	}
}

func TestReadCodexTranscriptUsageReturnsNilWhenMissing(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if gi, model, threadID := readCodexTranscriptUsage(time.Now(), ""); gi != nil || model != "" || threadID != "" {
		t.Fatalf("expected nil/empty for missing rollout dir; got gi=%+v model=%q threadID=%q", gi, model, threadID)
	}
}

func TestReadCodexTranscriptUsageFiltersByWorkingDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dayDir := filepath.Join(tmpHome, ".codex", "sessions", "2026", "05", "21")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	turnStart := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	inTurn := turnStart.Add(2 * time.Second).Format(time.RFC3339Nano)
	beforeTurn := turnStart.Add(-1 * time.Minute).Format(time.RFC3339Nano)

	writeRollout := func(name, id, cwd string, inputTokens int, mtime time.Time) {
		t.Helper()
		path := filepath.Join(dayDir, name)
		lines := []string{
			`{"type":"session_meta","timestamp":"` + beforeTurn + `","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}`,
			`{"type":"turn_context","timestamp":"` + inTurn + `","payload":{"model":"gpt-5.4"}}`,
			`{"type":"event_msg","timestamp":"` + inTurn + `","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":` + inputTokensString(inputTokens) + `,"cached_input_tokens":0,"output_tokens":10,"total_tokens":` + inputTokensString(inputTokens+10) + `}}}}`,
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("write rollout: %v", err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	writeRollout(
		"rollout-2026-05-21T12-00-00-11111111-1111-4111-8111-111111111111.jsonl",
		"11111111-1111-4111-8111-111111111111",
		"/wrong",
		900,
		turnStart.Add(10*time.Second),
	)
	writeRollout(
		"rollout-2026-05-21T12-00-00-22222222-2222-4222-8222-222222222222.jsonl",
		"22222222-2222-4222-8222-222222222222",
		"/wanted",
		500,
		turnStart.Add(5*time.Second),
	)

	gi, _, threadID := readCodexTranscriptUsage(turnStart, "/wanted")
	if threadID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("threadID = %q, want working-dir-matched session", threadID)
	}
	if gi == nil || gi.PromptTokens == nil || *gi.PromptTokens != 500 {
		t.Fatalf("PromptTokens = %#v, want 500 from matched rollout", gi)
	}

	_, _, newestThreadID := readCodexTranscriptUsage(turnStart, "")
	if newestThreadID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("unscoped threadID = %q, want freshest rollout", newestThreadID)
	}
}

func inputTokensString(v int) string {
	return strconv.Itoa(v)
}
