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
	// PLAT-106: resolve THIS session's rollout, not the newest rollout that
	// happens to share its working directory. A workflow's Chat and Schedule run
	// in the same directory, so a directory match could return the other
	// conversation's final answer — which the host then stamped with this
	// session's IDs, making the leak invisible to every downstream consumer.
	rolloutPath := resolveCodexRolloutPath(session, turnStart)
	if rolloutPath == "" {
		return nil
	}
	finalText, _ := readCodexRolloutFinalAssistantText(rolloutPath, turnStart)
	if strings.TrimSpace(finalText) == "" {
		return nil
	}
	return []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeAI, finalText),
	}
}
