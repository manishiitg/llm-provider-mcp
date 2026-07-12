package agycli

import (
	"context"
	"fmt"

	"github.com/manishiitg/multi-llm-provider-go/pkg/tmuxinput"
)

// SendAgyInteractiveControlKey injects a tmux control key (e.g. "Escape",
// "C-c") into a registered Antigravity CLI interactive session without sending Enter.
// Returns an error if no session is registered for the owner.
func SendAgyInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	sessionName, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Agy interactive session registered for owner session %s", ownerSessionID)
	}
	_, err := tmuxinput.Default.Do(ctx, tmuxinput.Request{
		SessionID: sessionName,
		Source:    "agy-cli-control",
		Priority:  tmuxinput.PriorityForKey(key),
	}, func(ctx context.Context) error {
		return runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, key)
	})
	return err
}
