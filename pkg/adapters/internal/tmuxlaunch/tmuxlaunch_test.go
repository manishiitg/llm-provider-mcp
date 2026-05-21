package tmuxlaunch

import (
	"context"
	"testing"
	"time"
)

func TestAcquireQueuesConcurrentStarts(t *testing.T) {
	t.Setenv(EnvStartConcurrency, "1")

	releaseFirst, err := Acquire(context.Background(), "test", "first")
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	defer releaseFirst()

	acquiredSecond := make(chan struct{})
	go func() {
		releaseSecond, err := Acquire(context.Background(), "test", "second")
		if err != nil {
			return
		}
		defer releaseSecond()
		close(acquiredSecond)
	}()

	select {
	case <-acquiredSecond:
		t.Fatal("second startup acquired before first released")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()

	select {
	case <-acquiredSecond:
	case <-time.After(time.Second):
		t.Fatal("second startup did not acquire after first released")
	}
}

func TestAcquireHonorsCancellationWhileQueued(t *testing.T) {
	t.Setenv(EnvStartConcurrency, "1")

	releaseFirst, err := Acquire(context.Background(), "test", "first")
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	defer releaseFirst()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if releaseSecond, err := Acquire(ctx, "test", "second"); err == nil {
		releaseSecond()
		t.Fatal("queued startup acquired despite canceled context")
	}
}

func TestPromptWaitUsesGlobalDefault(t *testing.T) {
	t.Setenv(EnvPromptWaitSeconds, "")
	t.Setenv("TEST_PROVIDER_PROMPT_WAIT_SECONDS", "")

	if got := PromptWait("TEST_PROVIDER_PROMPT_WAIT_SECONDS"); got != defaultPromptWait {
		t.Fatalf("PromptWait default = %v, want %v", got, defaultPromptWait)
	}
}

func TestPromptWaitUsesGlobalOverride(t *testing.T) {
	t.Setenv(EnvPromptWaitSeconds, "3")
	t.Setenv("TEST_PROVIDER_PROMPT_WAIT_SECONDS", "")

	if got := PromptWait("TEST_PROVIDER_PROMPT_WAIT_SECONDS"); got != 3*time.Second {
		t.Fatalf("PromptWait global override = %v, want 3s", got)
	}
}

func TestPromptWaitProviderOverrideWins(t *testing.T) {
	t.Setenv(EnvPromptWaitSeconds, "3")
	t.Setenv("TEST_PROVIDER_PROMPT_WAIT_SECONDS", "7")

	if got := PromptWait("TEST_PROVIDER_PROMPT_WAIT_SECONDS"); got != 7*time.Second {
		t.Fatalf("PromptWait provider override = %v, want 7s", got)
	}
}

func TestPromptWaitIgnoresInvalidProviderOverride(t *testing.T) {
	t.Setenv(EnvPromptWaitSeconds, "3")
	t.Setenv("TEST_PROVIDER_PROMPT_WAIT_SECONDS", "bad")

	if got := PromptWait("TEST_PROVIDER_PROMPT_WAIT_SECONDS"); got != 3*time.Second {
		t.Fatalf("PromptWait invalid provider override = %v, want global 3s", got)
	}
}
