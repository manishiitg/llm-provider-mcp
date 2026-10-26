package codexcli

import (
	"context"
	"fmt"
)

// SendCodexInteractiveControlKey translates the interrupt actions supported by the native protocol.
func SendCodexInteractiveControlKey(ctx context.Context, ownerSessionID, key string) error {
	if key != "Escape" && key != "C-c" {
		return fmt.Errorf("Codex app-server does not accept terminal key %q", key)
	}
	return InterruptCodexAppServer(ctx, ownerSessionID)
}
