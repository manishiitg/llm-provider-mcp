package cursorcli

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errCursorComposerChanged = errors.New("Cursor composer changed before input delivery")

// The same controls serve both GenerateContent and direct retained-session
// follow-ups. Web approval remains opt-in; no broader approval policy is added.
type cursorRuntimeControls struct {
	autoApproveWebSearch bool
	lastSent             map[string]time.Time
}

func (c *cursorRuntimeControls) handle(ctx context.Context, sessionName, captured string) bool {
	controls := []struct {
		source   string
		visible  func(string) bool
		key      string
		interval time.Duration
		enabled  bool
	}{
		{"cursor-followups-send-now", hasCursorQueuedFollowupsSendPrompt, "C-m", time.Second, true},
		// Bridge-only child agents do not reliably inherit the scoped bridge.
		{"cursor-mode-switch-reject", hasCursorModeSwitchPrompt, "n", time.Second, true},
		{"cursor-mcp-approval", hasCursorMCPToolApprovalPrompt, "Tab", time.Second, true},
		{"cursor-web-approval", hasCursorWebAccessApprovalPrompt, "y", 2 * time.Second, c.autoApproveWebSearch},
	}
	for _, control := range controls {
		if !control.enabled || !control.visible(captured) {
			continue
		}
		if time.Since(c.lastSent[control.source]) >= control.interval {
			// Re-read inside the shared broker: a stale approval must never
			// inject a control key into the user's normal composer.
			if handled, err := sendCursorControlIfVisible(ctx, sessionName, control.source, control.visible, control.key); err == nil && handled {
				if c.lastSent == nil {
					c.lastSent = make(map[string]time.Time)
				}
				c.lastSent[control.source] = time.Now()
			}
		}
		return true
	}
	return false
}

type cursorRetainedControls struct {
	cancel context.CancelFunc
	done   chan struct{}
}

var cursorRetainedControlRegistry = struct {
	sync.Mutex
	sessions map[string]*cursorRetainedControls
}{sessions: make(map[string]*cursorRetainedControls)}

// Called while the provider session is locked, after a successful turn or
// launch-only return. Direct Send calls do not re-enter the response loop.
func startCursorRetainedControls(sessionName string, autoApproveWebSearch bool) {
	stopCursorRetainedControls(sessionName)
	ctx, cancel := context.WithCancel(context.Background())
	watcher := &cursorRetainedControls{cancel: cancel, done: make(chan struct{})}
	cursorRetainedControlRegistry.Lock()
	cursorRetainedControlRegistry.sessions[sessionName] = watcher
	cursorRetainedControlRegistry.Unlock()
	go func() {
		defer func() {
			cancel()
			cursorRetainedControlRegistry.Lock()
			if cursorRetainedControlRegistry.sessions[sessionName] == watcher {
				delete(cursorRetainedControlRegistry.sessions, sessionName)
			}
			cursorRetainedControlRegistry.Unlock()
			close(watcher.done)
		}()
		controls := cursorRuntimeControls{autoApproveWebSearch: autoApproveWebSearch}
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, pollCancel := context.WithTimeout(ctx, 2*time.Second)
				captured, err := captureCursorPane(pollCtx, sessionName)
				if err == nil {
					controls.handle(pollCtx, sessionName, captured)
				}
				pollCancel()
				if isCursorTmuxSessionLostError(err) {
					return
				}
			}
		}
	}()
}

// Stop before re-entering GenerateContent or unregistering/closing a session.
// Waiting for done prevents the old handler from racing a changed policy.
func stopCursorRetainedControls(sessionName string) {
	cursorRetainedControlRegistry.Lock()
	watcher := cursorRetainedControlRegistry.sessions[sessionName]
	delete(cursorRetainedControlRegistry.sessions, sessionName)
	cursorRetainedControlRegistry.Unlock()
	if watcher != nil {
		watcher.cancel()
		<-watcher.done
	}
}
