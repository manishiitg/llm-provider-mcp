package claudecode

import (
	"testing"
	"time"
)

func TestClaudeLoggedOutStatusDoesNotMatchConversationText(t *testing.T) {
	for _, text := range []string{
		"❯ Why does it say Not logged in?",
		"⏺ The app reports Not logged in, but the session is working.",
		"⎿ grep: return \"not logged in\" when strings.Contains(text, \"Not logged in\")",
		"The response failed: claude code tmux session failed: Not logged in",
		"⏺ Not logged in",
		"❯ Not logged in · Please run /login",
	} {
		if got := detectTmuxFatalStatus(text); got != "" {
			t.Errorf("conversation text %q became fatal: %s", text, got)
		}
		if isClaudeFatalProgressLine(text) {
			t.Errorf("ordinary assistant text was discarded as a fatal status: %q", text)
		}
	}
}

func TestClaudeLoggedOutStatusRecognizesLoginWall(t *testing.T) {
	for _, text := range []string{
		"Not logged in",
		"  Not logged in · Please run /login  ",
		"⎿ Not logged in · /login",
		"⏺ Not logged in · Please run /login",
		"Not logged in\u00a0·\u00a0Please run /login",
	} {
		if got := detectTmuxFatalStatus(text); got != "not logged in" {
			t.Errorf("login wall %q: got %q", text, got)
		}
	}
}

func TestClaudeLoggedOutStatusRequiresStableIdlePane(t *testing.T) {
	start := time.Date(2026, 9, 11, 5, 45, 12, 0, time.UTC)
	var status claudeLoggedOutStatus
	pane := "Not logged in · Please run /login\n❯"
	for _, elapsed := range []time.Duration{0, 250 * time.Millisecond, 1500 * time.Millisecond} {
		pending, confirmed := status.observe(pane, true, start.Add(elapsed))
		if !pending || confirmed {
			t.Fatalf("transient status ended turn after %s: pending=%v confirmed=%v", elapsed, pending, confirmed)
		}
	}
	// The incident completed in the native session after the platform had
	// reported failure. Working CLI output must contradict a stale auth footer.
	active := pane + "\n✻ Working… (esc to interrupt)"
	if pending, confirmed := status.observe(active, true, start.Add(3*time.Second)); pending || confirmed {
		t.Fatal("a working Claude session must not be rejected as logged out")
	}
	if _, confirmed := status.observe(pane, true, start.Add(4*time.Second)); confirmed {
		t.Fatal("activity must reset the confirmation window")
	}
	if pending, confirmed := status.observe(pane, true, start.Add(6*time.Second)); !pending || !confirmed {
		t.Fatal("a persistent idle login wall must still fail")
	}
}

func TestClaudeLoggedOutStatusResetsWhenPaneChangesOrWarningDisappears(t *testing.T) {
	start := time.Now()
	var status claudeLoggedOutStatus
	status.observe("Not logged in", true, start)
	if _, confirmed := status.observe("Not logged in\nConnecting", true, start.Add(3*time.Second)); confirmed {
		t.Fatal("new output must reset the stable-pane window")
	}
	if pending, confirmed := status.observe("❯", false, start.Add(4*time.Second)); pending || confirmed {
		t.Fatal("a cleared status must not remain pending")
	}
	if _, confirmed := status.observe("Not logged in", true, start.Add(7*time.Second)); confirmed {
		t.Fatal("a new warning cannot inherit confirmation from an old warning")
	}
}
