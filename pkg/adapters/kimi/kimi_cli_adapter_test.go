package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestGenerateContentInvokesKimiCLIWithReadOnlyAgentFile(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "args.txt")
	agentCopyPath := filepath.Join(tmpDir, "agent.yaml")
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
prev=""
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$KIMI_FAKE_ARGS_PATH"
  if [ "$prev" = "--agent-file" ]; then
    cp "$arg" "$KIMI_FAKE_AGENT_COPY_PATH"
  fi
  prev="$arg"
done
printf '%s\n' '{"type":"message","role":"assistant","content":"hello from fake kimi"}'
printf '%s\n' '{"type":"result","session_id":"session-123","model":"fake-kimi","stats":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}'
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_FAKE_ARGS_PATH", argsPath)
	t.Setenv("KIMI_FAKE_AGENT_COPY_PATH", agentCopyPath)
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "")
	t.Setenv("KIMI_CODE_CLI_AGENT", "default")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	resp, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say hello"}},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}
	if got := resp.Choices[0].Content; got != "hello from fake kimi" {
		t.Fatalf("content = %q, want fake kimi response", got)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v, want total tokens 5", resp.Usage)
	}

	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("failed to read fake kimi args: %v", err)
	}
	args := string(argsBytes)
	for _, want := range []string{"--print", "--output-format", "stream-json", "--prompt", "--agent-file"} {
		if !strings.Contains(args, want) {
			t.Fatalf("kimi args = %q, missing %q", args, want)
		}
	}
	if strings.Contains(args, "--agent\ndefault") {
		t.Fatalf("kimi args = %q, should prefer generated --agent-file over KIMI_CODE_CLI_AGENT", args)
	}

	agentBytes, err := os.ReadFile(agentCopyPath)
	if err != nil {
		t.Fatalf("failed to read copied generated agent file: %v", err)
	}
	agent := string(agentBytes)
	for _, disallowed := range []string{"Shell", "WriteFile", "StrReplaceFile", "Agent", "SearchWeb", "FetchURL"} {
		if strings.Contains(agent, disallowed) {
			t.Fatalf("generated agent contains disallowed tool %q: %s", disallowed, agent)
		}
	}
}

func TestGenerateContentPassesOptionalFlags(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "args.txt")
	workDir := filepath.Join(tmpDir, "work")
	if err := os.Mkdir(workDir, 0755); err != nil {
		t.Fatalf("failed to create work dir: %v", err)
	}
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$KIMI_FAKE_ARGS_PATH"
done
printf '%s\n' '{"role":"assistant","content":[{"type":"text","text":"ok"}]}'
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_FAKE_ARGS_PATH", argsPath)
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "default")
	t.Setenv("KIMI_CODE_CLI_AGENT", "okabe")
	t.Setenv("KIMI_CODE_CLI_MODEL", "custom-kimi")
	t.Setenv("KIMI_CODE_CLI_WORK_DIR", workDir)
	t.Setenv("KIMI_CODE_CLI_MAX_STEPS_PER_TURN", "4")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	resp, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	})
	if err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}
	if got := resp.Choices[0].Content; got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}

	args := readArgLines(t, argsPath)
	assertArgPair(t, args, "--model", "custom-kimi")
	assertArgPair(t, args, "--work-dir", workDir)
	assertArgPair(t, args, "--max-steps-per-turn", "4")
	assertArgPair(t, args, "--agent", "okabe")
	assertArgAbsent(t, args, "--agent-file")
}

func TestGenerateContentPassesMCPConfigFromMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "args.txt")
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$KIMI_FAKE_ARGS_PATH"
done
printf '%s\n' '{"type":"message","role":"assistant","content":"ok"}'
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	mcpConfig := `{"mcpServers":{"api-bridge":{"command":"/tmp/mcpbridge","env":{"MCP_API_URL":"http://127.0.0.1:1"}}}}`
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_FAKE_ARGS_PATH", argsPath)
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	if _, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	}, func(opts *llmtypes.CallOptions) {
		if opts.Metadata == nil {
			opts.Metadata = &llmtypes.Metadata{Custom: map[string]interface{}{}}
		}
		if opts.Metadata.Custom == nil {
			opts.Metadata.Custom = map[string]interface{}{}
		}
		opts.Metadata.Custom["mcp_config"] = mcpConfig
	}); err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}

	args := readArgLines(t, argsPath)
	assertArgPair(t, args, "--mcp-config", mcpConfig)
	assertArgPair(t, args, "--agent-file", args[argIndex(t, args, "--agent-file")+1])
}

