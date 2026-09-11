package musecli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestReadMuseRetainedTurnMessagesUsesPostSubmissionCommit(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"sequence":5,"payload_type":"runtime.session","payload":{"kind":"run","run_id":"old","event":{"kind":"assistant_message_committed","text":"old answer"}}}` + "\n" +
		`{"sequence":11,"payload_type":"runtime.session","payload":{"kind":"run","run_id":"new","event":{"kind":"assistant_message_committed","text":"new answer"}}}` + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	owner := "muse-retained-test"
	musePersistentPool.Lock()
	previousEntry := musePersistentPool.m[owner]
	musePersistentPool.m[owner] = &musePersistentSession{
		tmuxName:                 "mlp-muse-retained-test",
		logPath:                  logPath,
		retainedBaselineSequence: 10,
	}
	musePersistentPool.Unlock()
	previousReady := museRetainedTurnReady
	museRetainedTurnReady = func(tmuxName, gotLogPath string) bool {
		return tmuxName == "mlp-muse-retained-test" && gotLogPath == logPath
	}
	t.Cleanup(func() {
		museRetainedTurnReady = previousReady
		musePersistentPool.Lock()
		if previousEntry == nil {
			delete(musePersistentPool.m, owner)
		} else {
			musePersistentPool.m[owner] = previousEntry
		}
		musePersistentPool.Unlock()
	})

	messages := ReadRetainedTurnMessages(owner, time.Now())
	if len(messages) != 1 || messages[0].Role != llmtypes.ChatMessageTypeAI {
		t.Fatalf("messages = %+v, want one AI message", messages)
	}
	if got := messageText(messages[0]); got != "new answer" {
		t.Fatalf("final text = %q, want new answer", got)
	}
}

func TestReadMuseRetainedTurnMessagesWaitsForReadyBoundary(t *testing.T) {
	owner := "muse-retained-not-ready"
	musePersistentPool.Lock()
	previousEntry := musePersistentPool.m[owner]
	musePersistentPool.m[owner] = &musePersistentSession{tmuxName: "mlp-muse-retained-not-ready", logPath: "unused"}
	musePersistentPool.Unlock()
	previousReady := museRetainedTurnReady
	museRetainedTurnReady = func(string, string) bool { return false }
	t.Cleanup(func() {
		museRetainedTurnReady = previousReady
		musePersistentPool.Lock()
		if previousEntry == nil {
			delete(musePersistentPool.m, owner)
		} else {
			musePersistentPool.m[owner] = previousEntry
		}
		musePersistentPool.Unlock()
	})

	if messages := ReadRetainedTurnMessages(owner, time.Now()); len(messages) != 0 {
		t.Fatalf("messages = %+v, want none before the ready boundary", messages)
	}
}
