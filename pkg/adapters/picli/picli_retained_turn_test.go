package picli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-177 (mcp-agent-builder-go). mcpagent's startRetainedCompletionWatch
// (agent/turn_session.go) polls ReadRetainedTurnMessages every 100ms and
// declares a retained (tmux-delivered) coding-agent turn complete the instant
// it returns any non-empty assistant text -- no check that pi is actually
// done, no check for a pending tool call. Confirmed live: a prompt explicitly
// asking for a progress update ("intermediate commentary, not your final
// answer") BEFORE a tool call had that progress update itself declared the
// final result, closing the turn ~2.2s in -- before the tool's own `sleep 2`
// could have finished.
//
// The fix belongs here, not in mcpagent's generic poll loop: pi's own status
// line already exposes genuine liveness (piPaneReadyForInput, used correctly
// by the ordinary non-retained turn flow at line 1250) and this reader was
// simply never consulting it. ReadRetainedTurnMessages must now return
// nothing until the pane confirms pi is actually idle -- mirroring the exact
// check the healthy turn path already trusts, not inventing a new signal.
func registerFakePiInteractiveSessionForTest(t *testing.T, ownerSessionID, nativeSessionID, tmuxSessionName string) {
	t.Helper()
	piInteractiveRegistry.Lock()
	piInteractiveRegistry.sessions[ownerSessionID] = &piInteractiveSession{
		ownerSessionID:  ownerSessionID,
		nativeSessionID: nativeSessionID,
		tmuxSessionName: tmuxSessionName,
	}
	piInteractiveRegistry.Unlock()
	t.Cleanup(func() {
		piInteractiveRegistry.Lock()
		delete(piInteractiveRegistry.sessions, ownerSessionID)
		piInteractiveRegistry.Unlock()
	})
}

func writeFakeRetainedTranscript(t *testing.T, sessionDir, nativeSessionID string, turnStart time.Time, assistantText string) {
	t.Helper()
	transcript := filepath.Join(sessionDir, "2026-08-22T10-00-00-000Z_"+nativeSessionID+".jsonl")
	inTurn := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	line := `{"type":"message","timestamp":"` + inTurn + `","message":{"role":"assistant","content":[{"type":"text","text":"` + assistantText + `"}],"stopReason":"stop"}}`
	if err := os.WriteFile(transcript, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write fake transcript: %v", err)
	}
}

func TestReadRetainedTurnMessagesWithholdsResultUntilPaneIsIdle(t *testing.T) {
	sessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", sessionDir)

	const ownerSessionID = "plat177-owner-session"
	const nativeSessionID = "plat177-native-session"
	registerFakePiInteractiveSessionForTest(t, ownerSessionID, nativeSessionID, "mlp-pi-cli-int-plat177")

	turnStart := time.Now().UTC().Add(-time.Minute)
	writeFakeRetainedTranscript(t, sessionDir, nativeSessionID, turnStart, "progress update, not the final answer")

	original := piRetainedTurnPaneReady
	t.Cleanup(func() { piRetainedTurnPaneReady = original })

	// Pane still busy (the model is mid-turn, about to call a tool): the
	// transcript already has non-empty assistant text, but the turn must NOT
	// be reported as final yet.
	piRetainedTurnPaneReady = func(ctx context.Context, tmuxSessionName string) bool { return false }
	if got := ReadRetainedTurnMessages(ownerSessionID, turnStart); got != nil {
		t.Fatalf("expected no messages while pane is busy, got %#v", got)
	}

	// Pane now genuinely idle: the same transcript content is now trustworthy
	// as the final result.
	piRetainedTurnPaneReady = func(ctx context.Context, tmuxSessionName string) bool { return true }
	got := ReadRetainedTurnMessages(ownerSessionID, turnStart)
	if len(got) != 1 {
		t.Fatalf("expected 1 message once pane is idle, got %d: %#v", len(got), got)
	}
	part, ok := got[0].Parts[0].(llmtypes.TextContent)
	if !ok || !strings.Contains(part.Text, "progress update") {
		t.Fatalf("expected the transcript's assistant text once idle, got %#v", got[0].Parts)
	}
}

func TestReadRetainedTurnMessagesReturnsNothingWhenPaneCheckFails(t *testing.T) {
	sessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", sessionDir)

	const ownerSessionID = "plat177-owner-session-2"
	const nativeSessionID = "plat177-native-session-2"
	registerFakePiInteractiveSessionForTest(t, ownerSessionID, nativeSessionID, "mlp-pi-cli-int-plat177-2")

	turnStart := time.Now().UTC().Add(-time.Minute)
	writeFakeRetainedTranscript(t, sessionDir, nativeSessionID, turnStart, "some text")

	original := piRetainedTurnPaneReady
	t.Cleanup(func() { piRetainedTurnPaneReady = original })

	// A capture failure (tmux transiently unavailable) must fail closed -- an
	// unconfirmed pane state is not evidence the turn is done. mcpagent's
	// poller (100ms tick) will simply try again on the next tick.
	piRetainedTurnPaneReady = func(ctx context.Context, tmuxSessionName string) bool { return false }
	if got := ReadRetainedTurnMessages(ownerSessionID, turnStart); got != nil {
		t.Fatalf("expected fail-closed (no messages) on an unconfirmed pane state, got %#v", got)
	}
}
