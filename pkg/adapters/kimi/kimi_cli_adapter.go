package kimi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	defaultKimiCLIBinary = "kimi"
)

// KimiCLIAdapter runs the native Kimi Code CLI instead of Kimi's
// Anthropic-compatible HTTP endpoint. This is intentionally hidden behind an
// internal feature flag while we validate the CLI's stream-json contract.
type KimiCLIAdapter struct {
	modelID string
	logger  interfaces.Logger
}

func NewKimiCLIAdapter(modelID string, logger interfaces.Logger) *KimiCLIAdapter {
	if strings.TrimSpace(modelID) == "" {
		modelID = ModelKimiCode
	}
	return &KimiCLIAdapter{
		modelID: modelID,
		logger:  logger,
	}
}

func (k *KimiCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	binary := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_BINARY"))
	if binary == "" {
		binary = defaultKimiCLIBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("kimi cli not found in PATH. Install Kimi Code CLI and run `kimi login` before enabling KIMI_CODE_TRANSPORT=cli")
	}

	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	promptText := buildKimiCLIPrompt(messages)
	if strings.TrimSpace(promptText) == "" {
		return nil, fmt.Errorf("kimi cli prompt is empty")
	}

	args := []string{"--print", "--output-format", "stream-json", "--prompt", promptText}
	if model := k.resolveCLIModel(); model != "" {
		args = append(args, "--model", model)
	}
	if maxSteps := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_MAX_STEPS_PER_TURN")); maxSteps != "" {
		args = append(args, "--max-steps-per-turn", maxSteps)
	}
	args = append(args, k.resolveMCPArgs(opts)...)

	agentFile, cleanupAgentFile, err := k.resolveAgentFile()
	if err != nil {
		return nil, err
	}
	defer cleanupAgentFile()
	if agentFile != "" {
		args = append(args, "--agent-file", agentFile)
	} else if agent := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_AGENT")); agent != "" {
		args = append(args, "--agent", agent)
	}
	if workDir := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_WORK_DIR")); workDir != "" {
		args = append(args, "--work-dir", workDir)
	}

	k.logger.Infof("Executing Kimi Code CLI: %s %v", binary, args)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	defer func() {
		if ctx.Err() != nil && cmd.Process != nil {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && k.logger != nil {
				k.logger.Errorf("Failed to kill Kimi CLI process group: %v", err)
			}
		}
	}()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create kimi cli stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create kimi cli stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start kimi cli: %w", err)
	}

	var stderrBuilder strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			stderrBuilder.WriteString(scanner.Text() + "\n")
		}
	}()

	var accumulatedText strings.Builder
	var stdoutDiagnostics strings.Builder
	var sessionID string
	var resolvedModel string
	var usage *llmtypes.Usage
	var genInfo *llmtypes.GenerationInfo

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			k.logger.Errorf("Failed to decode Kimi CLI stream-json line: %v (line: %s)", err, truncateKimiCLILog(line, 300))
			stdoutDiagnostics.WriteString(line + "\n")
			continue
		}

		k.handleKimiCLIEvent(raw, opts, &accumulatedText, &sessionID, &resolvedModel, &usage, &genInfo)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading kimi cli stdout: %w", err)
	}

	waitErr := cmd.Wait()
	<-stderrDone
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("kimi cli cancelled: %w", ctx.Err())
		}
		diagnosticText := formatKimiCLIDiagnostics(stdoutDiagnostics.String(), stderrBuilder.String())
		if diagnosticText != "" {
			return nil, fmt.Errorf("kimi cli failed: %w: %s", waitErr, truncateKimiCLILog(diagnosticText, 2000))
		}
		return nil, fmt.Errorf("kimi cli failed: %w", waitErr)
	}

	content := accumulatedText.String()
	if strings.TrimSpace(content) == "" {
		diagnosticText := formatKimiCLIDiagnostics(stdoutDiagnostics.String(), stderrBuilder.String())
		if diagnosticText != "" {
			return nil, fmt.Errorf("kimi cli completed without response content: %s", truncateKimiCLILog(diagnosticText, 2000))
		}
	}

	if genInfo == nil {
		genInfo = &llmtypes.GenerationInfo{Additional: map[string]interface{}{}}
	}
	if genInfo.Additional == nil {
		genInfo.Additional = map[string]interface{}{}
	}
	genInfo.Additional["provider"] = "kimi-cli"
	if sessionID != "" {
		genInfo.Additional["session_id"] = sessionID
	}
	if resolvedModel != "" {
		genInfo.Additional["model"] = resolvedModel
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        content,
				GenerationInfo: genInfo,
			},
		},
		Usage: usage,
	}, nil
}

func formatKimiCLIDiagnostics(stdoutText, stderrText string) string {
	var parts []string
	if text := strings.TrimSpace(stdoutText); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(stderrText); text != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}

func (k *KimiCLIAdapter) GetModelID() string {
	return k.modelID
}

func (k *KimiCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	return GetKimiModelMetadata(modelID)
}

