package llmtypes

import (
	"encoding/json"
	"testing"
	"time"
)

// PLAT-101: the reset instant must survive as data. Formatting it away is what
// left the runtime unable to say how long a workflow had to wait.
func TestRateLimitWindowsSurviveOnStatusLine(t *testing.T) {
	reset := time.Date(2026, 8, 18, 23, 30, 0, 0, time.UTC)
	status := &StatusLine{Provider: "claudecode"}
	status.SetRateLimitWindows([]RateLimitWindow{
		{Name: "five_hour", UsedPercent: 42},
		{Name: "seven_day", UsedPercent: 100, ResetsAt: reset},
	})

	got := status.RateLimitWindows()
	if len(got) != 2 {
		t.Fatalf("windows = %d, want 2", len(got))
	}
	if got[1].ResetsAt != reset {
		t.Errorf("resets_at = %v, want %v", got[1].ResetsAt, reset)
	}
	if got[0].Exhausted() {
		t.Error("five_hour at 42%% must not report exhausted")
	}
	if !got[1].Exhausted() {
		t.Error("seven_day at 100%% must report exhausted")
	}
}

// A StatusLine crosses a process boundary between the adapter that fills it and
// the runtime that reads it, so the JSON round trip is the real path.
func TestRateLimitWindowsSurviveJSONRoundTrip(t *testing.T) {
	reset := time.Date(2026, 8, 18, 23, 30, 0, 0, time.UTC)
	status := &StatusLine{}
	status.SetRateLimitWindows([]RateLimitWindow{{Name: "seven_day", UsedPercent: 100, ResetsAt: reset}})

	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var decoded StatusLine
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.RateLimitWindows()
	if len(got) != 1 {
		t.Fatalf("windows after round trip = %d, want 1", len(got))
	}
	if !got[0].ResetsAt.Equal(reset) {
		t.Errorf("resets_at after round trip = %v, want %v", got[0].ResetsAt, reset)
	}
}

// The caller is unblocked by the FIRST exhausted window to reopen; windows that
// are not exhausted, have no reset, or already passed say nothing about the
// current block.
func TestEarliestResetPicksTheSoonestBlockingWindow(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	windows := []RateLimitWindow{
		{Name: "not_exhausted", UsedPercent: 10, ResetsAt: now.Add(time.Minute)},
		{Name: "seven_day", UsedPercent: 100, ResetsAt: now.Add(6 * time.Hour)},
		{Name: "five_hour", UsedPercent: 100, ResetsAt: now.Add(2 * time.Hour)},
		{Name: "stale", UsedPercent: 100, ResetsAt: now.Add(-time.Hour)},
		{Name: "unknown_reset", UsedPercent: 100},
	}
	at, name := EarliestReset(windows, now)
	if name != "five_hour" {
		t.Errorf("window = %q, want five_hour (soonest exhausted future reset)", name)
	}
	if !at.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("reset = %v, want %v", at, now.Add(2*time.Hour))
	}
}

// "Exhausted, reset time unknown" must stay distinguishable from "not
// exhausted": PLAT-101 requires the caller to enter an unknown-capacity state
// rather than invent a timestamp.
func TestEarliestResetReturnsZeroWhenNoReliableTime(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	at, name := EarliestReset([]RateLimitWindow{{Name: "seven_day", UsedPercent: 100}}, now)
	if !at.IsZero() || name != "" {
		t.Errorf("EarliestReset = (%v, %q), want zero time and empty name", at, name)
	}
}
