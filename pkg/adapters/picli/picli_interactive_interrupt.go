package picli

import (
	"context"
	"fmt"
)

// SendPiInteractiveControlKey injects a tmux control key (for example Escape
// or C-c) into a registered Pi interactive session.
func SendPiInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Pi interactive session registered for owner session %s", ownerSessionID)
	}
	return tmuxexecRunPiControlKey(ctx, session.tmuxSessionName, key)
}

func tmuxexecRunPiControlKey(ctx context.Context, sessionName, key string) error {
	return runPiCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, key)
}
