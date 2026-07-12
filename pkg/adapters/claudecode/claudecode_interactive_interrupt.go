package claudecode

import (
	"context"
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/pkg/tmuxinput"
)

// SendClaudeCodeControlKey injects a tmux control key (e.g. "Escape", "C-c")
// into a registered Claude Code tmux interactive session without
// sending Enter. Returns an error if no session is registered for the owner.
func SendClaudeCodeControlKey(ctx context.Context, ownerSessionID, key string) error {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return fmt.Errorf("Claude Code owner session ID is required")
	}
	sessionName, ok := activeClaudeInteractiveOwner(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Claude Code tmux session registered for owner session %s", ownerSessionID)
	}
	_, err := tmuxinput.Default.Do(ctx, tmuxinput.Request{
		SessionID: sessionName,
		Source:    "claude-code-control",
		Priority:  tmuxinput.PriorityForKey(key),
	}, func(ctx context.Context) error {
		return runCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, key)
	})
	return err
}
