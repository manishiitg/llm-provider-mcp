package codexcli

import (
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// ReadRetainedTurnMessages returns the committed final answer for one
// directly-injected turn. Intermediate assistant commentary is deliberately
// excluded: Codex can emit commentary before calling tools, and treating that
// text as completion ends the retained turn while the CLI is still working.
// It does not start, resume, or otherwise mutate the coding-agent process.
func ReadRetainedTurnMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return nil
	}
	session, ok := codexPersistentRegistry.Get(ownerSessionID)
	if !ok || session == nil {
		return nil
	}
	session.mu.Lock()
	workingDir := session.workingDir
	session.mu.Unlock()
	finalText, _ := readCodexTranscriptFinalAssistantText(turnStart, workingDir)
	if strings.TrimSpace(finalText) == "" {
		return nil
	}
	return []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeAI, finalText),
	}
}
