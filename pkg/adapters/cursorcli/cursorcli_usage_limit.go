package cursorcli

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
)

// PLAT-101 for Cursor. When a Cursor plan is spent, the TUI prints the notice
// as its own lines and returns to the prompt with no assistant output:
//
//	Error: Weekly usage limit reached. It resets in 6 days.
//	Buy credits to keep going now, or upgrade your plan for more weekly usage.
//
// Without this, the turn ended as "returned to the prompt without visible
// assistant output" and whatever partial text preceded it reached the caller
// as if it were the answer (RTS, 2026-09-29: a Crew's reply to another Crew's
// ask). Quota exhaustion must surface as KindQuotaExhausted so the run waits
// for capacity instead of failing or passing on a half answer.
//
// A statement only counts at the START of a line, after an optional "Error:",
// so an agent writing about someone else's usage limit is not misread.
var cursorUsageLimitLine = regexp.MustCompile(
	`(?i)^(?:error:\s*)?(?:(?:weekly|monthly|daily|hourly)\s+)?usage\s+limit\s+(?:reached|exceeded)\b|^(?:error:\s*)?you(?:'ve|’ve|\s+have)\s+(?:hit|reached)\s+your\s+(?:[a-z0-9-]+\s+){0,3}limit\b`,
)

// cursorResetInPattern reads the relative reset Cursor states: "resets in 6
// days", "resets in 3 hours", "resets in 45 minutes".
var cursorResetInPattern = regexp.MustCompile(`(?i)resets?\s+in\s+(\d{1,3})\s*(day|hour|hr|minute|min)s?\b`)

const cursorUsageLimitLineMarkers = " \t⎿●⏺│•*·✻⚠!-"

// IsCursorUsageLimitText reports whether pane text states that the Cursor
// plan's usage limit has been reached.
func IsCursorUsageLimitText(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimLeft(line, cursorUsageLimitLineMarkers)
		if line != "" && cursorUsageLimitLine.MatchString(line) {
			return true
		}
	}
	return false
}

// CursorUsageLimitResetAt returns when the limit reopens, from the relative
// time Cursor states, or the zero time when none is stated.
func CursorUsageLimitResetAt(text string, now time.Time) time.Time {
	match := cursorResetInPattern.FindStringSubmatch(text)
	if match == nil {
		return time.Time{}
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n <= 0 {
		return time.Time{}
	}
	unit := time.Minute
	switch strings.ToLower(match[2]) {
	case "day":
		unit = 24 * time.Hour
	case "hour", "hr":
		unit = time.Hour
	}
	return now.Add(time.Duration(n) * unit).UTC()
}

// NewCursorUsageLimitError is the typed failure for a spent Cursor plan.
func NewCursorUsageLimitError(model, paneText string, now time.Time) error {
	return &llmerrors.Error{
		Kind:     llmerrors.KindQuotaExhausted,
		Provider: "cursor-cli",
		Model:    model,
		RetryAt:  CursorUsageLimitResetAt(paneText, now),
		Err:      errors.New("cursor usage limit reached"),
	}
}
