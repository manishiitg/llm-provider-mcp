package picli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestPiRetainedProgressPreservesMessagesAroundTools(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", dir)
	owner, native := t.Name(), "pi-progress-native"
	file, err := os.Create(filepath.Join(dir, "2026-09-12_"+native+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	piInteractiveRegistry.Lock()
	piInteractiveRegistry.sessions[owner] = &piInteractiveSession{ownerSessionID: owner, nativeSessionID: native, tmuxSessionName: "pi-retained-test"}
	piInteractiveRegistry.Unlock()
	t.Cleanup(func() {
		piInteractiveRegistry.Lock()
		delete(piInteractiveRegistry.sessions, owner)
		piInteractiveRegistry.Unlock()
	})
	start := time.Now().UTC()
	appendMessage := func(role, text string, timestamp time.Time, tool bool) {
		t.Helper()
		content := []map[string]interface{}{{"type": "text", "text": text}}
		if tool {
			content = append(content, map[string]interface{}{"type": "toolCall", "id": "call", "name": "read_file", "arguments": map[string]string{}})
		}
		row := map[string]interface{}{"type": "message", "timestamp": timestamp.Format(time.RFC3339Nano), "message": map[string]interface{}{"role": role, "content": content}}
		if err := json.NewEncoder(file).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	appendMessage("assistant", "Previous answer.", start.Add(-time.Minute), false)
	appendMessage("user", "Check the report.", start, false)
	for i := 1; i <= 2; i++ {
		appendMessage("assistant", "Checking the report.", start.Add(time.Duration(i)*time.Second), true)
		got := ReadRetainedTurnProgressMessages(owner, start)
		if len(got) != 1 || got[0].Parts[0].(llmtypes.TextContent).Text != "Checking the report." {
			t.Fatalf("missing block %d: %+v", i, got)
		}
		if got := ReadRetainedTurnProgressMessages(owner, start); len(got) != 0 {
			t.Fatalf("replayed block: %+v", got)
		}
		// Reading progress must not consume or strip the tool call from the
		// final-response reader's authoritative transcript.
		trail := ReadRetainedTurnMessages(owner, start)
		last := trail[len(trail)-1]
		if _, ok := last.Parts[1].(llmtypes.ToolCall); !ok {
			t.Fatalf("lost completion guard: %+v", last)
		}
	}
	appendMessage("assistant", "Report checked.", start.Add(3*time.Second), false)
	if got := ReadRetainedTurnProgressMessages(owner, start); len(got) != 1 || got[0].Parts[0].(llmtypes.TextContent).Text != "Report checked." {
		t.Fatalf("missing final flush: %+v", got)
	}
	if got := ReadRetainedTurnProgressMessages(owner, start.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("replayed previous turn: %+v", got)
	}
	if got := ReadRetainedTurnProgressMessages("another-owner", start); len(got) != 0 {
		t.Fatalf("leaked another session: %+v", got)
	}
}
