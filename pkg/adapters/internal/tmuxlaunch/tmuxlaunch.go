package tmuxlaunch

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

const EnvStartConcurrency = "CODING_SDK_TMUX_START_CONCURRENCY"

const defaultStartConcurrency = 1

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
