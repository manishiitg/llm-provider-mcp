package claudecode

import (
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// ReadRetainedTurnMessages reconstructs one directly-injected turn from the
// Claude Code transcript owned by an already-running interactive session.
// It does not start, resume, or otherwise mutate the coding-agent process.
func ReadRetainedTurnMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return nil
	}
	session, ok := claudeInteractivePersistentRegistry.Get(ownerSessionID)
	if !ok || session == nil {
		return nil
	}
	session.mu.Lock()
	nativeSessionID := session.nativeSessionID
	workingDir := session.workingDir
	session.mu.Unlock()
	return readClaudeTranscriptMessages(nativeSessionID, workingDir, turnStart)
}
