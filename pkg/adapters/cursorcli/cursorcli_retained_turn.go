package cursorcli

import (
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// ReadRetainedTurnMessages reconstructs one directly-injected turn from the
// Cursor store owned by an already-running interactive session. It does not
// start, resume, or otherwise mutate the coding-agent process.
func ReadRetainedTurnMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return nil
	}
	session, ok := cursorPersistentRegistry.Get(ownerSessionID)
	if !ok || session == nil {
		return nil
	}
	session.mu.Lock()
	workingDir := session.workingDir
	session.mu.Unlock()
	return readCursorTranscriptMessages(turnStart, workingDir, ownerSessionID)
}
