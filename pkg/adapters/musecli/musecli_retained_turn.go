package musecli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type museRetainedProgress struct {
	mu        sync.Mutex
	turnStart time.Time
	lastSeq   int64
}

// ReadRetainedTurnProgressMessages exposes committed text and visible status
// summaries even while the TUI is busy. Completion keeps its existing gate.
func ReadRetainedTurnProgressMessages(ownerSessionID string, turnStart time.Time) []llmtypes.MessageContent {
	if turnStart.IsZero() {
		return nil
	}
	key, err := musePersistentKey(ownerSessionID)
	if err != nil {
		return nil
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	if entry == nil {
		musePersistentPool.Unlock()
		return nil
	}
	path, baseline := entry.logPath, entry.retainedBaselineSequence
	if path == "" && entry.nativeSessionID != "" {
		path = museSessionLogPath(entry.nativeSessionID)
	}
	musePersistentPool.Unlock()
	progress := &entry.retainedProgress
	progress.mu.Lock()
	defer progress.mu.Unlock()
	if !progress.turnStart.Equal(turnStart) {
		progress.turnStart, progress.lastSeq = turnStart, baseline
	}
	raw, err := os.ReadFile(path) //nolint:gosec // adapter-owned transcript path
	if err != nil {
		return nil
	}
	var messages []llmtypes.MessageContent
	// Only newline-committed records advance the cursor.
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines[:len(lines)-1] {
		var envelope struct {
			Sequence int64 `json:"sequence"`
		}
		if json.Unmarshal([]byte(line), &envelope) != nil || envelope.Sequence <= progress.lastSeq {
			continue
		}
		progress.lastSeq = envelope.Sequence
		for _, chunk := range museTranscriptLineToChunks(line, map[string]bool{}, map[string]bool{}, nil) {
			if (chunk.Type == llmtypes.StreamChunkTypeContent || chunk.Metadata["presentation"] == "assistant_update") && strings.TrimSpace(chunk.Content) != "" {
				messages = append(messages, llmtypes.TextPart(llmtypes.ChatMessageTypeAI, chunk.Content))
			}
		}
	}
	return messages
}

var museRetainedTurnReady = func(tmuxName, logPath string) bool {
	if tmuxName == "" || logPath == "" || !museLogQuietSince(logPath, 5*time.Second) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pane, err := museTmuxCapturePane(ctx, tmuxName)
	return err == nil && musePendingUserInputError(pane) == nil && museTUIAtPrompt(pane) && musePaneStable(ctx, tmuxName, pane)
}

// ReadRetainedTurnMessages returns the committed final response for a prompt
// injected into an idle persistent Muse TUI. A sequence cursor captured before
// submission prevents the previous answer from completing the new turn. The
// idle-pane and quiet-log gate matches Muse's normal bounded-turn completion
// contract, so intermediate tool-call model commits are never surfaced as the
// final chat response.
func ReadRetainedTurnMessages(ownerSessionID string, _ time.Time) []llmtypes.MessageContent {
	key, err := musePersistentKey(ownerSessionID)
	if err != nil {
		return nil
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	if entry == nil {
		musePersistentPool.Unlock()
		return nil
	}
	tmuxName := entry.tmuxName
	logPath := entry.logPath
	if logPath == "" && entry.nativeSessionID != "" {
		logPath = museSessionLogPath(entry.nativeSessionID)
	}
	baseline := entry.retainedBaselineSequence
	autoAnswer := entry.autoAnswer
	musePersistentPool.Unlock()
	// Live-input turns have no GenerateContent waiter. Service the same
	// native controls here before asking whether the retained turn is done.
	if autoAnswer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ctx = context.WithValue(ctx, museAutoAnswerKey{}, autoAnswer)
		pane, captureErr := museTmuxCapturePane(ctx, tmuxName)
		pending, questionErr := false, captureErr
		if captureErr == nil {
			pending, questionErr = museHandlePendingQuestion(ctx, tmuxName, pane)
		}
		cancel()
		if pending || questionErr != nil {
			return nil
		}
	}
	if !museRetainedTurnReady(tmuxName, logPath) {
		return nil
	}

	raw, err := os.ReadFile(logPath) //nolint:gosec // adapter-owned transcript path
	if err != nil {
		return nil
	}
	var final string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		sequence, _ := rec["sequence"].(float64)
		if int64(sequence) <= baseline {
			continue
		}
		payload, _ := rec["payload"].(map[string]any)
		if payload == nil || museLogString(payload, "kind") != "run" {
			continue
		}
		event, _ := payload["event"].(map[string]any)
		if event == nil || museLogString(event, "kind") != "assistant_message_committed" {
			continue
		}
		if text := strings.TrimSpace(museLogString(event, "text")); text != "" {
			final = text
		}
	}
	if final == "" {
		return nil
	}
	return []llmtypes.MessageContent{{
		Role:  llmtypes.ChatMessageTypeAI,
		Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: final}},
	}}
}
