package claudecode

import (
	"regexp"
	"strings"
)

// PLAT-101. Claude states a reached subscription limit in several wordings,
// and the set grows over time:
//
//	You've hit your limit
//	You've hit your weekly limit · resets 11:30pm (Asia/Calcutta)
//	You've hit your 5-hour limit
//	Approaching your usage limit
//
// Matching one exact sentence missed every variant that inserts a qualifier
// between "your" and "limit" — most consequentially the weekly wording, which
// left the workflow lifecycle waiting on a terminal that would never produce
// another token. The run stayed "running" for hours with nothing executing.
//
// The pattern is deliberately shaped around what is stable in the wording (a
// possessive followed by "limit", optionally qualified) rather than around any
// single sentence, so a new qualifier does not silently reintroduce the same
// stall. It stays anchored on "hit"/"reached"/"exceeded" so ordinary prose
// mentioning the word "limit" — including an agent reasoning aloud about rate
// limits — is not misread as the pane being parked on a limit wall.
var claudeUsageLimitPattern = regexp.MustCompile(
	`(?i)\b(?:hit|reached|exceeded)\s+(?:your|the)\s+(?:[a-z0-9-]+\s+){0,3}limit\b`,
)

// claudeUsageLimitAlternatives are limit statements whose phrasing does not
// fit the verb+possessive shape above.
var claudeUsageLimitAlternatives = []string{
	"usage limit reached",
	"usage limits reached",
	"out of usage",
	"upgrade to increase your usage limit",
}

// IsClaudeUsageLimitText reports whether text states that a Claude
// subscription/usage limit has been reached. It is intentionally tolerant of
// wording variants and case, and intentionally NOT tolerant of unrelated prose
// that merely contains the word "limit".
func IsClaudeUsageLimitText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if claudeUsageLimitPattern.MatchString(trimmed) {
		return true
	}
	lowered := strings.ToLower(trimmed)
	for _, phrase := range claudeUsageLimitAlternatives {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}
