package agycli

import (
	"sync/atomic"
	"testing"
)

func TestAgyYieldIdleMountedSessionsKeepsActiveTurn(t *testing.T) {
	var releases atomic.Int32
	newSession := func(owner string, active bool) *agyInteractiveSession {
		session := &agyInteractiveSession{
			ownerSessionID:   owner,
			tmuxSessionName:  agySanitizeTmuxName(owner),
			mountFingerprint: "mounted-yield-test",
			releaseMounts:    func() { releases.Add(1) },
		}
		if active {
			session.turnLeases.Store(1)
		}
		agyInteractiveRegistry.Lock()
		agyInteractiveRegistry.sessions[owner] = session
		agyInteractiveRegistry.Unlock()
		return session
	}
	idle := newSession(t.Name()+"-idle", false)
	active := newSession(t.Name()+"-active", true)
	t.Cleanup(func() {
		CloseAgyCLIInteractiveSessionForOwner(active.ownerSessionID, "test cleanup")
	})

	agyYieldIdleMountedSessions("mounted-yield-test")
	if _, ok := activeAgyInteractiveSession(idle.ownerSessionID); ok {
		t.Fatal("idle sidecar retained a conflicting global mount")
	}
	if _, ok := activeAgyInteractiveSession(active.ownerSessionID); !ok {
		t.Fatal("active turn was interrupted to release its mount")
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("mount releases = %d, want 1", got)
	}

	active.turnLeases.Add(-1)
	agyYieldIdleMountedSessions("mounted-yield-test")
	if _, ok := activeAgyInteractiveSession(active.ownerSessionID); ok {
		t.Fatal("completed turn did not yield its mount")
	}
	if got := releases.Load(); got != 2 {
		t.Fatalf("mount releases = %d, want 2", got)
	}
}
