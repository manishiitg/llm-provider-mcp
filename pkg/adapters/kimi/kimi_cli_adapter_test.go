package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestBuildKimiCLIPromptIncludesRoles(t *testing.T) {
	prompt := buildKimiCLIPrompt([]llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use concise answers."}},
		},
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Fix the bug."}},
		},
		{
			Role:  llmtypes.ChatMessageTypeAI,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "I will inspect it."}},
		},
		{
			Role:  llmtypes.ChatMessageTypeTool,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "tool failed"}},
		},
	})

	wantParts := []string{
		"System instructions:\nUse concise answers.",
		"User: Fix the bug.",
		"Assistant: I will inspect it.",
		"Tool result: tool failed",
	}
	for _, want := range wantParts {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt = %q, missing %q", prompt, want)
		}
	}
}

func TestKimiWorkingDirOption(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(" /tmp/kimi-work ")(opts)
	if got := kimiWorkingDirFromOptions(opts); got != "/tmp/kimi-work" {
		t.Fatalf("working dir = %q, want /tmp/kimi-work", got)
	}
}

func TestKimiCLIRejectsImageContent(t *testing.T) {
	adapter := NewKimiCLIAdapter(ModelKimiCode, &testLogger{})

	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Describe this image."},
				llmtypes.ImageContent{SourceType: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="},
			},
		},
	})
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want unsupported image content error")
	}
	if !strings.Contains(err.Error(), "does not support llmtypes.ImageContent") {
		t.Fatalf("GenerateContent() error = %v, want image content unsupported error", err)
	}
}

func TestParseKimiCLIUsage(t *testing.T) {
	raw := map[string]interface{}{
		"stats": map[string]interface{}{
			"input_tokens":  float64(12),
			"output_tokens": float64(5),
			"total_tokens":  float64(17),
		},
	}

	usage, genInfo := parseKimiCLIUsage(raw, "session-1", "kimi-for-coding")
	if usage == nil {
		t.Fatal("usage is nil")
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 5 || usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v, want 12/5/17", usage)
	}
	if got := genInfo.Additional["session_id"]; got != "session-1" {
		t.Fatalf("session_id = %v, want session-1", got)
	}
}

func TestParseKimiCLIUsageFallsBackToUsageFieldAndComputesTotal(t *testing.T) {
	raw := map[string]interface{}{
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(7),
			"completion_tokens": float64(8),
		},
	}

	usage, genInfo := parseKimiCLIUsage(raw, "", "")
	if usage == nil {
		t.Fatal("usage is nil")
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 8 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v, want 7/8/15", usage)
	}
	if genInfo.InputTokens == nil || *genInfo.InputTokens != 7 {
		t.Fatalf("generation input tokens = %#v, want 7", genInfo.InputTokens)
	}
}

func TestHandleKimiCLIEventParsesRoleAssistantContentArray(t *testing.T) {
	raw := map[string]interface{}{
		"role": "assistant",
		"content": []interface{}{
			map[string]interface{}{"type": "think", "think": "internal reasoning"},
			map[string]interface{}{"type": "text", "text": "Hello from real Kimi shape."},
		},
	}

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	var text strings.Builder
	var sessionID string
	var resolvedModel string
	var usage *llmtypes.Usage
	var genInfo *llmtypes.GenerationInfo
	adapter.handleKimiCLIEvent(raw, &llmtypes.CallOptions{}, &text, &sessionID, &resolvedModel, &usage, &genInfo)

	if got := text.String(); got != "Hello from real Kimi shape." {
		t.Fatalf("parsed text = %q, want real Kimi text content only", got)
	}
}

func TestHandleKimiCLIEventIgnoresNonAssistantRole(t *testing.T) {
	raw := map[string]interface{}{
		"role":    "user",
		"content": "do not echo",
	}

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	var text strings.Builder
	var sessionID string
	var resolvedModel string
	var usage *llmtypes.Usage
	var genInfo *llmtypes.GenerationInfo
	adapter.handleKimiCLIEvent(raw, &llmtypes.CallOptions{}, &text, &sessionID, &resolvedModel, &usage, &genInfo)

	if got := text.String(); got != "" {
		t.Fatalf("parsed text = %q, want empty for non-assistant role", got)
	}
}

func TestHandleKimiCLIEventStreamsToolUseAndResult(t *testing.T) {
	stream := make(chan llmtypes.StreamChunk, 2)
	opts := &llmtypes.CallOptions{StreamChan: stream}
	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	var text strings.Builder
	var sessionID string
	var resolvedModel string
	var usage *llmtypes.Usage
	var genInfo *llmtypes.GenerationInfo

	adapter.handleKimiCLIEvent(map[string]interface{}{
		"type":      "tool_use",
		"tool_name": "ReadFile",
		"tool_id":   "tool-1",
		"parameters": map[string]interface{}{
			"path": "README.md",
		},
	}, opts, &text, &sessionID, &resolvedModel, &usage, &genInfo)
	adapter.handleKimiCLIEvent(map[string]interface{}{
		"type":    "tool_result",
		"name":    "ReadFile",
		"id":      "tool-1",
		"content": "file contents",
	}, opts, &text, &sessionID, &resolvedModel, &usage, &genInfo)

	start := <-stream
	if start.Type != llmtypes.StreamChunkTypeToolCallStart || start.ToolName != "ReadFile" || start.ToolCallID != "tool-1" {
		t.Fatalf("tool start chunk = %+v", start)
	}
	if !strings.Contains(start.ToolArgs, `"path":"README.md"`) {
		t.Fatalf("tool args = %q, want path argument", start.ToolArgs)
	}

	end := <-stream
	if end.Type != llmtypes.StreamChunkTypeToolCallEnd || end.ToolName != "ReadFile" || end.ToolCallID != "tool-1" || end.ToolResult != "file contents" {
		t.Fatalf("tool end chunk = %+v", end)
	}
}

