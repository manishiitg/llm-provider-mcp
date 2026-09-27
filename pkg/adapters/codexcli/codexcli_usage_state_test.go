package codexcli

import "testing"

func TestCodexMaxUsedPercent(t *testing.T) {
	if _, ok := codexMaxUsedPercent(nil); ok {
		t.Fatal("nil rate limits must be unknown")
	}
	used, ok := codexMaxUsedPercent(&codexRateLimits{Primary: &codexRateLimitWindow{UsedPercent: 12}, Secondary: &codexRateLimitWindow{UsedPercent: 64}})
	if !ok || used != 64 {
		t.Fatalf("used=%v ok=%v", used, ok)
	}
	if known, _ := UsageLimitState("no-such-tmux-session"); known {
		t.Fatal("an unknown session must be unknown")
	}
}