func (k *KimiCLIAdapter) resolveCLIModel() string {
	if model := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_MODEL")); model != "" {
		return model
	}
	if k.modelID == "" || k.modelID == ModelKimiCode {
		return ""
	}
	return k.modelID
}

func (k *KimiCLIAdapter) resolveMCPArgs(opts *llmtypes.CallOptions) []string {
	var args []string
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mcpConfig, ok := opts.Metadata.Custom["mcp_config"].(string); ok && strings.TrimSpace(mcpConfig) != "" {
			args = append(args, "--mcp-config", mcpConfig)
		}
		if mcpConfigFile, ok := opts.Metadata.Custom["kimi_mcp_config_file"].(string); ok && strings.TrimSpace(mcpConfigFile) != "" {
			args = append(args, "--mcp-config-file", mcpConfigFile)
		}
	}
	if mcpConfig := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_MCP_CONFIG")); mcpConfig != "" {
		args = append(args, "--mcp-config", mcpConfig)
	}
	if mcpConfigFile := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_MCP_CONFIG_FILE")); mcpConfigFile != "" {
		args = append(args, "--mcp-config-file", mcpConfigFile)
	}
	return args
}

func (k *KimiCLIAdapter) resolveAgentFile() (string, func(), error) {
	if agentFile := strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_AGENT_FILE")); agentFile != "" {
		return agentFile, func() {}, nil
	}

	toolMode := strings.ToLower(strings.TrimSpace(os.Getenv("KIMI_CODE_CLI_TOOL_MODE")))
	if toolMode == "" {
		toolMode = "none"
	}
	if toolMode == "default" || toolMode == "builtin" {
		return "", func() {}, nil
	}

	dir, err := os.MkdirTemp("", "kimi-code-agent-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create kimi cli agent temp dir: %w", err)
	}
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			k.logger.Errorf("Failed to remove Kimi CLI agent temp dir %s: %v", dir, err)
		}
	}

	systemPath := dir + "/system.md"
	agentPath := dir + "/agent.yaml"
	if err := os.WriteFile(systemPath, []byte(defaultKimiCLISystemPrompt(toolMode)), 0600); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to write kimi cli system prompt: %w", err)
	}
	if err := os.WriteFile(agentPath, []byte(defaultKimiCLIAgentYAML(toolMode)), 0600); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to write kimi cli agent file: %w", err)
	}
	return agentPath, cleanup, nil
}

func defaultKimiCLISystemPrompt(toolMode string) string {
	switch toolMode {
	case "none", "no-tools", "disabled":
		return "# Kimi Code Restricted Agent\n\nAnswer from the supplied conversation only. Do not use tools.\n"
	default:
		return "# Kimi Code Read-Only Agent\n\nYou may inspect files with read-only file tools. Do not run shell commands, modify files, launch subagents, or use web tools.\n"
	}
}

func defaultKimiCLIAgentYAML(toolMode string) string {
	if toolMode == "none" || toolMode == "no-tools" || toolMode == "disabled" {
		return `version: 1
agent:
  name: mcp-agent-kimi-no-tools
  system_prompt_path: ./system.md
  tools: []
`
	}

	return `version: 1
agent:
  name: mcp-agent-kimi-readonly
  system_prompt_path: ./system.md
  tools:
    - "kimi_cli.tools.todo:SetTodoList"
    - "kimi_cli.tools.file:ReadFile"
    - "kimi_cli.tools.file:ReadMediaFile"
    - "kimi_cli.tools.file:Glob"
    - "kimi_cli.tools.file:Grep"
`
}