func TestHandleKimiCLIEventParsesResultMetadata(t *testing.T) {
	raw := map[string]interface{}{
		"type":       "result",
		"session_id": "session-456",
		"model":      "kimi-for-coding",
		"stats": map[string]interface{}{
			"input_tokens":  float64(1),
			"output_tokens": float64(2),
			"total_tokens":  float64(3),
		},
	}

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	var text strings.Builder
	var sessionID string
	var resolvedModel string
	var usage *llmtypes.Usage
	var genInfo *llmtypes.GenerationInfo
	adapter.handleKimiCLIEvent(raw, &llmtypes.CallOptions{}, &text, &sessionID, &resolvedModel, &usage, &genInfo)

	if sessionID != "session-456" || resolvedModel != "kimi-for-coding" {
		t.Fatalf("session/model = %q/%q, want session-456/kimi-for-coding", sessionID, resolvedModel)
	}
	if usage == nil || usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v, want total tokens 3", usage)
	}
}

func TestResolveAgentFileDefaultsToNoTools(t *testing.T) {
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	agentFile, cleanup, err := adapter.resolveAgentFile()
	if err != nil {
		t.Fatalf("resolveAgentFile returned error: %v", err)
	}
	defer cleanup()

	contentBytes, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("failed to read generated agent file: %v", err)
	}
	if !strings.Contains(string(contentBytes), "tools: []") {
		t.Fatalf("generated default agent file = %s, want tools: []", string(contentBytes))
	}
}

func TestKimiCLIAdapterDoesNotImplementWebSearchModel(t *testing.T) {
	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); ok {
		t.Fatal("KimiCLIAdapter unexpectedly implements WebSearchModel; add a real web-search contract test before enabling it")
	}
}

func TestResolveAgentFileCanDisableAllTools(t *testing.T) {
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	agentFile, cleanup, err := adapter.resolveAgentFile()
	if err != nil {
		t.Fatalf("resolveAgentFile returned error: %v", err)
	}
	defer cleanup()

	contentBytes, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("failed to read generated agent file: %v", err)
	}
	if !strings.Contains(string(contentBytes), "tools: []") {
		t.Fatalf("generated no-tools agent file = %s, want tools: []", string(contentBytes))
	}
}

func TestResolveAgentFileCanEnableReadOnlyTools(t *testing.T) {
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "readonly")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	agentFile, cleanup, err := adapter.resolveAgentFile()
	if err != nil {
		t.Fatalf("resolveAgentFile returned error: %v", err)
	}
	defer cleanup()

	contentBytes, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("failed to read generated agent file: %v", err)
	}
	content := string(contentBytes)
	for _, disallowed := range []string{"Shell", "WriteFile", "StrReplaceFile", "Agent", "SearchWeb", "FetchURL"} {
		if strings.Contains(content, disallowed) {
			t.Fatalf("generated readonly agent file contains disallowed tool %q: %s", disallowed, content)
		}
	}
	for _, allowed := range []string{"ReadFile", "ReadMediaFile", "Glob", "Grep"} {
		if !strings.Contains(content, allowed) {
			t.Fatalf("generated readonly agent file missing allowed tool %q: %s", allowed, content)
		}
	}
}

func TestResolveAgentFileUsesExplicitAgentFile(t *testing.T) {
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "/tmp/custom-kimi-agent.yaml")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	agentFile, cleanup, err := adapter.resolveAgentFile()
	if err != nil {
		t.Fatalf("resolveAgentFile returned error: %v", err)
	}
	defer cleanup()
	if agentFile != "/tmp/custom-kimi-agent.yaml" {
		t.Fatalf("agentFile = %q, want explicit path", agentFile)
	}
}

func TestResolveAgentFileDefaultModeUsesBuiltinAgent(t *testing.T) {
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "default")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	agentFile, cleanup, err := adapter.resolveAgentFile()
	if err != nil {
		t.Fatalf("resolveAgentFile returned error: %v", err)
	}
	defer cleanup()
	if agentFile != "" {
		t.Fatalf("agentFile = %q, want empty for builtin default", agentFile)
	}
}

func TestKimiCLIParserFixtures(t *testing.T) {
	fixtures := map[string]string{
		"real-thinking-text": `{"role":"assistant","content":[{"type":"think","think":"hidden"},{"type":"text","text":"visible"}]}`,
		"message-string":     `{"type":"message","role":"assistant","content":"visible"}`,
		"delta":              `{"type":"delta","delta":{"text":"visible"}}`,
		"final":              `{"type":"final","text":"visible"}`,
	}

	for name, line := range fixtures {
		t.Run(name, func(t *testing.T) {
			var raw map[string]interface{}
			decoder := json.NewDecoder(bytes.NewBufferString(line))
			decoder.UseNumber()
			if err := decoder.Decode(&raw); err != nil {
				t.Fatalf("failed to decode fixture: %v", err)
			}

			adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
			var text strings.Builder
			var sessionID string
			var resolvedModel string
			var usage *llmtypes.Usage
			var genInfo *llmtypes.GenerationInfo
			adapter.handleKimiCLIEvent(raw, &llmtypes.CallOptions{}, &text, &sessionID, &resolvedModel, &usage, &genInfo)

			if got := text.String(); got != "visible" {
				t.Fatalf("parsed text = %q, want visible", got)
			}
		})
	}
}

type testLogger struct{}

func (testLogger) Debugf(string, ...interface{}) {}
func (testLogger) Infof(string, ...interface{})  {}
func (testLogger) Warnf(string, ...interface{})  {}
func (testLogger) Errorf(string, ...interface{}) {}
