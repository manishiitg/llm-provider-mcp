package claudecode

import (
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type claudeRetainedProgress struct {
	mu           sync.Mutex
	turnStart    time.Time
	offset       int64
	pendingTools map[string]time.Time
}

// ReadRetainedTurnProgressMessages publishes newly committed assistant text
// while a directly submitted turn is still running. It never decides whether
// the turn is complete; ReadRetainedTurnMessages owns that separate decision.
func ReadRetainedTurnProgressMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	if turnStart.IsZero() {
		return nil
	}
	session, ok := claudeInteractivePersistentRegistry.Get(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return nil
	}
	session.mu.Lock()
	nativeSessionID, workingDir := session.nativeSessionID, session.workingDir
	session.mu.Unlock()
	progress := &session.retainedProgress
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if !progress.turnStart.Equal(turnStart) {
		progress.turnStart, progress.offset = turnStart, 0
		progress.pendingTools = map[string]time.Time{}
	}
	path, err := resolveClaudeTranscriptPath(nativeSessionID, workingDir, true)
	if err != nil || path == "" {
		return nil
	}
	rows, next, err := readClaudeTranscriptEventsFromFile(path, progress.offset, turnStart, progress.pendingTools)
	if err != nil {
		return nil
	}
	progress.offset = next
	var messages []llmtypes.MessageContent
	for _, row := range rows {
		if strings.TrimSpace(row.Text) != "" {
			messages = append(messages, llmtypes.TextPart(llmtypes.ChatMessageTypeAI, row.Text))
		}
	}
	return messages
}

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
