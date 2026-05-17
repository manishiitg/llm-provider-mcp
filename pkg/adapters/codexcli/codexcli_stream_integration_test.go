package codexcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCodexCLIRealExecJSONContract(t *testing.T) {
	requireRealCodexCLIStreamJSONE2E(t)

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, quietCodexStreamLogger{})
	token := "CODEX_EXEC_JSON_" + codexRandomHex(4)
	firstWant := "saved " + token

	largeSystemPrompt := strings.Repeat("You are testing the Codex CLI exec-json transport. Do not use tools. Keep exact-token replies concise.\n", 80)
	firstPrompt := fmt.Sprintf(`This is a real Codex CLI exec-json contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Reply exactly:
%s`, token, firstWant)

	firstStream := make(chan llmtypes.StreamChunk, 128)
	firstCapture := collectCodexStream(firstStream)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: largeSystemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: firstPrompt}}},
	},
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		llmtypes.WithStreamingChan(firstStream),
	)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	firstContent := strings.TrimSpace(first.Choices[0].Content)
	if !strings.Contains(firstContent, firstWant) {
		t.Fatalf("first content = %q, want %q", firstContent, firstWant)
	}
	firstStreamed := (<-firstCapture).content
	assertCodexStreamQuality(t, firstStreamed, firstWant)
	assertCodexDoesNotContainAny(t, "first stream", firstStreamed, "SHOULD_NOT_RUN", "developer_instructions", "stdout", "stderr", "exit_code")

	threadID, ok := first.Choices[0].GenerationInfo.Additional["codex_thread_id"].(string)
	if !ok || strings.TrimSpace(threadID) == "" {
		t.Fatalf("missing codex_thread_id in generation info: %#v", first.Choices[0].GenerationInfo.Additional)
	}

	secondWant := "CODEX_EXEC_JSON_RESUME_" + codexRandomHex(4)
	secondStream := make(chan llmtypes.StreamChunk, 128)
	secondCapture := collectCodexStream(secondStream)
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: largeSystemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + secondWant + ". Do not mention the previous token."}}},
	},
		WithResumeSessionID(threadID),
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		llmtypes.WithStreamingChan(secondStream),
	)
	if err != nil {
		t.Fatalf("resume GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, secondWant) {
		t.Fatalf("second content = %q, want %q", secondContent, secondWant)
	}
	if strings.Contains(secondContent, firstWant) {
		t.Fatalf("second content replayed first answer: %q", secondContent)
	}
	secondStreamed := (<-secondCapture).content
	assertCodexStreamQuality(t, secondStreamed, secondWant)
	assertCodexDoesNotContainAny(t, "second stream", secondStreamed, firstWant, "developer_instructions", "stdout", "stderr", "exit_code")
}

func TestCodexCLIRealExecJSONMCPBridgeContract(t *testing.T) {
	requireRealCodexCLIStreamJSONE2E(t)

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, quietCodexStreamLogger{})
	bridgeToken := "CODEX_EXEC_JSON_BRIDGE_" + codexRandomHex(4)
	mcpServerPath := writeCodexContractMCPServer(t)
	mcpCommandOverride, err := codexStringConfigOverride("mcp_servers.api-bridge.command", mcpServerPath)
	if err != nil {
		t.Fatalf("build MCP command override: %v", err)
	}

	streamChan := make(chan llmtypes.StreamChunk, 128)
	captureDone := collectCodexStream(streamChan)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge echo_contract MCP tool with token %s. Then reply exactly with the tool result text.", bridgeToken)}}},
	},
		WithDisableShellTool(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		WithConfigOverrides([]string{mcpCommandOverride}),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent with MCP bridge error = %v", err)
	}

	want := "BRIDGE_TOOL_OK_" + bridgeToken
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, want) {
		t.Fatalf("content = %q, want bridge tool result %q", content, want)
	}
	capture := <-captureDone
	if !strings.Contains(capture.content, want) {
		t.Fatalf("streamed = %q, want bridge tool result %q", capture.content, want)
	}
	if capture.toolStarts == 0 || capture.toolEnds == 0 {
		t.Fatalf("expected structured MCP tool start/end chunks, got starts=%d ends=%d content=%q", capture.toolStarts, capture.toolEnds, capture.content)
	}
	assertCodexDoesNotContainAny(t, "MCP bridge stream", capture.content,
		"stdout", "stderr", "exit_code", "execution_time_ms", "developer_instructions",
	)
}

func requireRealCodexCLIStreamJSONE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_CODEX_CLI_REAL_E2E") == "" && os.Getenv("RUN_CODEX_CLI_STREAM_JSON_E2E") == "" {
		t.Skip("set RUN_CODEX_CLI_STREAM_JSON_E2E=1 to run real Codex CLI exec-json contract tests")
	}
	for _, bin := range []string{"codex", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Codex CLI exec-json tests require %s in PATH: %v", bin, err)
		}
	}
}

type quietCodexStreamLogger struct{}

func (quietCodexStreamLogger) Debugf(string, ...interface{}) {}
func (quietCodexStreamLogger) Infof(string, ...any)          {}
func (quietCodexStreamLogger) Errorf(string, ...any)         {}

type codexStreamCapture struct {
	content    string
	toolStarts int
	toolEnds   int
}

func collectCodexStream(streamChan <-chan llmtypes.StreamChunk) <-chan codexStreamCapture {
	done := make(chan codexStreamCapture, 1)
	go func() {
		var capture codexStreamCapture
		var content strings.Builder
		for chunk := range streamChan {
			switch chunk.Type {
			case llmtypes.StreamChunkTypeContent:
				content.WriteString(chunk.Content)
			case llmtypes.StreamChunkTypeToolCallStart:
				capture.toolStarts++
			case llmtypes.StreamChunkTypeToolCallEnd:
				capture.toolEnds++
			}
		}
		capture.content = strings.TrimSpace(content.String())
		done <- capture
	}()
	return done
}
