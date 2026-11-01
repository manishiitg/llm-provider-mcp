package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeClaudeModTestEvents(t *testing.T, sessionName string, events ...claudeModEvent) {
	t.Helper()
	dir, err := prepareClaudeModSessionDir(sessionName)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		data, _ := json.Marshal(event)
		name := fmt.Sprintf("%014d-%06d-%s.json", event.At, event.Seq, event.Type)
		if err := os.WriteFile(filepath.Join(dir, "events", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// /clear typed in the terminal moved Claude to a new session; the chat kept
// watching and resuming the old one (why typed slash commands were blocked).
// The mod's events name the new session and the retained terminal adopts it.
func TestClaudeModFollowsSessionSwitchAndQueuedTurns(t *testing.T) {
	sessionName := fmt.Sprintf("mod-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { removeClaudeModSessionDir(sessionName) })
	start := time.Now().Add(-time.Second)
	at := start.UnixMilli() + 10
	writeClaudeModTestEvents(t, sessionName,
		claudeModEvent{Type: "turn_start", At: at, Seq: 1, SessionID: "old"},
		claudeModEvent{Type: "turn_complete", At: at + 1, Seq: 2, SessionID: "old", Answer: "first"},
		// A message queued during the first answer starts the next turn.
		claudeModEvent{Type: "turn_start", At: at + 2, Seq: 3, SessionID: "old"},
		claudeModEvent{Type: "turn_complete", At: at + 3, Seq: 4, SessionID: "old", Answer: "second"},
		claudeModEvent{Type: "session_end", At: at + 4, Seq: 5, SessionID: "old", Reason: "clear"},
		claudeModEvent{Type: "command", At: at + 5, Seq: 6, SessionID: "new", Command: "clear"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	end, ok := waitForClaudeModTurnComplete(ctx, sessionName, start)
	if !ok || end.Answer != "second" {
		t.Fatalf("turn end = %+v, %v; want the queued turn's answer", end, ok)
	}

	session := &claudeInteractivePersistentSession{tmuxSessionName: sessionName, nativeSessionID: "old"}
	adoptClaudeModSession(session, nil)
	if session.nativeSessionID != "new" {
		t.Fatalf("native session = %q, want the one /clear moved to", session.nativeSessionID)
	}
}
