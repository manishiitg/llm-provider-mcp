package claudecode

import (
	"context"
	"fmt"
	"strings"
)

// SendClaudeCodeExperimentalControlKey injects a tmux control key (e.g. "Escape",
// "C-c") into a registered Claude Code experimental interactive session without
// sending Enter. Returns an error if no session is registered for the owner.
func SendClaudeCodeExperimentalControlKey(ctx context.Context, ownerSessionID, key string) error {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return fmt.Errorf("Claude Code experimental owner session ID is required")
	}
	sessionName, ok := activeClaudeExperimentalInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Claude Code experimental session registered for owner session %s", ownerSessionID)
	}
	return runCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, key)
}
