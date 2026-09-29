package cursorcli

import (
	"errors"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
)

func TestCursorUsageLimitIsQuotaExhaustion(t *testing.T) {
	pane := "  Cursor Agent\n\n  Error: Weekly usage limit reached. It resets in 6 days.\n  Buy credits to keep going now, or upgrade your plan for more weekly usage.\n"
	if !IsCursorUsageLimitText(pane) {
		t.Fatal("the weekly limit notice was not recognised")
	}
	now := time.Date(2026, 9, 29, 3, 3, 0, 0, time.UTC)
	err := NewCursorUsageLimitError("auto", pane, now)
	if !llmerrors.IsQuotaExhausted(err) {
		t.Fatalf("error kind = %v, want quota exhausted", llmerrors.KindOf(err))
	}
	var typed *llmerrors.Error
	if !errors.As(err, &typed) || !typed.RetryAt.Equal(now.Add(6*24*time.Hour)) {
		t.Fatalf("retry at = %v, want 6 days later", typed)
	}
	for _, prose := range []string{
		"The Notion API returned: usage limit reached for this integration.",
		"If you hit your weekly limit, Cursor stops.",
		"I checked the logs; the weekly usage limit reached error came from Stripe.",
	} {
		if IsCursorUsageLimitText(prose) {
			t.Errorf("prose misread as a limit wall: %q", prose)
		}
	}
	if IsCursorUsageLimitText("You've hit your usage limit") == false {
		t.Error("the \"You've hit your usage limit\" wording was not recognised")
	}
	if !CursorUsageLimitResetAt("resets in 3 hours", now).Equal(now.Add(3 * time.Hour)) {
		t.Error("hour reset not parsed")
	}
}