func buildKimiCLIPrompt(messages []llmtypes.MessageContent) string {
	var systemParts []string
	var conversation []string
	for _, msg := range messages {
		text := extractKimiCLIText(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch msg.Role {
		case llmtypes.ChatMessageTypeSystem:
			systemParts = append(systemParts, text)
		case llmtypes.ChatMessageTypeHuman:
			conversation = append(conversation, "User: "+text)
		case llmtypes.ChatMessageTypeAI:
			conversation = append(conversation, "Assistant: "+text)
		case llmtypes.ChatMessageTypeTool:
			conversation = append(conversation, "Tool result: "+text)
		default:
			conversation = append(conversation, text)
		}
	}

	if len(systemParts) == 0 {
		return strings.Join(conversation, "\n\n")
	}
	parts := []string{"System instructions:\n" + strings.Join(systemParts, "\n\n")}
	if len(conversation) > 0 {
		parts = append(parts, strings.Join(conversation, "\n\n"))
	}
	return strings.Join(parts, "\n\n")
}

func extractKimiCLIText(msg llmtypes.MessageContent) string {
	var parts []string
	for _, part := range msg.Parts {
		switch p := part.(type) {
		case llmtypes.TextContent:
			parts = append(parts, p.Text)
		case llmtypes.ToolCallResponse:
			parts = append(parts, p.Content)
		}
	}
	return strings.Join(parts, "\n")
}

func (k *KimiCLIAdapter) handleKimiCLIEvent(
	raw map[string]interface{},
	opts *llmtypes.CallOptions,
	accumulatedText *strings.Builder,
	sessionID *string,
	resolvedModel *string,
	usage **llmtypes.Usage,
	genInfo **llmtypes.GenerationInfo,
) {
	if sid, ok := stringField(raw, "session_id", "sessionId", "session"); ok && sid != "" {
		*sessionID = sid
	}
	if model, ok := stringField(raw, "model", "model_id", "modelId"); ok && model != "" {
		*resolvedModel = model
	}

	msgType, _ := raw["type"].(string)
	if msgType == "" {
		if text := extractKimiCLIContent(raw); text != "" {
			accumulatedText.WriteString(text)
			if opts.StreamChan != nil {
				opts.StreamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: text}
			}
		}
		return
	}
	switch msgType {
	case "message", "assistant", "content", "text", "delta":
		if text := extractKimiCLIContent(raw); text != "" {
			accumulatedText.WriteString(text)
			if opts.StreamChan != nil {
				opts.StreamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: text}
			}
		}
	case "tool_use":
		toolName, _ := stringField(raw, "tool_name", "name")
		toolID, _ := stringField(raw, "tool_id", "tool_use_id", "id")
		toolArgs := marshalKimiCLIArgs(raw["parameters"])
		if toolArgs == "" {
			toolArgs = marshalKimiCLIArgs(raw["args"])
		}
		if toolArgs == "" {
			toolArgs = marshalKimiCLIArgs(raw["input"])
		}
		if opts.StreamChan != nil {
			opts.StreamChan <- llmtypes.StreamChunk{
				Type:       llmtypes.StreamChunkTypeToolCallStart,
				ToolName:   toolName,
				ToolCallID: toolID,
				ToolArgs:   toolArgs,
			}
		}
	case "tool_result":
		toolName, _ := stringField(raw, "tool_name", "name")
		toolID, _ := stringField(raw, "tool_id", "tool_use_id", "id")
		result, _ := stringField(raw, "output", "content", "result")
		if opts.StreamChan != nil {
			opts.StreamChan <- llmtypes.StreamChunk{
				Type:         llmtypes.StreamChunkTypeToolCallEnd,
				ToolName:     toolName,
				ToolCallID:   toolID,
				ToolResult:   result,
				ToolDuration: 0 * time.Second,
			}
		}
	case "result", "summary", "final":
		if text := extractKimiCLIContent(raw); text != "" {
			accumulatedText.WriteString(text)
			if opts.StreamChan != nil {
				opts.StreamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: text}
			}
		}
		*usage, *genInfo = parseKimiCLIUsage(raw, *sessionID, *resolvedModel)
	}
}

func extractKimiCLIContent(raw map[string]interface{}) string {
	if role, _ := raw["role"].(string); role != "" && role != "assistant" {
		return ""
	}
	if text, ok := stringField(raw, "content", "text", "message", "output", "result"); ok {
		return text
	}
	if delta, ok := raw["delta"].(map[string]interface{}); ok {
		if text, ok := stringField(delta, "text", "content"); ok {
			return text
		}
	}
	if message, ok := raw["message"].(map[string]interface{}); ok {
		if role, _ := message["role"].(string); role != "" && role != "assistant" {
			return ""
		}
		if text, ok := stringField(message, "content", "text"); ok {
			return text
		}
	}
	return ""
}

func parseKimiCLIUsage(raw map[string]interface{}, sessionID string, resolvedModel string) (*llmtypes.Usage, *llmtypes.GenerationInfo) {
	stats := mapFromAny(raw["stats"])
	if len(stats) == 0 {
		stats = mapFromAny(raw["usage"])
	}

	inputTokens := intField(stats, "input_tokens", "prompt_tokens")
	outputTokens := intField(stats, "output_tokens", "completion_tokens")
	totalTokens := intField(stats, "total_tokens")
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}

	additional := map[string]interface{}{
		"provider": "kimi-cli",
	}
	if sessionID != "" {
		additional["session_id"] = sessionID
	}
	if resolvedModel != "" {
		additional["model"] = resolvedModel
	}

	genInfo := &llmtypes.GenerationInfo{Additional: additional}
	if inputTokens > 0 {
		genInfo.InputTokens = &inputTokens
	}
	if outputTokens > 0 {
		genInfo.OutputTokens = &outputTokens
	}
	if totalTokens > 0 {
		genInfo.TotalTokens = &totalTokens
	}
	if inputTokens == 0 && outputTokens == 0 && totalTokens == 0 {
		return nil, genInfo
	}

	return &llmtypes.Usage{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
	}, genInfo
}

func stringField(raw map[string]interface{}, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch v := value.(type) {
			case string:
				return v, true
			case []interface{}:
				var parts []string
				for _, item := range v {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if text, ok := stringField(itemMap, "text", "content"); ok {
							parts = append(parts, text)
						}
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, ""), true
				}
			}
		}
	}
	return "", false
}

func mapFromAny(value interface{}) map[string]interface{} {
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func intField(raw map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		switch v := raw[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return 0
}

func marshalKimiCLIArgs(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(bytes)
}

func truncateKimiCLILog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