func TestGenerateContentPassesMCPConfigFileFromEnv(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "args.txt")
	mcpConfigFile := filepath.Join(tmpDir, "mcp.json")
	if err := os.WriteFile(mcpConfigFile, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("failed to write mcp config file: %v", err)
	}
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$KIMI_FAKE_ARGS_PATH"
done
printf '%s\n' '{"type":"message","role":"assistant","content":"ok"}'
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_FAKE_ARGS_PATH", argsPath)
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")
	t.Setenv("KIMI_CODE_CLI_MCP_CONFIG_FILE", mcpConfigFile)

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	if _, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	}); err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}

	args := readArgLines(t, argsPath)
	assertArgPair(t, args, "--mcp-config-file", mcpConfigFile)
}

func TestGenerateContentUsesExplicitAgentFile(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "args.txt")
	agentFile := filepath.Join(tmpDir, "agent.yaml")
	if err := os.WriteFile(agentFile, []byte("version: 1\nagent:\n  name: custom\n  tools: []\n"), 0644); err != nil {
		t.Fatalf("failed to write agent file: %v", err)
	}
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$KIMI_FAKE_ARGS_PATH"
done
printf '%s\n' '{"type":"message","role":"assistant","content":"ok"}'
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_FAKE_ARGS_PATH", argsPath)
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", agentFile)
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")
	t.Setenv("KIMI_CODE_CLI_AGENT", "okabe")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	if _, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	}); err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}

	args := readArgLines(t, argsPath)
	assertArgPair(t, args, "--agent-file", agentFile)
	assertArgAbsent(t, args, "--agent")
}

func TestGenerateContentSkipsMalformedJSONLines(t *testing.T) {
	tmpDir := t.TempDir()
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
printf '%s\n' 'this is not json'
printf '%s\n' '{"role":"assistant","content":[{"type":"text","text":"valid after malformed"}]}'
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	resp, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	})
	if err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}
	if got := resp.Choices[0].Content; got != "valid after malformed" {
		t.Fatalf("content = %q, want valid line after malformed JSON", got)
	}
}

func TestGenerateContentReturnsStderrOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
printf '%s\n' 'auth failed' >&2
exit 7
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	})
	if err == nil {
		t.Fatal("GenerateContent returned nil error for failing CLI")
	}
	if !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("error = %q, want stderr content", err.Error())
	}
}

func TestGenerateContentReportsCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	fakeKimiPath := filepath.Join(tmpDir, "kimi")
	fakeKimi := `#!/bin/sh
sleep 5
`
	if err := os.WriteFile(fakeKimiPath, []byte(fakeKimi), 0755); err != nil {
		t.Fatalf("failed to write fake kimi binary: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say ok"}}},
	})
	if err == nil {
		t.Fatal("GenerateContent returned nil error for cancelled context")
	}
	if !strings.Contains(err.Error(), "kimi cli cancelled") {
		t.Fatalf("error = %q, want cancellation error", err.Error())
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

func readArgLines(t *testing.T, path string) []string {
	t.Helper()
	argsBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read args file: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
}

func assertArgPair(t *testing.T, args []string, key string, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return
		}
	}
	t.Fatalf("args = %#v, missing pair %s %s", args, key, value)
}

func argIndex(t *testing.T, args []string, key string) int {
	t.Helper()
	for i, arg := range args {
		if arg == key {
			return i
		}
	}
	t.Fatalf("args = %#v, missing %s", args, key)
	return -1
}

func assertArgAbsent(t *testing.T, args []string, key string) {
	t.Helper()
	for _, arg := range args {
		if arg == key {
			t.Fatalf("args = %#v, expected %s to be absent", args, key)
		}
	}
}

type testLogger struct{}

func (testLogger) Debugf(string, ...interface{}) {}
func (testLogger) Infof(string, ...interface{})  {}
func (testLogger) Warnf(string, ...interface{})  {}
func (testLogger) Errorf(string, ...interface{}) {}
