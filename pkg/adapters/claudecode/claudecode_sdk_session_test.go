package claudecode

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// PLAT-872: a kept session may take the next turn only with the same launch
// and the same conversation; anything else starts a fresh session.
func TestClaudeSDKReuseDecision(t *testing.T) {
	a, b := [32]byte{1}, [32]byte{2}
	for _, tc := range []struct {
		name           string
		kept, wanted   [32]byte
		keptID, resume string
		reuse          bool
		reason         string
	}{
		{"same launch and conversation", a, a, "s1", "s1", true, ""},
		{"launch changed", a, b, "s1", "s1", false, "launch_changed"},
		{"new conversation", a, a, "s1", "", false, "new_conversation"},
		{"other conversation", a, a, "s1", "s2", false, "other_conversation"},
	} {
		reuse, reason := claudeSDKReuseDecision(tc.kept, tc.wanted, tc.keptID, tc.resume)
		if reuse != tc.reuse || reason != tc.reason {
			t.Errorf("%s: got %t %q, want %t %q", tc.name, reuse, reason, tc.reuse, tc.reason)
		}
	}
}

// The background line is shown in chat: inline NAME=value assignments can carry
// secret values and must never reach it.
func TestClaudeSDKBackgroundCommandLabelHidesInlineValues(t *testing.T) {
	for in, want := range map[string]string{
		"API_KEY=abc123 PORT=3000 npm run dev": "npm run dev",
		"export TOKEN='x y' && npm start":      "&& npm start",
		"DB_URL=postgres://u:p@h/db":           "",
		"npm run dev\nsecond line":             "npm run dev",
		strings.Repeat("a", 200):               strings.Repeat("a", 119) + "…",
	} {
		if got := claudeBackgroundCommandLabel(in); got != want {
			t.Errorf("label(%q) = %q, want %q", in, got, want)
		}
	}
}

// A kept session closes itself when its last background task ends, and a
// kept session whose launch no longer matches is closed (its tasks stopped)
// instead of taking the turn. A fake bridge stands in for the SDK.
func TestClaudeSDKKeptSessionLifecycle(t *testing.T) {
	var mu sync.Mutex
	var closed []string
	SetSDKBackgroundListener(func(u SDKBackgroundUpdate) {
		mu.Lock()
		defer mu.Unlock()
		if u.Closed != "" {
			closed = append(closed, u.Owner+":"+u.Closed)
		}
	})
	defer SetSDKBackgroundListener(nil)

	// The fake bridge runs its opening (for example: wait for a trigger line,
	// then report an empty task list), then exits on stop_background like the
	// real bridge between turns.
	start := func(owner, opening string) *claudeSDKSession {
		life, kill := context.WithCancel(context.Background())
		cmd := exec.CommandContext(life, "sh", "-c", opening+` while read line; do case "$line" in *stop_background*) exit 0;; esac; done`)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		s, err := startClaudeSDKSession(owner, [32]byte{7}, cmd, stdin, stdout, &claudeLockedBuffer{}, kill, func() {}, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.setNativeID("native-1")
		s.setTasks([]SDKBackgroundTask{{TaskID: "t1", Command: "npm run dev"}})
		s.park()
		return s
	}
	waitExit := func(s *claudeSDKSession) {
		select {
		case <-s.exited:
		case <-time.After(10 * time.Second):
			t.Fatal("kept session did not exit")
		}
	}

	finished := start("chat-finished", `read trigger; echo '{"type":"sdk_background","background":[]}';`)
	if err := finished.input.write(map[string]any{"type": "go"}); err != nil {
		t.Fatal(err)
	}
	waitExit(finished)

	replaced := start("chat-replaced", "")
	if s := takeKeptClaudeSDKSession("chat-replaced", [32]byte{8}, true, "native-1"); s != nil {
		t.Fatal("a kept session with another launch must not take the turn")
	}
	waitExit(replaced)

	kept := start("chat-kept", "")
	if s := takeKeptClaudeSDKSession("chat-kept", [32]byte{7}, true, "native-1"); s != kept {
		t.Fatal("an unchanged launch of the same conversation must reuse the kept session")
	}
	// A session that dies while it still lists tasks clears the chat's line.
	kept.kill()
	waitExit(kept)

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(closed, ","); got != "chat-finished:finished,chat-replaced:replaced,chat-kept:finished" {
		t.Fatalf("closed updates = %q", got)
	}
}
