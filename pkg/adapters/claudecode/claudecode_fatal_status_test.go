package claudecode

import "testing"

// Claude Code's usage-limit message changed from "You've hit your limit" to
// "You've hit your session limit", which slipped past detectTmuxFatalStatus and
// left scheduled/main-agent runs wedged "running" forever. Lock the current
// wording (and the variants) so a future copy change is caught by tests.
func TestDetectTmuxFatalStatusUsageLimit(t *testing.T) {
	limitPanes := []string{
		"  ⎿  You've hit your session limit · resets 1:30pm (Asia/Calcutta)\n     /usage-credits to finish what you're working on.",
		"You've hit your limit",
		"You've hit your usage limit, resets later",
		"   /usage-credits to finish what you're working on.",
	}
	for _, p := range limitPanes {
		if got := detectTmuxFatalStatus(p); got == "" {
			t.Errorf("expected a fatal status for usage-limit pane, got none:\n%q", p)
		} else if got != "usage limit reached" {
			t.Errorf("got %q, want \"usage limit reached\" for:\n%q", got, p)
		}
	}

	// Non-limit panes must NOT trip it.
	for _, p := range []string{"⏺ Working on the task…", "❯ ", "Allocator done; the Opportunity Scanner is running."} {
		if got := detectTmuxFatalStatus(p); got != "" {
			t.Errorf("false positive %q on benign pane: %q", got, p)
		}
	}
}
