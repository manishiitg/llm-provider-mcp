package claudecode

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"strings"
	"time"
)

// The owner component survives backend restarts. Keep a unique launch suffix:
// delayed cleanup from an old turn must never kill its replacement.
func claudePersistentTmuxOwnerPrefix(owner string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(owner)))
	return fmt.Sprintf("%s-owner-%x-", claudeInteractiveSessionPrefix(), sum[:16])
}

func closePreviousClaudeTmuxCopies(ctx context.Context, owner string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	prefix := claudePersistentTmuxOwnerPrefix(owner)
	out, err := runCommandOutput(ctx, nil, "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if !isTmuxNoServerError(err) {
			log.Printf("[CLI_RUNTIME] old_copy_lookup_failed chat=%s err=%v", owner, err)
		}
		return
	}
	for _, name := range strings.Fields(out) {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if err := killClaudeInteractiveSession(ctx, name); err != nil {
			log.Printf("[CLI_RUNTIME] old_copy_close_failed chat=%s tmux=%s err=%v", owner, name, err)
			continue
		}
		log.Printf("[CLI_RUNTIME] old_copy_closed chat=%s tmux=%s", owner, name)
	}
}
