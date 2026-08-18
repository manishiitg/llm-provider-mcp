package claudecode

import "testing"

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
