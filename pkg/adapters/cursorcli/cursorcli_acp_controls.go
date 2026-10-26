package cursorcli

import (
	"context"
	"fmt"
)

func CloseACP(owner string) { closeCursorACP(owner) }
func CleanupACP(ctx context.Context) error {
	cursorACPSessions.Lock()
	owners := make([]string, 0, len(cursorACPSessions.sessions))
	for owner := range cursorACPSessions.sessions {
		owners = append(owners, owner)
	}
	cursorACPSessions.Unlock()
	for _, owner := range owners {
		if err := ctx.Err(); err != nil {
			return err
		}
		closeCursorACP(owner)
	}
	return nil
}
func InterruptACP(ctx context.Context, owner, key string) error {
	if key != "Escape" && key != "C-c" {
		return fmt.Errorf("Cursor supports cancellation only")
	}
	cursorACPSessions.Lock()
	s := cursorACPSessions.sessions[owner]
	cursorACPSessions.Unlock()
	if s == nil {
		return fmt.Errorf("no active Cursor session")
	}
	s.mu.Lock()
	r, native := s.rpc, s.native
	s.mu.Unlock()
	if r == nil {
		return fmt.Errorf("Cursor is starting")
	}
	return r.writeContext(ctx, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": native}})
}

func CloseCursorCLIInteractiveSessionForOwner(owner, reason string) { closeCursorACP(owner) }
func CleanupCursorCLIInteractiveSessions(ctx context.Context) error { return CleanupACP(ctx) }
func InteractiveSessionRegistered(owner string) bool {
	cursorACPSessions.Lock()
	defer cursorACPSessions.Unlock()
	return cursorACPSessions.sessions[owner] != nil
}
