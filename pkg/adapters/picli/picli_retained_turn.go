package picli

import (
	"io"
	"strings"
	"sync"
	"time"

	"bufio"
	"encoding/json"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"os"
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
	return llmtypes.TranscriptProgressText(ReadRetainedTurnStructuredProgressMessages(ownerSessionID, turnStart), false, true)
}

func ReadRetainedTurnStructuredProgressMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
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
	session.mu.Lock()
	nativeSessionID := session.nativeSessionID
	sessionDir := session.sessionDir
	session.mu.Unlock()
	summary := readPiTranscriptSummaryInDir(sessionDir, nativeSessionID, turnStart)
	if summary == nil {
		return nil
	}
	messages := summary.StructuredProgressMessages
	if len(messages) <= progress.messageCount {
		return nil
	}
	newMessages := messages[progress.messageCount:]
	progress.messageCount = len(messages)
	var updates []llmtypes.MessageContent
	for _, message := range newMessages {
		if message.Role != llmtypes.ChatMessageTypeAI && message.Role != llmtypes.ChatMessageTypeTool {
			continue
		}
		for _, part := range message.Parts {
			switch part := part.(type) {
			case llmtypes.ToolCall, llmtypes.ToolCallResponse:
				updates = append(updates, llmtypes.MessageContent{Role: message.Role, Parts: []llmtypes.ContentPart{part}})
			case llmtypes.TextContent:
				if strings.TrimSpace(part.Text) != "" {
					updates = append(updates, llmtypes.TextPart(llmtypes.ChatMessageTypeAI, part.Text))
				}
			case llmtypes.ThinkingContent:
				if strings.TrimSpace(part.Thinking) != "" {
					updates = append(updates, llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{part}})
				}
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
	sessionDir := session.sessionDir
	session.mu.Unlock()
	summary := readPiTranscriptSummaryInDir(sessionDir, nativeSessionID, turnStart)
	if summary == nil {
		return nil
	}
	return summary.Messages
}

// RetainedTurnEndedWithoutReply reports that Pi's own record shows the turn that began at turnStart ended on an error
// stop with no reply: the person pressed Escape (Pi writes an empty assistant message, stopReason "error", "This
// operation was aborted") or the model call failed. ReadRetainedTurnMessages returns no final text for such a turn, so
// without this a retained turn typed into the running pane never settles and the chat stays Working. The stop reason is
// returned for the log.
func RetainedTurnEndedWithoutReply(ownerSessionID string, turnStart time.Time) (string, bool) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return "", false
	}
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || session == nil {
		return "", false
	}
	session.mu.Lock()
	nativeSessionID := session.nativeSessionID
	sessionDir := session.sessionDir
	session.mu.Unlock()
	if strings.TrimSpace(sessionDir) == "" {
		sessionDir = piTranscriptSessionDir()
	}
	path := latestPiTranscriptPathInDir(sessionDir, nativeSessionID)
	if path == "" {
		return "", false
	}
	return piTranscriptEndedWithoutReply(path, turnStart)
}

// piTranscriptEndedWithoutReply is true when the last record of the turn is an assistant message with an error stop
// reason and nothing in it: no text and no tool call (a pending tool call is still work).
func piTranscriptEndedWithoutReply(path string, turnStart time.Time) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	// The watch asks every poll, and only the last record decides: read the tail of the file, not all of it. A record
	// longer than the tail is a big tool result, never the empty aborted message.
	start := int64(0)
	if info, err := f.Stat(); err == nil && info.Size() > piEndedWithoutReplyTailBytes {
		start = info.Size() - piEndedWithoutReplyTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", false
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	if start > 0 {
		if _, err := reader.ReadBytes('\n'); err != nil { // the first line is cut by the seek
			return "", false
		}
	}
	lastRole, lastStop, lastEmpty := "", "", false
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var ev piTranscriptRecord
			if json.Unmarshal(line, &ev) == nil && ev.Type == "message" && ev.Message != nil && piTranscriptRecordInTurn(ev, turnStart) {
				if role := strings.ToLower(strings.TrimSpace(ev.Message.Role)); role != "" {
					lastRole, lastStop = role, strings.ToLower(strings.TrimSpace(ev.Message.StopReason))
					lastEmpty = true
					for _, part := range ev.Message.Content {
						if strings.TrimSpace(part.Text) != "" || strings.TrimSpace(part.Type) == "toolCall" {
							lastEmpty = false
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	if lastRole == "assistant" && lastEmpty && (lastStop == "error" || lastStop == "aborted") {
		return lastStop, true
	}
	return "", false
}

// piEndedWithoutReplyTailBytes is how much of the end of Pi's session file the ended-without-reply check reads.
const piEndedWithoutReplyTailBytes = 256 * 1024
