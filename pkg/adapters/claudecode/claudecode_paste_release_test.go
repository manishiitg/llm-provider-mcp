package claudecode

import "testing"

func TestClaudeRetryNeedsPasteRelease(t *testing.T) {
	draft := "──────\n❯ Reply with OK\n  second line\n──────\n  ⏵⏵ auto mode on"
	empty := "──────\n❯ \n──────\n  ⏵⏵ auto mode on"
	cases := []struct {
		name    string
		attempt int
		pane    string
		want    bool
	}{
		{"first attempt never sends a lone marker", 1, draft, false},
		{"retry with the draft still in the box", 2, draft, true},
		{"retry with an empty box", 2, empty, false},
	}
	for _, tc := range cases {
		if got := claudeRetryNeedsPasteRelease(tc.attempt, tc.pane); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
