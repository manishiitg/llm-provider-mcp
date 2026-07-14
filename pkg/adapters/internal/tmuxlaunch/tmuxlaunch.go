package tmuxlaunch

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const EnvStartConcurrency = "CODING_SDK_TMUX_START_CONCURRENCY"
const EnvPromptWaitSeconds = "CODING_SDK_TMUX_PROMPT_WAIT_SECONDS"
const EnvRetentionSeconds = "CODING_SDK_TMUX_RETENTION_SECONDS"

const defaultStartConcurrency = 1
const defaultPromptWait = 300 * time.Second

var startGate = struct {
	sync.Mutex
	ch   chan struct{}
	size int
}{}

func Acquire(ctx context.Context, provider, sessionName string) (func(), error) {
	size := configuredStartConcurrency()
	startGate.Lock()
	if startGate.ch == nil || startGate.size != size {
		startGate.ch = make(chan struct{}, size)
		startGate.size = size
	}
	ch := startGate.ch
	startGate.Unlock()

	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-ch })
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for tmux startup slot for %s session %q: %w", provider, sessionName, ctx.Err())
	}
}

func configuredStartConcurrency() int {
	value := strings.TrimSpace(os.Getenv(EnvStartConcurrency))
	if value == "" {
		return defaultStartConcurrency
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return defaultStartConcurrency
	}
	return parsed
}

func PromptWait(providerEnvKey string) time.Duration {
	if parsed, ok := durationFromEnv(providerEnvKey); ok {
		return parsed
	}
	if parsed, ok := durationFromEnv(EnvPromptWaitSeconds); ok {
		return parsed
	}
	return defaultPromptWait
}

func Retention(fallback time.Duration) time.Duration {
	if parsed, ok := durationFromEnv(EnvRetentionSeconds); ok {
		return parsed
	}
	return fallback
}

// WithHistoryLimit configures tmux's global default immediately before the
// new-session command in the same tmux invocation. tmux copies history-limit
// into a pane when that pane is created; setting the session option after
// new-session is too late for the session's first pane.
func WithHistoryLimit(newSessionArgs []string, historyLimit string) []string {
	historyLimit = strings.TrimSpace(historyLimit)
	if historyLimit == "" {
		return append([]string(nil), newSessionArgs...)
	}
	args := []string{"set-option", "-g", "history-limit", historyLimit, ";"}
	return append(args, newSessionArgs...)
}

func durationFromEnv(key string) (time.Duration, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false
	}
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return time.Duration(parsed) * time.Second, true
}
