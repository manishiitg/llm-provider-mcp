package musecli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var museRetainedTurnReady = func(tmuxName, logPath string) bool {
	if tmuxName == "" || logPath == "" || !museLogQuietSince(logPath, 5*time.Second) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pane, err := museTmuxCapturePane(ctx, tmuxName)
	return err == nil && museTUIAtPrompt(pane) && musePaneStable(ctx, tmuxName, pane)
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
	musePersistentPool.Unlock()
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
