package claudecode

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
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

// claudePaneResetPattern captures the reset clock time and optional IANA zone
// Claude prints beside a limit statement:
//
//	You've hit your weekly limit · resets 11:30pm (Asia/Calcutta)
//
// PLAT-101 ranks reset-time sources: structured rate_limits.<window>.resets_at
// from the status line first, typed provider metadata second, and this visible
// text only as a compatibility fallback. It is the fallback because it is
// lossy — the pane states a wall-clock time with no date, so the exact instant
// has to be reconstructed, and a wrong reconstruction is worse than none.
var claudePaneResetPattern = regexp.MustCompile(
	`(?i)resets?\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*(?:\(([^)]+)\))?`,
)

// ClaudeUsageLimitResetAt reconstructs the absolute instant a limit stated in
// pane text reopens. now is passed in for deterministic testing.
//
// Returns the zero time whenever the answer would be a guess: no reset stated,
// an unparseable clock, or an unknown zone. PLAT-101 requires an explicit
// unknown-capacity state rather than a fabricated timestamp, because a wrong
// timestamp silently schedules a resume that fails again — worse than
// admitting the time is unknown and asking.
func ClaudeUsageLimitResetAt(text string, now time.Time) time.Time {
	match := claudePaneResetPattern.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}
	}

	hour, err := strconv.Atoi(match[1])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}
	}
	minute := 0
	if match[2] != "" {
		if minute, err = strconv.Atoi(match[2]); err != nil || minute < 0 || minute > 59 {
			return time.Time{}
		}
	}
	switch strings.ToLower(match[3]) {
	case "pm":
		if hour < 12 {
			hour += 12
		}
	case "am":
		if hour == 12 {
			hour = 0
		}
	}

	// The zone the pane names is the one the clock time is expressed in.
	// Guessing the server's zone instead would silently shift the instant by
	// hours, so an unresolvable zone yields no answer at all.
	loc := now.Location()
	if zone := strings.TrimSpace(match[4]); zone != "" {
		resolved, err := time.LoadLocation(zone)
		if err != nil {
			return time.Time{}
		}
		loc = resolved
	}

	local := now.In(loc)
	reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !reset.After(local) {
		// The stated time already passed today, so it names tomorrow. A window
		// that reopens "at 11:30pm" when it is already 11:45pm means the next
		// 11:30pm, not one fifteen minutes in the past.
		reset = reset.AddDate(0, 0, 1)
	}
	return reset.UTC()
}

// NewClaudeUsageLimitError builds the typed failure for a pane parked on a
// usage-limit wall, carrying the reset instant when the pane stated one.
//
// PLAT-101: this path previously returned a plain fmt.Errorf whose text
// ("...rate limit reached") was then classified by llmerrors as
// KindRateLimit — transient throttling. That is the wrong contract in the way
// that matters most: IsRetryable reports true for transient throttling, so the
// stack retried the same exhausted model instead of failing over, and no
// consumer could learn when capacity returns because a formatted sentence
// carries no instant. Quota exhaustion is not throttling; it does not clear in
// seconds and same-model retries cannot succeed.
func NewClaudeUsageLimitError(provider, model, paneText string, now time.Time) error {
	return &llmerrors.Error{
		Kind:     llmerrors.KindQuotaExhausted,
		Provider: provider,
		Model:    model,
		RetryAt:  ClaudeUsageLimitResetAt(paneText, now),
		Err:      errors.New("claude code usage limit reached"),
	}
}
