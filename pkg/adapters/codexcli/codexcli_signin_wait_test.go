package codexcli

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Production 2026-10-15: a Codex launch that sat on its sign-in screen waited up to 90 minutes for a prompt that
// could never be accepted. The launch wait now ends at once with the plain login error.
func TestLaunchPromptWaitEndsOnTheSignInScreen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wait := codexSubmissionWait{capture: func(context.Context, string) (string, error) { return codexSignInScreen, nil }}
	start := time.Now()
	_, err := waitForCodexInitialPromptAcceptedWith(ctx, "scratch-session", "hello", nil, false, wait)
	var loginRequired *CodexLoginRequiredError
	if !errors.As(err, &loginRequired) {
		t.Fatalf("err = %v, want the sign-in error", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("the wait must end at once, took %s", time.Since(start))
	}
}
