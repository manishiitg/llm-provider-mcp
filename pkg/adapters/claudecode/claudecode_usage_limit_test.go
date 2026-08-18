package claudecode

import (
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
)

// PLAT-101. The adapter recognized only the exact sentence "You've hit your
// limit". Claude's weekly wording inserts a qualifier ("You've hit your weekly
// limit"), which does not contain that substring, so a real limit wall went
// undetected and the workflow lifecycle waited on a terminal that would never
// produce another token.
func TestClaudeUsageLimitTextMatchesRealWordings(t *testing.T) {
	for _, text := range []string{
		"You've hit your limit",
		"You've hit your weekly limit · resets 11:30pm (Asia/Calcutta)",
		"You've hit your 5-hour limit",
		"You have reached your usage limit",
		"You've exceeded your weekly usage limit",
		"you've hit your LIMIT",
		"Usage limit reached",
		"Upgrade to increase your usage limit",
	} {
		if !IsClaudeUsageLimitText(text) {
			t.Errorf("IsClaudeUsageLimitText(%q) = false, want true", text)
		}
	}
}

// The matcher must not fire on ordinary prose that mentions limits — including
// an agent reasoning aloud about rate limiting, which appears in panes
// constantly. A false positive here kills a healthy session.
func TestClaudeUsageLimitTextIgnoresUnrelatedProse(t *testing.T) {
	for _, text := range []string{
		"",
		"   ",
		"Retrying after a rate limit response from the API",
		"I should add a limit to the query so it does not scan every row",
		"The rate limit for this endpoint is 100 requests per minute",
		"limit",
		"Checking whether we hit any limits during this run",
		"Set LIMIT 10 in the SQL statement",
	} {
		if IsClaudeUsageLimitText(text) {
			t.Errorf("IsClaudeUsageLimitText(%q) = true, want false", text)
		}
	}
}

// The two pane-inspection sites must agree with the matcher, since either one
// firing alone is what decides whether a stalled pane is recognized.
func TestClaudeFatalDetectorsUseTheTolerantMatcher(t *testing.T) {
	weekly := "You've hit your weekly limit · resets 11:30pm (Asia/Calcutta)"
	if got := detectTmuxFatalStatus(weekly); got != "rate limit reached" {
		t.Errorf("detectTmuxFatalStatus(weekly) = %q, want %q", got, "rate limit reached")
	}
	if !isClaudeFatalProgressLine(weekly) {
		t.Error("isClaudeFatalProgressLine(weekly) = false, want true")
	}
	// Unrelated fatal statuses must keep working.
	if got := detectTmuxFatalStatus("Not logged in"); got != "not logged in" {
		t.Errorf("detectTmuxFatalStatus(not logged in) = %q", got)
	}
}

// PLAT-101 wiring. The limit path used to return a plain error whose text
// ("...rate limit reached") llmerrors then read as KindRateLimit — transient
// throttling. Measured before this change: IsRetryable true (so the stack
// retried an exhausted model) and no reset time at all.
func TestUsageLimitErrorIsTypedQuotaExhaustionWithResetTime(t *testing.T) {
	pane := "You've hit your weekly limit · resets 11:30pm (Asia/Calcutta)"
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

	err := NewClaudeUsageLimitError("claudecode", "sonnet", pane, now)

	if !llmerrors.IsQuotaExhausted(err) {
		t.Fatalf("kind = %s, want quota_exhausted: throttling and an exhausted subscription window need opposite handling",
			llmerrors.KindOf(err))
	}
	if llmerrors.IsRetryable(err) {
		t.Error("quota exhaustion must not be retryable on the same model; that is what burned retries against a window that reopens in hours")
	}
	// 11:30pm Asia/Calcutta (UTC+5:30) == 18:00 UTC the same day.
	want := time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC)
	if got := llmerrors.RetryAtOrZero(err); !got.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", got, want)
	}
}

func TestClaudeUsageLimitResetAtParsesStatedWordings(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		text string
		want time.Time
	}{
		{"resets 11:30pm (Asia/Calcutta)", time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC)},
		{"resets 6pm (UTC)", time.Date(2026, 8, 18, 18, 0, 0, 0, time.UTC)},
		{"reset 11:30 pm (UTC)", time.Date(2026, 8, 18, 23, 30, 0, 0, time.UTC)},
		{"resets 12am (UTC)", time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}, // midnight is tomorrow
	}
	for _, tc := range cases {
		if got := ClaudeUsageLimitResetAt(tc.text, now); !got.Equal(tc.want) {
			t.Errorf("ClaudeUsageLimitResetAt(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// A stated time that already passed today names tomorrow's occurrence -- a
// window reopening "at 11:30pm" when it is already 11:45pm is not in the past.
func TestClaudeUsageLimitResetAtRollsForwardWhenTimeAlreadyPassed(t *testing.T) {
	now := time.Date(2026, 8, 18, 23, 45, 0, 0, time.UTC)
	got := ClaudeUsageLimitResetAt("resets 11:30pm (UTC)", now)
	want := time.Date(2026, 8, 19, 23, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v (tomorrow, not a past instant)", got, want)
	}
}

// PLAT-101 forbids inventing a timestamp: an unparseable or absent reset must
// yield the zero time so the caller enters an explicit unknown-capacity state.
// A wrong timestamp silently schedules a resume that fails again.
func TestClaudeUsageLimitResetAtRefusesToGuess(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	for _, text := range []string{
		"You've hit your weekly limit", // no reset stated at all
		"resets soon",                  // no clock time
		"resets 11:30pm (Not/AZone)",   // unresolvable zone
		"resets 99:99pm (UTC)",         // impossible clock
	} {
		if got := ClaudeUsageLimitResetAt(text, now); !got.IsZero() {
			t.Errorf("ClaudeUsageLimitResetAt(%q) = %v, want zero rather than a guess", text, got)
		}
	}
}

// Without a stated reset the failure is still a typed quota exhaustion -- the
// caller must be able to tell "exhausted, time unknown" from "not exhausted".
func TestUsageLimitErrorStaysTypedWithoutAResetTime(t *testing.T) {
	err := NewClaudeUsageLimitError("claudecode", "sonnet", "You've hit your weekly limit", time.Now())
	if !llmerrors.IsQuotaExhausted(err) {
		t.Error("still quota exhaustion when no reset time was stated")
	}
	if !llmerrors.RetryAtOrZero(err).IsZero() {
		t.Error("RetryAt must be zero, never guessed")
	}
}
