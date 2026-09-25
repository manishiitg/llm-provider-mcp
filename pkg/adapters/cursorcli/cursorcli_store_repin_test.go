package cursorcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cursorUserQueryBlob(t *testing.T, query string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": []map[string]string{{"type": "text", "text": "<user_query>\n" + query + "\n</user_query>"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func repinFixture(t *testing.T) (workingDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	workingDir = filepath.Join(home, "projects", "flow-tester")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return workingDir
}

// RTS 2026-09-25: after a relaunch into a fresh native session two chats lived
// in the crew folder. The first turn's chat was pinned while the pane typed
// into the other, so every later message failed its ack and its reply never
// reached the chat. The chat that holds the sent message wins.
func TestRepinStoreFollowsTheChatThatReceivedTheMessage(t *testing.T) {
	workingDir := repinFixture(t)
	message := "did you setup  the pytohn scripts"
	pinned := writeCursorStoreFixture(t, workingDir, "first-turn-chat", []string{cursorUserQueryBlob(t, "update the memory and skills")})
	live := writeCursorStoreFixture(t, workingDir, "pane-chat", []string{cursorUserQueryBlob(t, message)})

	session := &cursorInteractiveSession{ownerSessionID: "owner", workingDir: workingDir, retainedWorkingDir: workingDir, retainedNativeID: "first-turn-chat", retainedStoreDB: pinned}
	session.retainedInput = newCursorRetainedInput(pinned, message)

	if got := session.repinStoreForMessage(message, time.Now().Add(-time.Minute)); got != live {
		t.Fatalf("repinned to %q, want the pane's chat %q", got, live)
	}
	if session.retainedNativeID != "pane-chat" || session.retainedStoreDB != live {
		t.Fatalf("session pin = %q %q", session.retainedNativeID, session.retainedStoreDB)
	}
	if session.retainedInput.storeDB != live || len(session.retainedInput.baseline) != 0 {
		t.Fatalf("pending input still reads the old chat: %+v", session.retainedInput)
	}
}

func TestRepinStoreKeepsAPinThatHoldsTheMessage(t *testing.T) {
	workingDir := repinFixture(t)
	message := "yes"
	pinned := writeCursorStoreFixture(t, workingDir, "pane-chat", []string{cursorUserQueryBlob(t, message)})
	writeCursorStoreFixture(t, workingDir, "other-chat", []string{cursorUserQueryBlob(t, message)})

	session := &cursorInteractiveSession{ownerSessionID: "owner", workingDir: workingDir, retainedWorkingDir: workingDir, retainedNativeID: "pane-chat", retainedStoreDB: pinned}
	if got := session.repinStoreForMessage(message, time.Now().Add(-time.Minute)); got != pinned {
		t.Fatalf("moved off a chat that holds the message: %q", got)
	}
}

func TestRepinStoreNeverGuessesBetweenSeveralChats(t *testing.T) {
	workingDir := repinFixture(t)
	message := "run the smoke test"
	pinned := writeCursorStoreFixture(t, workingDir, "first-turn-chat", []string{cursorUserQueryBlob(t, "something else")})
	writeCursorStoreFixture(t, workingDir, "chat-a", []string{cursorUserQueryBlob(t, message)})
	writeCursorStoreFixture(t, workingDir, "chat-b", []string{cursorUserQueryBlob(t, message)})

	session := &cursorInteractiveSession{ownerSessionID: "owner", workingDir: workingDir, retainedWorkingDir: workingDir, retainedNativeID: "first-turn-chat", retainedStoreDB: pinned}
	if got := session.repinStoreForMessage(message, time.Now().Add(-time.Minute)); got != pinned {
		t.Fatalf("guessed between two matching chats: %q", got)
	}
}
