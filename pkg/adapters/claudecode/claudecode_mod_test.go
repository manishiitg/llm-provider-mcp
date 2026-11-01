package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeClaudeModTestEvents(t *testing.T, sessionName string, events ...claudeModEvent) {
	t.Helper()
	if _, ok := claudeModBaseFor(sessionName); !ok {
		dir := t.TempDir()
		claudeModBases.Store(sessionName, claudeModBase{dir: dir, inbox: filepath.Join(dir, "inbox"), private: true})
	}
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
// The mod's events name the new session and the retained terminal adopts it,
// but only a session of its own project folder: anything in the session can
// write events, so a named session without a transcript there is refused.
func TestClaudeModFollowsSessionSwitchAndQueuedTurns(t *testing.T) {
	sessionName := fmt.Sprintf("mod-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { removeClaudeModSessionDir(sessionName) })
	start := time.Now().Add(-time.Second)
	at := start.UnixMilli() + 10
	const oldID, newID, foreignID = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	writeClaudeModTestEvents(t, sessionName,
		claudeModEvent{Type: "turn_start", At: at, Seq: 1, SessionID: oldID},
		claudeModEvent{Type: "turn_complete", At: at + 1, Seq: 2, SessionID: oldID, Answer: "first"},
		// A message queued during the first answer starts the next turn.
		claudeModEvent{Type: "turn_start", At: at + 2, Seq: 3, SessionID: oldID},
		claudeModEvent{Type: "turn_complete", At: at + 3, Seq: 4, SessionID: oldID, Answer: "second"},
		claudeModEvent{Type: "session_end", At: at + 4, Seq: 5, SessionID: oldID, Reason: "clear"},
		claudeModEvent{Type: "command", At: at + 5, Seq: 6, SessionID: newID, Command: "clear"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	end, ok := waitForClaudeModTurnComplete(ctx, sessionName, start)
	if !ok || end.Answer != "second" {
		t.Fatalf("turn end = %+v, %v; want the queued turn's answer", end, ok)
	}

	home, workingDir := t.TempDir(), t.TempDir()
	session := &claudeInteractivePersistentSession{tmuxSessionName: sessionName, nativeSessionID: oldID, workingDir: workingDir, accountHome: home}
	adoptClaudeModSession(session, nil)
	if session.nativeSessionID != oldID {
		t.Fatalf("native session = %q, adopted a session with no transcript in this project", session.nativeSessionID)
	}
	transcript := claudeTranscriptWorkingDirCandidates(home, workingDir, newID)[0]
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adoptClaudeModSession(session, nil)
	if session.nativeSessionID != newID {
		t.Fatalf("native session = %q, want the one /clear moved to", session.nativeSessionID)
	}
	writeClaudeModTestEvents(t, sessionName, claudeModEvent{Type: "command", At: at + 6, Seq: 7, SessionID: foreignID})
	adoptClaudeModSession(session, nil)
	if session.nativeSessionID != newID {
		t.Fatalf("native session = %q, adopted a session named from elsewhere", session.nativeSessionID)
	}
}

// The mod runs each inbox request as the person (a prompt, a slash command),
// so in a confined launch the agent must not be able to write one: the inbox
// is read, never in the sandbox's write paths, while the events folder is.
func TestClaudeModInboxIsReadOnlyForTheConfinedAgent(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "cli-home", "claude-code")
	sessionName := fmt.Sprintf("mod-inbox-test-%d", time.Now().UnixNano())
	claudeModBases.Store(sessionName, claudeModBase{dir: filepath.Join(home, ".agentworks-mod"), inbox: filepath.Join(filepath.Dir(home), ".agentworks-mod-inbox"), shared: true})
	t.Cleanup(func() { claudeModBases.Delete(sessionName) })

	read, write := claudeModSandboxPaths([]string{"claude", "--plugin-dir", "/plugin"}, sessionName, nil, []string{home})
	inbox := claudeModInboxDir(sessionName)
	if !slices.Contains(read, inbox) {
		t.Fatalf("read paths %v do not include the inbox %s", read, inbox)
	}
	for _, path := range write {
		if rel, err := filepath.Rel(path, inbox); err == nil && !strings.HasPrefix(rel, "..") {
			t.Fatalf("inbox %s is writable through %s", inbox, path)
		}
	}
	if !slices.Contains(write, filepath.Join(claudeModSessionDir(sessionName), "events")) {
		t.Fatalf("write paths %v do not include the events folder", write)
	}
}
