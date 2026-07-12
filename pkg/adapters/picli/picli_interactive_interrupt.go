package picli

import (
	"context"
	"fmt"

	"github.com/manishiitg/multi-llm-provider-go/pkg/tmuxinput"
)

// SendPiInteractiveControlKey injects a tmux control key (for example Escape
// or C-c) into a registered Pi interactive session.
func SendPiInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Pi interactive session registered for owner session %s", ownerSessionID)
	}
	_, err := tmuxinput.Default.Do(ctx, tmuxinput.Request{
		SessionID: session.tmuxSessionName,
		Source:    "pi-cli-control",
		Priority:  tmuxinput.PriorityForKey(key),
	}, func(ctx context.Context) error {
		return tmuxexecRunPiControlKey(ctx, session.tmuxSessionName, key)
	})
	return err
}

func tmuxexecRunPiControlKey(ctx context.Context, sessionName, key string) error {
	return runPiCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, key)
}
