package picli

import (
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type piRetainedProgress struct {
	mu           sync.Mutex
	turnStart    time.Time
	messageCount int
}

// ReadRetainedTurnProgressMessages returns newly committed assistant blocks,
// including text alongside tool calls. Token fragments stay in Pi's native
// stream; a completed block becomes one durable chat update.
func ReadRetainedTurnProgressMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	if turnStart.IsZero() {
		return nil
	}
	session, ok := activePiInteractiveSession(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return nil
	}
	progress := &session.retainedProgress
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if !progress.turnStart.Equal(turnStart) {
		progress.turnStart, progress.messageCount = turnStart, 0
	}
	messages := ReadRetainedTurnMessages(ownerSessionID, turnStart)
	if len(messages) <= progress.messageCount {
		return nil
	}
	newMessages := messages[progress.messageCount:]
	progress.messageCount = len(messages)
	var updates []llmtypes.MessageContent
	for _, message := range newMessages {
		if message.Role != llmtypes.ChatMessageTypeAI {
			continue
		}
		for _, part := range message.Parts {
			if text, ok := part.(llmtypes.TextContent); ok && strings.TrimSpace(text.Text) != "" {
				updates = append(updates, llmtypes.TextPart(llmtypes.ChatMessageTypeAI, text.Text))
			}
		}
	}
	return updates
}

// ReadRetainedTurnMessages reconstructs one directly-injected turn from the Pi
// transcript owned by an already-running interactive session. It does not
// start, resume, or otherwise mutate the coding-agent process.
//
// PLAT-179. An earlier version of this function gated its result on pi's own
// tmux pane reporting "idle" text (piPaneReadyForInput), to keep a message
// bundling intermediate commentary with a pending tool call from being
// treated as the turn's final answer. That gate failed live: the pi CLI
// build under test never printed "idle" in its status line at all, so the
// gate never fired and a retained turn hung until the caller's own timeout --
// worse than the bug it was meant to fix, which at least returned promptly
// (with the wrong text). The correct fix does not need pane inspection: it
// is the transcript itself that already distinguishes a message with a
// pending tool call from a genuinely finished one, and that check now lives
// once, shared, in mcpagent's retainedturn.finalResponse (skips any AI
// message that also carries a ToolCall part) rather than duplicated per
// provider with a pane-specific heuristic.
func ReadRetainedTurnMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return nil
	}
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || session == nil {
		return nil
	}
	session.mu.Lock()
	nativeSessionID := session.nativeSessionID
	session.mu.Unlock()
	summary := readPiTranscriptSummary(nativeSessionID, turnStart)
	if summary == nil {
		return nil
	}
	return summary.Messages
}
