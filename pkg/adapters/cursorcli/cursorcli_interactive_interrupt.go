package cursorcli

import (
	"context"
	"fmt"
)

// SendCursorInteractiveControlKey injects a tmux control key (e.g. "Escape",
// "C-c") into a registered Cursor CLI interactive session without sending Enter.
// Returns an error if no session is registered for the owner.
func SendCursorInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	sessionName, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Cursor interactive session registered for owner session %s", ownerSessionID)
	}
	return runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, key)
}
