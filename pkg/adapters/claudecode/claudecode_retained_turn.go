package claudecode

import (
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// ReadRetainedTurnMessages returns the committed final answer for one
// directly-injected turn from the Claude Code transcript owned by an
// already-running interactive session. Intermediate assistant narration is
// deliberately excluded: Claude attaches that text to messages whose
// stop_reason is tool_use, and treating it as completion ends the retained
// turn while the CLI is still working.
//
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
	response := completedAssistantResponseFromTranscript(nativeSessionID, workingDir, turnStart)
	if !response.Found || !response.Completed || strings.TrimSpace(response.Text) == "" {
		return nil
	}
	return []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeAI, response.Text),
	}
}
