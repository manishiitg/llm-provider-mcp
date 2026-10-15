package stalldiag

import (
	"strings"
	"testing"
	"time"
)

func TestSummaryNamesKnownPromptsAndNeverLogsPaneText(t *testing.T) {
	pane := "  1. Sign in with ChatGPT\n  Press enter to continue\nDo you want to send the invoice to Acme? I need permission first.\nthe customer's secret plan is to buy acme\n\n"
	got := Summary(pane)
	if !strings.Contains(got, "sign in with") || !strings.Contains(got, "press enter to continue") {
		t.Fatalf("the matched pattern names must be shown: %s", got)
	}
	for _, leaked := range []string{"Sign in with ChatGPT", "invoice", "Acme", "acme", "secret plan", "permission first"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("pane text %q must never be logged: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "lines=4") {
		t.Fatalf("line count expected: %s", got)
	}
	if !strings.Contains(got, "lines_hash=") {
		t.Fatalf("hash expected: %s", got)
	}
}

func TestWatchLogsOncePerQuietStretchAndRearmsOnChange(t *testing.T) {
	w := &Watch{Provider: "codex-cli", Session: "s1", After: time.Minute}
	start := time.Unix(1000, 0)
	logged := func() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.logged }
	w.Observe(start, "a", true)
	w.Observe(start.Add(30*time.Second), "a", true)
	if logged() {
		t.Fatal("logged before the quiet time")
	}
	w.Observe(start.Add(61*time.Second), "a", true)
	if !logged() {
		t.Fatal("not logged after the quiet time")
	}
	w.Observe(start.Add(90*time.Second), "b", true)
	if logged() {
		t.Fatal("a changed pane must re-arm the watch")
	}
}
