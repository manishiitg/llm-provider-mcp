package cursorcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCursorRetainedInputWaitsForItsOwnQueryAndKeepsFinalAcrossPolls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := filepath.Join(t.TempDir(), "workspace")
	oldQuery := `{"role":"user","content":[{"type":"text","text":"<user_query>change schedule</user_query>"}]}`
	oldFinal := `{"role":"assistant","content":[{"type":"text","text":"Latency runs every other day."}]}`
	newQuery := `{"role":"user","content":[{"type":"text","text":"<user_query>fix Slack readability</user_query>"}]}`
	tool := `{"role":"assistant","content":[{"type":"text","text":"Updating instructions"},{"type":"tool-call","toolCallId":"call-1","toolName":"Write","args":{}}]}`
	final := `{"role":"assistant","content":[{"type":"text","text":"The Slack card is now easier to scan."}]}`
	blobs := []string{oldQuery}
	path := writeCursorStoreFixture(t, cwd, "owned-session", blobs)
	input := newCursorRetainedInput(path, "fix Slack readability")
	update := func(next ...string) {
		t.Helper()
		blobs = append(blobs, next...)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		writeCursorStoreFixture(t, cwd, "owned-session", blobs)
	}
	// The previous turn flushes AFTER delivery of the new request.
	update(oldFinal)
	if got := readCursorRetainedInput(input); len(got) != 0 {
		t.Fatalf("old reply leaked: %+v", got)
	}
	update(newQuery)
	if got := readCursorRetainedInput(input); len(got) != 1 || got[0].Role != llmtypes.ChatMessageTypeHuman {
		t.Fatalf("query boundary missing: %+v", got)
	}
	update(tool)
	for i := 0; i < 2; i++ {
		got := readCursorRetainedInput(input)
		if len(got) != 2 {
			t.Fatalf("poll %d lost pending tool: %+v", i, got)
		}
		if _, ok := got[1].Parts[1].(llmtypes.ToolCall); !ok {
			t.Fatal("pending tool lost")
		}
	}
	// A newer nested helper's store must never replace the owned transcript.
	writeCursorStoreFixture(t, cwd, "image-helper", []string{newQuery, oldFinal})
	update(final)
	for i := 0; i < 2; i++ {
		got := readCursorRetainedInput(input)
		if len(got) != 3 || got[2].Parts[0].(llmtypes.TextContent).Text != "The Slack card is now easier to scan." {
			t.Fatalf("poll %d final = %+v", i, got)
		}
	}
	// Repeating the same prompt must not reuse its already committed answer.
	repeated := newCursorRetainedInput(path, "fix Slack readability")
	if got := readCursorRetainedInput(repeated); len(got) != 0 {
		t.Fatalf("repeated prompt reused old final: %+v", got)
	}
	update(`{"role":"user","content":[{"type":"text","text":"<timestamp>later</timestamp><user_query>fix Slack readability</user_query>"}]}`, final)
	if got := readCursorRetainedInput(repeated); len(got) != 2 {
		t.Fatalf("repeated prompt's new answer = %+v", got)
	}
}

func TestCursorRetainedReaderRequiresIdleAndDeliveryFailureRestoresBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pane, _ := retainedControlsFixture(t)
	cwd := t.TempDir()
	path := writeCursorStoreFixture(t, cwd, "owned", []string{
		`{"role":"user","content":[{"type":"text","text":"<user_query>fix Slack</user_query>"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"Slack fixed."}]}`,
	})
	input := &cursorRetainedInput{storeDB: path, query: "fix Slack"}
	session := &cursorInteractiveSession{tmuxSessionName: t.Name(), workingDir: cwd, retainedInput: input}
	session.setRetainedStore("owned")
	cursorPersistentRegistry.Set(t.Name(), session)
	registerCursorInteractiveSession(t.Name(), t.Name())
	t.Cleanup(func() {
		cursorPersistentRegistry.Delete(t.Name())
		unregisterCursorInteractiveSession(t.Name(), t.Name())
	})
	for _, busy := range []string{"Composing\nctrl+c to stop\n→ Add a follow-up\n", retainedWebApprovalPane} {
		if err := os.WriteFile(pane, []byte(busy), 0600); err != nil {
			t.Fatal(err)
		}
		if got := ReadRetainedTurnMessages(t.Name(), time.Now()); len(got) != 0 {
			t.Fatalf("busy pane returned final: %+v", got)
		}
		if got := ReadRetainedTurnProgressMessages(t.Name()); len(got) != 2 {
			t.Fatalf("busy pane hid committed progress: %+v", got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SendCursorInteractiveInput(ctx, t.Name(), "new request"); err == nil {
		t.Fatal("expected canceled delivery")
	}
	if session.retainedInput != input {
		t.Fatal("failed delivery replaced existing boundary")
	}
	if err := os.WriteFile(pane, []byte("→ Add a follow-up\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ReadRetainedTurnMessages(t.Name(), time.Now()); len(got) != 2 {
		t.Fatalf("idle final missing: %+v", got)
	}
	// Losing the known store must never redirect to a helper's newer database.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeCursorStoreFixture(t, cwd, "helper", []string{`{"role":"assistant","content":"wrong reply"}`})
	if got := ReadRetainedTurnMessages(t.Name(), time.Now()); len(got) != 0 {
		t.Fatalf("helper reply leaked: %+v", got)
	}
}
