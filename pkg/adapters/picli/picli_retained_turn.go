package picli

import (
	"context"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// piRetainedTurnPaneReadyTimeout bounds the tmux capture this reader performs
// on every mcpagent poll tick (100ms, turn_session.go). A local capture-pane
// call is fast; this only guards against a wedged/unresponsive tmux.
const piRetainedTurnPaneReadyTimeout = 3 * time.Second

// piRetainedTurnPaneReady reports whether pi's own tmux pane confirms it is
// genuinely idle, reusing the exact liveness check (piPaneReadyForInput) the
// ordinary non-retained turn flow already trusts. Package-level var so a test
// can substitute it without a real tmux session.
var piRetainedTurnPaneReady = func(ctx context.Context, tmuxSessionName string) bool {
	captured, err := capturePiPaneANSI(ctx, tmuxSessionName)
	if err != nil {
		return false
	}
	return piPaneReadyForInput(captured)
}

// ReadRetainedTurnMessages reconstructs one directly-injected turn from the Pi
// transcript owned by an already-running interactive session. It does not
// start, resume, or otherwise mutate the coding-agent process.
//
// PLAT-177. mcpagent's completion poller (startRetainedCompletionWatch,
// agent/turn_session.go) declares the turn done the instant this returns any
// non-empty result -- it has no way to know pi is still working. Before this
// fix, the transcript alone decided that: the first non-empty assistant
// message won, even when it was intermediate commentary a prompt explicitly
// labeled "not your final answer," sent before the tool call it was
// introducing. Confirmed live: a retained turn completed in ~2.2s with that
// commentary as its final_result, before the tool's own `sleep 2` could have
// finished.
//
// The transcript alone cannot distinguish "one message finished" from "the
// whole exchange is over" -- pi's own status line can, and the healthy
// non-retained turn flow already relies on exactly this signal
// (piPaneReadyForInput, picli_interactive_adapter.go:1250). This reader now
// withholds its result -- returns nil, which the poller already treats as
// "not done yet, keep waiting" -- until the pane confirms pi is actually
// idle, rather than inventing a new signal or changing the poller's own
// (already-correct) "empty means not done" contract.
func ReadRetainedTurnMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return nil
	}
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || session == nil {
		return nil
	}
	session.mu.Lock()
	nativeSessionID := session.nativeSessionID
	tmuxSessionName := session.tmuxSessionName
	session.mu.Unlock()
	summary := readPiTranscriptSummary(nativeSessionID, turnStart)
	if summary == nil || len(summary.Messages) == 0 {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(context.Background(), piRetainedTurnPaneReadyTimeout)
	defer cancel()
	if !piRetainedTurnPaneReady(readyCtx, tmuxSessionName) {
		return nil
	}
	return summary.Messages
}
