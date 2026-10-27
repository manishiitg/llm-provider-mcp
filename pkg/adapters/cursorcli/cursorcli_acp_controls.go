package cursorcli

import (
	"context"
	"fmt"
)

// Close, cleanup and Stop reach whichever transport owns the chat: the ACP process or the SDK bridge.
func CloseACP(owner string) { closeCursorACP(owner); closeCursorSDKInstance(owner, nil) }
func CleanupACP(ctx context.Context) error {
	cursorACPSessions.Lock()
	owners := make([]string, 0, len(cursorACPSessions.sessions))
	for owner := range cursorACPSessions.sessions {
		owners = append(owners, owner)
	}
	cursorACPSessions.Unlock()
	cursorSDKSessions.Lock()
	for owner := range cursorSDKSessions.sessions {
		owners = append(owners, owner)
	}
	cursorSDKSessions.Unlock()
	for _, owner := range owners {
		if err := ctx.Err(); err != nil {
			return err
		}
		CloseACP(owner)
	}
	return nil
}
func InterruptACP(ctx context.Context, owner, key string) error {
	if key != "Escape" && key != "C-c" {
		return fmt.Errorf("Cursor supports cancellation only")
	}
	cursorSDKSessions.Lock()
	sdk := cursorSDKSessions.sessions[owner]
	cursorSDKSessions.Unlock()
	if sdk != nil {
		sdk.mu.Lock()
		p, running := sdk.proc, sdk.running
		sdk.mu.Unlock()
		if p == nil || !running {
			return fmt.Errorf("no running Cursor turn")
		}
		return p.send(map[string]any{"type": "cancel"})
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

func CloseCursorCLIInteractiveSessionForOwner(owner, reason string) { CloseACP(owner) }
func CleanupCursorCLIInteractiveSessions(ctx context.Context) error { return CleanupACP(ctx) }
func InteractiveSessionRegistered(owner string) bool {
	cursorSDKSessions.Lock()
	sdk := cursorSDKSessions.sessions[owner] != nil
	cursorSDKSessions.Unlock()
	cursorACPSessions.Lock()
	defer cursorACPSessions.Unlock()
	return sdk || cursorACPSessions.sessions[owner] != nil
}
