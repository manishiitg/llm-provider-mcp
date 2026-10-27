package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-872: SDK input must remain open for questions/interrupts, and partial
// tokens plus completed assistant messages must not duplicate the visible reply.
func TestClaudeSDKStreamingQuestionsAndInterrupt(t *testing.T) {
	skipClaudeInteractivePersistentE2E(t)
	adapter := NewClaudeCodeInteractiveAdapter(claudeInteractiveIntegrationModel(), &MockLogger{})
	adapter.SetSDKTransport(true)
	t.Run("question_and_stream", func(t *testing.T) {
		owner := "sdk-question-" + randomHex(4)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		asked := 0
		ctx = llmtypes.WithCodingAgentQuestionHandler(ctx, "claude-code", owner, func(_ context.Context, args map[string]any) (string, error) {
			asked++
			raw, _ := json.Marshal(args)
			var request struct {
				Questions []struct {
					ID      string `json:"id"`
					Options []struct {
						Label string `json:"label"`
					} `json:"options"`
				} `json:"questions"`
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				return "", err
			}
			answers := []any{}
			for _, q := range request.Questions {
				if len(q.Options) < 2 {
					return "", fmt.Errorf("question lacks choices")
				}
				answers = append(answers, map[string]any{"id": q.ID, "selected_labels": []string{q.Options[1].Label}})
			}
			reply, _ := json.Marshal(map[string]any{"status": "answered", "answers": answers})
			return string(reply), nil
		})
		stream := make(chan llmtypes.StreamChunk, 128)
		var text strings.Builder
		deltas := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			for chunk := range stream {
				if chunk.Type == llmtypes.StreamChunkTypeContent {
					text.WriteString(chunk.Content)
					if chunk.Metadata[llmtypes.ContentDeltaMetadataKey] == true {
						deltas++
					}
				}
			}
		}()
		response, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Use AskUserQuestion now to ask me to pick Alpha or Beta. You must wait for my choice, then reply with only the chosen word. Do not call other tools.")}, WithWorkingDir(t.TempDir()), WithInteractiveSessionID(owner), WithClaudeCodeTools("AskUserQuestion"), WithPermissionMode("default"), llmtypes.WithStreamingChan(stream))
		<-done
		if err != nil {
			t.Fatal(err)
		}
		if asked != 1 {
			t.Fatalf("native question callbacks=%d", asked)
		}
		content := strings.TrimSpace(response.Choices[0].Content)
		if content != "Beta" {
			t.Fatalf("answer=%q", content)
		}
		if deltas == 0 || strings.TrimSpace(text.String()) != content {
			t.Fatalf("stream duplicated or missing: %d deltas, stream=%q final=%q", deltas, text.String(), content)
		}
		if response.Usage == nil || response.Usage.TotalTokens == 0 {
			t.Fatal("native usage absent")
		}
	})
	t.Run("interrupt_slow_tool", func(t *testing.T) {
		owner := "sdk-interrupt-" + randomHex(4)
		dir := t.TempDir()
		marker := filepath.Join(dir, "started")
		server := writeClaudeInteractiveSlowMCPServer(t, marker)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Call api-bridge slow_contract with token TEST and delay_ms 30000. Wait for its result before replying.")}, WithWorkingDir(dir), WithInteractiveSessionID(owner), WithMCPConfig(fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":%q}}}`, server)), WithClaudeCodeTools(""), WithAllowedTools("mcp__api-bridge__slow_contract"))
			done <- err
		}()
		deadline := time.NewTimer(40 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
	waiting:
		for {
			select {
			case err := <-done:
				t.Fatalf("completed before slow tool: %v", err)
			case <-deadline.C:
				t.Fatal("slow tool never started")
			case <-ticker.C:
				if _, err := os.Stat(marker); err == nil {
					break waiting
				}
			}
		}
		if err := SendClaudeCodeControlKey(ctx, owner, "Escape"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Fatal("SDK interruption did not finish the active turn")
		}
	})
}
