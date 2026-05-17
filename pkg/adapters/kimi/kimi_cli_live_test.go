package kimi

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestKimiCLIRealStreamJSONContract(t *testing.T) {
	requireRealKimiCLIStreamJSONE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")
	t.Setenv("KIMI_CODE_CLI_MAX_STEPS_PER_TURN", "1")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	token := "KIMI_STREAM_JSON_OK"
	streamChan := make(chan llmtypes.StreamChunk, 128)
	captureDone := collectKimiStream(streamChan)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: strings.Repeat(
				"You are testing the Kimi CLI stream-json transport. Do not use tools. Keep exact-token replies concise.\n",
				60,
			)}},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: `This is a real Kimi CLI stream-json contract test.

Preserve input safely:

blank line above
JSON: {"token": "KIMI_STREAM_JSON_OK", "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Reply exactly:
saved KIMI_STREAM_JSON_OK`}},
		},
	}, llmtypes.WithStreamingChan(streamChan))
	if err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Content) == "" {
		t.Fatalf("GenerateContent returned empty response: %#v", resp)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want token %s", content, token)
	}
	streamed := <-captureDone
	if !strings.Contains(streamed, token) {
		t.Fatalf("streamed = %q, want token %s", streamed, token)
	}
	for _, noisy := range []string{"SHOULD_NOT_RUN", "stdout", "stderr", "exit_code", "execution_time_ms"} {
		if strings.Contains(streamed, noisy) {
			t.Fatalf("streamed content leaked %q in %q", noisy, streamed)
		}
	}
}

func requireRealKimiCLIStreamJSONE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_KIMI_CLI_REAL_E2E") == "" && os.Getenv("RUN_KIMI_CLI_STREAM_JSON_E2E") == "" && os.Getenv("KIMI_CLI_LIVE_TEST") == "" {
		t.Skip("set RUN_KIMI_CLI_STREAM_JSON_E2E=1 to run real Kimi CLI stream-json contract tests")
	}
	if _, err := exec.LookPath("kimi"); err != nil {
		t.Fatalf("real Kimi CLI stream-json tests require kimi in PATH: %v", err)
	}
}

func collectKimiStream(streamChan <-chan llmtypes.StreamChunk) <-chan string {
	done := make(chan string, 1)
	go func() {
		var content strings.Builder
		for chunk := range streamChan {
			if chunk.Type == llmtypes.StreamChunkTypeContent {
				content.WriteString(chunk.Content)
			}
		}
		done <- strings.TrimSpace(content.String())
	}()
	return done
}
