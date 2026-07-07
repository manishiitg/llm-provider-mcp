package cursorcli

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestCleanupCursorCLIInteractiveSessionsDoesNotBlockOnBusySession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	session := &cursorInteractiveSession{
		ownerSessionID:  "busy-owner",
		tmuxSessionName: "mlp-cursor-cli-cleanup-busy-test",
	}
	session.mu.Lock()
	defer session.mu.Unlock()

	oldPersistent := cursorPersistentRegistry.Replace(map[string]*cursorInteractiveSession{
		session.ownerSessionID: session,
	})
	t.Cleanup(func() {
		cursorPersistentRegistry.Replace(oldPersistent)
	})

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- CleanupCursorCLIInteractiveSessions(ctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cleanup error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cleanup blocked on busy session mutex")
	}
}
