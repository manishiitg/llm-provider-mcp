package claudecode

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// PLAT-872: a turn resuming a long conversation after the prompt cache expired
// compacts first; a short, recent, just-compacted, running or new one does not.
func TestClaudeIdleCompactDecision(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	assistant := func(at time.Time, cacheRead int) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"usage":{"input_tokens":3,"cache_read_input_tokens":%d,"cache_creation_input_tokens":500,"output_tokens":90}}}`, at.Format(time.RFC3339Nano), cacheRead)
	}
	boundary := func(at time.Time) string {
		return fmt.Sprintf(`{"type":"system","subtype":"compact_boundary","timestamp":%q,"compactMetadata":{"trigger":"auto","preTokens":880000,"postTokens":21900}}`, at.Format(time.RFC3339Nano))
	}
	sidechain := fmt.Sprintf(`{"type":"assistant","isSidechain":true,"timestamp":%q,"message":{"usage":{"input_tokens":1,"cache_read_input_tokens":10}}}`, now.Add(-time.Minute).Format(time.RFC3339Nano))
	long := 880_000
	cases := []struct {
		name       string
		resume     string
		running    bool
		transcript []string
		compact    bool
		reason     string
	}{
		{"long and idle past the cache", "s1", false, []string{assistant(now.Add(-2*time.Hour), 50_000), assistant(now.Add(-70*time.Minute), long)}, true, "idle_cache_expired"},
		{"subagent rows do not count as activity", "s1", false, []string{assistant(now.Add(-70*time.Minute), long), sidechain}, true, "idle_cache_expired"},
		{"cache still warm", "s1", false, []string{assistant(now.Add(-20*time.Minute), long)}, false, "recently_active"},
		{"short conversation", "s1", false, []string{assistant(now.Add(-3*time.Hour), 150_000)}, false, "below_threshold"},
		{"last event is a compaction", "s1", false, []string{assistant(now.Add(-3*time.Hour), long), boundary(now.Add(-2 * time.Hour))}, false, "just_compacted"},
		{"session still running", "s1", true, []string{assistant(now.Add(-3*time.Hour), long)}, false, "session_running"},
		{"new conversation", "", false, nil, false, "new_conversation"},
		{"no readable transcript", "s1", false, []string{"not json"}, false, "no_transcript"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := parseClaudeIdleState(strings.NewReader(strings.Join(tc.transcript, "\n")), false)
			compact, reason := claudeIdleCompactDecision(tc.resume, tc.running, state, claudeIdleCompactDefaultMinTokens, now)
			if compact != tc.compact || reason != tc.reason {
				t.Fatalf("got compact=%t reason=%s, want compact=%t reason=%s", compact, reason, tc.compact, tc.reason)
			}
		})
	}
}
