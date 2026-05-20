package geminicli

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestCleanupGeminiCLIInteractiveSessionsDoesNotBlockOnBusySession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	session := &geminiInteractiveSession{
		ownerSessionID:  "busy-owner",
		tmuxSessionName: "mlp-gemini-cli-cleanup-busy-test",
	}
	session.mu.Lock()
	defer session.mu.Unlock()

	geminiPersistentRegistry.Lock()
	oldPersistent := geminiPersistentRegistry.sessions
	geminiPersistentRegistry.sessions = map[string]*geminiInteractiveSession{
		session.ownerSessionID: session,
	}
	geminiPersistentRegistry.Unlock()
	t.Cleanup(func() {
		geminiPersistentRegistry.Lock()
		geminiPersistentRegistry.sessions = oldPersistent
		geminiPersistentRegistry.Unlock()
	})

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done <- CleanupGeminiCLIInteractiveSessions(ctx)
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
