package agycli

import (
	"context"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type agyRetainedState struct {
	sentAt        time.Time
	progressText  string
	settledIdx    int
	settledAnswer string
	settledAt     time.Time
}

func agyLatestRetainedRecord(session *agyInteractiveSession) (agyPendingDurableAck, agyTurnRecord, bool) {
	session.durableMu.Lock()
	if len(session.pendingDurable) == 0 {
		session.durableMu.Unlock()
		return agyPendingDurableAck{}, agyTurnRecord{}, false
	}
	receipt := session.pendingDurable[len(session.pendingDurable)-1]
	session.durableMu.Unlock()
	conversationID := receipt.conversationID
	if conversationID == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		conversationID = agyConversationIDFromPane(ctx, session.tmuxSessionName)
		cancel()
	}
	if conversationID == "" {
		conversationID = agyDiscoverConversationID(session.createdAt, receipt.message)
	}
	if conversationID == "" {
		return receipt, agyTurnRecord{}, false
	}
	indices, err := agyMatchingUserStepIndices(conversationID, receipt.baselineIdx, receipt.message)
	if err != nil || len(indices) < receipt.occurrence {
		return receipt, agyTurnRecord{}, false
	}
	// The answer reader starts after the user row, never at the pre-send
	// baseline where a prior turn's assistant may still be present.
	answer, err := agyReadTurnRecord(conversationID, indices[receipt.occurrence-1], "")
	return receipt, answer, err == nil
}

// ReadRetainedTurnProgressMessages returns newly committed assistant text
// from AGY's conversation record. The pane remains a terminal display only.
func ReadRetainedTurnProgressMessages(ownerSessionID string, _ time.Time) []llmtypes.MessageContent {
	session, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok {
		return nil
	}
	receipt, record, ok := agyLatestRetainedRecord(session)
	if !ok || record.answer == "" {
		return nil
	}
	session.retainedMu.Lock()
	defer session.retainedMu.Unlock()
	state := &session.retainedState
	if !state.sentAt.Equal(receipt.sentAt) {
		*state = agyRetainedState{sentAt: receipt.sentAt}
	}
	if state.progressText == record.answer {
		return nil
	}
	chunk := strings.TrimPrefix(record.answer, state.progressText)
	if !strings.HasPrefix(record.answer, state.progressText) {
		chunk = record.answer
	}
	state.progressText = record.answer
	if strings.TrimSpace(chunk) == "" {
		return nil
	}
	return []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeAI, chunk)}
}

// ReadRetainedTurnMessages polls the current live-input turn's SQLite trail.
// A matching user row must exist, and a completed assistant row must end a
// stable trail while the composer is ready. Unlike the old reader, it never
// returns prompt echo, thought text or tool renderings from the pane.
func ReadRetainedTurnMessages(ownerSessionID string, _ time.Time) []llmtypes.MessageContent {
	session, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok {
		return nil
	}
	receipt, record, ok := agyLatestRetainedRecord(session)
	if !ok {
		return nil
	}
	session.retainedMu.Lock()
	state := &session.retainedState
	if !state.sentAt.Equal(receipt.sentAt) {
		*state = agyRetainedState{sentAt: receipt.sentAt}
	}
	if record.lastType != agyStepAssistant || record.lastStatus != 3 || record.answer == "" {
		state.settledAt = time.Time{}
		session.retainedMu.Unlock()
		return nil
	}
	if record.lastIdx != state.settledIdx || record.answer != state.settledAnswer {
		state.settledIdx, state.settledAnswer, state.settledAt = record.lastIdx, record.answer, time.Now()
	}
	settled := time.Since(state.settledAt) >= 2*time.Second
	session.retainedMu.Unlock()
	if !settled {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	pane, err := captureAgyPane(ctx, session.tmuxSessionName)
	cancel()
	if err != nil || agyApprovalMarkerShown(pane) != "" || !PaneReadyForInput(pane) {
		return nil
	}
	return []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeAI, record.answer)}
}
