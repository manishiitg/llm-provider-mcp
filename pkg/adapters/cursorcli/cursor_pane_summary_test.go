package cursorcli

import (
	"strings"
	"testing"
)

// The summary says which state a screen is in and never repeats its text.
func TestCursorPaneStateSummaryNamesStatesWithoutText(t *testing.T) {
	secret := "private words the person typed"
	busy := "⣾ Generating…  ctrl+c to stop\n" + secret + "\n"
	got := cursorPaneStateSummary(busy)
	if !strings.Contains(got, "generating") {
		t.Errorf("a generating screen must say so: %s", got)
	}
	if strings.Contains(got, "private") || strings.Contains(got, secret) {
		t.Errorf("the summary must not repeat screen text: %s", got)
	}
	if empty := cursorPaneStateSummary(""); !strings.Contains(empty, "states=none") {
		t.Errorf("an empty screen has no state: %s", empty)
	}
}

func TestCursorComposerStateNeverReturnsText(t *testing.T) {
	for screen, want := range map[string]string{
		"history\n→ add a follow-up\nfooter": "empty",
		"history\n→ \nfooter":                "empty",
		"history\n→ some typed words\nx":     "nonempty",
		"no marker at all":                   "absent",
	} {
		if got := cursorComposerState(screen); got != want {
			t.Errorf("%q: got %s want %s", screen, got, want)
		}
	}
	noteCursorInputAccepted("s-test")
	if ms := cursorSinceLastAcceptedMS("s-test"); ms < 0 || ms > 5000 {
		t.Errorf("last accepted: %d", ms)
	}
	if ms := cursorSinceLastAcceptedMS("never-seen"); ms != -1 {
		t.Errorf("unknown session: %d", ms)
	}
}
