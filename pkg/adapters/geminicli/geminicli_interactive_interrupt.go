package geminicli

import (
	"context"
	"fmt"
)

// SendGeminiInteractiveControlKey injects a tmux control key (e.g. "Escape",
// "C-c") into a registered Gemini CLI interactive session without sending Enter.
// Returns an error if no session is registered for the owner.
func SendGeminiInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	sessionName, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Gemini interactive session registered for owner session %s", ownerSessionID)
	}
	return runGeminiCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, key)
}
