package opencodecli

import (
	"context"
	"fmt"
)

// CleanupOpenCodeCLIInteractiveSessions is a no-op because OpenCode CLI is a
// structured JSON provider, not a tmux-backed interactive provider.
func CleanupOpenCodeCLIInteractiveSessions(ctx context.Context) error {
	return nil
}

// SendOpenCodeInteractiveInput is unsupported because OpenCode CLI uses
// bounded structured JSON invocations instead of live tmux sessions.
func SendOpenCodeInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	return fmt.Errorf("opencode-cli uses structured JSON transport; live tmux input is not supported")
}

// SendOpenCodeInteractiveControlKey is unsupported for the same reason —
// OpenCode CLI does not run inside a persistent tmux session.
func SendOpenCodeInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	return fmt.Errorf("opencode-cli uses structured JSON transport; tmux control keys are not supported")
}
