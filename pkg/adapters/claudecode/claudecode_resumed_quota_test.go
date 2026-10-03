package claudecode

import (
	"testing"
)

func TestResumedClaudeQuotaWallBelongsToTheCurrentTurn(t *testing.T) {
	oldWall := "❯ previous task\n⎿ Usage limit reached · continuing automatically at 4pm\n● Previous task completed\n"
	for _, tc := range []struct {
		name, baseline, pane, want string
	}{
		{"resume before statusline", "Claude is starting...", oldWall + "❯ [ASK-AI view=Incoming email]\nHelp me connect Gmail\nRemoved 2 invisible characters · review and press Enter to send", ""},
		{"active after resume", "Claude is starting...", oldWall + "❯ connect Gmail\n● Calling get_gmail_trigger\n❯ ", ""},
		{"fresh wall after historical wall", "Claude is starting...", oldWall + "❯ connect Gmail\n⎿ You've hit your 5-hour limit · resets 5pm\n❯ ", "rate limit reached"},
		{"real wall without prompt in capture", "", "⎿ Usage limit reached · resets 5pm\n❯ ", "rate limit reached"},
		{"exact baseline retains real wall", "❯ connect Gmail\n", "❯ connect Gmail\n⎿ Usage limit reached\n❯ ", "rate limit reached"},
		{"old prompt glyph", "loading", oldWall + "> new request\n● Working\n> ", ""},
		{"dead pane with old wall", "loading", oldWall + "❯ new request\nPane is dead", "claude code process exited"},
		{"logged out with old wall", "loading", oldWall + "❯ new request\nNot logged in · Please run /login", "not logged in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delta := capturedAfterPaneBaseline(tc.pane, tc.baseline)
			quota := claudeUsageLimitTextForTurn(tc.pane, tc.baseline)
			if got := detectTmuxFatalStatusWithQuotaText(delta, quota); got != tc.want {
				t.Fatalf("fatal status = %q, want %q; quota candidate:\n%s", got, tc.want, quota)
			}
		})
	}
}
