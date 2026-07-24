package codexcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
)

// codexExecEvent is one JSONL line from `codex exec --json`. Verified live
// against the actually-installed codex-cli 0.145.0 (the doc's example format
// in docs/coding_sdk_structured_contract.md is stale — the real shape is
// simpler: thread.started -> turn.started -> item.started/item.completed
// (agent_message | command_execution) -> turn.completed).
type codexExecEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     *codexExecItem  `json:"item,omitempty"`
	Usage    *codexExecUsage `json:"usage,omitempty"`
}

type codexExecItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"` // "agent_message" | "command_execution" | "mcp_tool_call"
	Text             string `json:"text,omitempty"`
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Status           string `json:"status,omitempty"`
	// mcp_tool_call fields — verified live against codex-cli 0.145.0.
	Server string `json:"server,omitempty"`
	Tool   string `json:"tool,omitempty"`
}

// isCodexToolItem reports whether an item type represents a real tool
// invocation worth streaming as ToolCallStart/ToolCallEnd — native shell
// (command_execution) and MCP bridge calls (mcp_tool_call) both count.
func isCodexToolItem(itemType string) bool {
	return itemType == "command_execution" || itemType == "mcp_tool_call"
}

// codexToolItemLabel renders a human-readable label for a tool-call chunk.
func codexToolItemLabel(item *codexExecItem) string {
	if item.Type == "mcp_tool_call" {
		return item.Server + "." + item.Tool
	}
	return item.Command
}

type codexExecUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

// generateContentStructured drives `codex exec --json` — per-turn, one-shot,
// no tmux dependency. See MetadataKeyStructuredTransport doc comment for when
// to use this instead of the tmux interactive transport (tmux stays default).
func (c *CodexCLIAdapter) generateContentStructured(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	// Structured contract §7: close the stream channel after process exit or
	// error. Every return path below runs either before the event-parsing
	// goroutine starts, or after <-scannerDone (that goroutine has already
	// finished) — safe to close exactly once here on any return.
	if opts != nil && opts.StreamChan != nil {
		defer close(opts.StreamChan)
	}
	emitChunk := func(chunk llmtypes.StreamChunk) {
		if opts == nil || opts.StreamChan == nil {
			return
		}
		select {
		case opts.StreamChan <- chunk:
		case <-ctx.Done():
		}
	}

	binPath, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("codex not found in PATH: %w", err)
	}

	systemPrompt, conversationMessages := splitCodexSystemPrompt(messages)
	prompt := buildCodexStructuredPrompt(conversationMessages)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("codex-cli prompt is empty")
	}
	if strings.TrimSpace(systemPrompt) != "" {
		prompt = "[System Instructions]\n" + systemPrompt + "\n\n[User Message]\n" + prompt
	}

	// --- Gather configuration (transport-independent) ---
	workingDir := ""
	modelToUse := c.modelID
	sandboxMode := "workspace-write" // matches this session's flipped default (agent/coding_agent_integrations.go) — not read-only
	var mcpServersJSON string
	autoApproveMCPTools := false
	var configOverrides []string
	var disabledFeatures []string
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if dir, ok := opts.Metadata.Custom[MetadataKeyProjectDirID].(string); ok && strings.TrimSpace(dir) != "" {
			workingDir = strings.TrimSpace(dir)
		}
		if model, ok := opts.Metadata.Custom[MetadataKeyCodexModel].(string); ok && strings.TrimSpace(model) != "" {
			modelToUse = strings.TrimSpace(model)
		}
		if sandbox, ok := opts.Metadata.Custom[MetadataKeySandbox].(string); ok && strings.TrimSpace(sandbox) != "" {
			sandboxMode = strings.TrimSpace(sandbox)
		}
		if v, ok := opts.Metadata.Custom[MetadataKeyMCPServers].(string); ok {
			mcpServersJSON = v
		}
		if policy, ok := opts.Metadata.Custom[MetadataKeyApprovalPolicy].(string); ok {
			autoApproveMCPTools = strings.TrimSpace(policy) == "never"
		}
		if overrides, ok := opts.Metadata.Custom[MetadataKeyConfigOverrides].([]string); ok {
			configOverrides = overrides
		}
		// Bridge-only containment (mirrors the interactive/tmux adapter): when the
		// caller asks to deny codex's built-in shell, disable shell_tool + the
		// other native code-exec/escape features so the only remaining shell-like
		// tool is the MCP bridge. Without this the structured path let codex
		// bypass the bridge via native exec.
		if disable, ok := opts.Metadata.Custom[MetadataKeyDisableShellTool].(bool); ok && disable {
			disabledFeatures = append(disabledFeatures, codexBridgeOnlyDisabledFeatures...)
		}
		if features, ok := opts.Metadata.Custom[MetadataKeyDisableFeatures].(string); ok && strings.TrimSpace(features) != "" {
			for _, f := range strings.Split(features, ",") {
				if f = strings.TrimSpace(f); f != "" {
					disabledFeatures = append(disabledFeatures, f)
				}
			}
		}
	}

	// Reuse the SAME TOML session-profile mechanism the tmux path uses — large
	// tool schemas and bridge credentials belong in a profile file, never on
	// the command line (see codexcli_interactive_adapter.go's identical
	// rationale). The GLOBAL -p/--profile flag loads $CODEX_HOME/<name>.config.toml.
	sessionProfile, sessionProfileCleanup, err := writeCodexSessionMCPProfile(mcpServersJSON, autoApproveMCPTools)
	if err != nil {
		return nil, fmt.Errorf("codex session MCP profile: %w", err)
	}
	defer sessionProfileCleanup()

	// --- Build argv: multi-turn resume vs fresh one-shot ---
	// `codex exec resume <id>` continues a prior turn's native session. Unlike
	// plain `exec`, the resume subcommand does NOT accept --profile/--sandbox/-C,
	// so the MCP profile is supplied via the GLOBAL --profile flag (before the
	// subcommand) — keeping bridge secrets in the profile FILE, never on argv —
	// sandbox via a global -c override (not a secret), and cwd via cmd.Dir below.
	// Verified live against the real CLI: `codex exec resume <id> --json` restores
	// the prior turn's context, and the global profile layers the bridge MCP.
	resumeSessionID := strings.TrimSpace(codexResumeSessionIDFromOptions(opts))
	logCmd := "codex exec --json"
	if resumeSessionID != "" {
		logCmd = "codex exec resume <id> --json"
	}
	if workingDir != "" {
		// Was completely unwired until now. No --skill flag for codex (unlike
		// pi) — codex's own skill loader auto-discovers the projected directory
		// at session start, same as the tmux path
		// (codexcli_interactive_adapter.go), so projecting to disk before
		// launch is the entire fix. (Disk side-effect only; adds no argv.)
		if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
			_ = c.ProjectSkills(workingDir, skills)
		}
	}

	// argv SHAPE (the resume-vs-fresh ordering) is owned by the extracted,
	// unit-tested builder — see buildCodexStructuredArgs / TestBuildCodexStructuredArgs.
	args := buildCodexStructuredArgs(resumeSessionID, sessionProfile, sandboxMode, workingDir, modelToUse, disabledFeatures, configOverrides, prompt)

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = buildCodexStructuredEnv(c.apiKey)
	cmd.Stdin = strings.NewReader("") // codex exec reads stdin unless explicitly closed/empty; avoid any hang

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if c.logger != nil {
		c.logger.Infof("Executing Codex CLI structured: %s", logCmd)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex start: %w", err)
	}

	var finalContent string
	var totalUsage llmtypes.Usage
	var threadID string

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	scannerDone := make(chan struct{})
	go func() {
		defer close(scannerDone)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var event codexExecEvent
			if err := json.Unmarshal(line, &event); err != nil {
				if c.logger != nil {
					c.logger.Debugf("codex: failed to parse event: %v", err)
				}
				continue
			}
			if event.ThreadID != "" {
				threadID = event.ThreadID
			}
			switch event.Type {
			case "item.started":
				if event.Item != nil && isCodexToolItem(event.Item.Type) {
					emitChunk(llmtypes.StreamChunk{
						Type:       llmtypes.StreamChunkTypeToolCallStart,
						Content:    codexToolItemLabel(event.Item),
						ToolCallID: event.Item.ID,
					})
				}
			case "item.completed":
				if event.Item == nil {
					continue
				}
				switch {
				case event.Item.Type == "agent_message":
					if event.Item.Text != "" {
						finalContent = event.Item.Text
						emitChunk(llmtypes.StreamChunk{
							Type:    llmtypes.StreamChunkTypeContent,
							Content: event.Item.Text,
						})
					}
				case isCodexToolItem(event.Item.Type):
					emitChunk(llmtypes.StreamChunk{
						Type:       llmtypes.StreamChunkTypeToolCallEnd,
						Content:    event.Item.ID,
						ToolCallID: event.Item.ID,
					})
				}
			case "turn.completed":
				// End-of-turn teardown per the structured-CLI shutdown contract
				// (docs/coding_sdk_structured_contract.md §9).
				go procshutdown.Graceful(cmd, scannerDone, c.logger)
				if event.Usage != nil {
					totalUsage.InputTokens += event.Usage.InputTokens
					totalUsage.OutputTokens += event.Usage.OutputTokens
					totalUsage.TotalTokens += event.Usage.InputTokens + event.Usage.OutputTokens
					if event.Usage.CachedInputTokens > 0 {
						cacheRead := event.Usage.CachedInputTokens
						totalUsage.CacheTokens = &cacheRead
					}
				}
			}
		}
	}()
	<-scannerDone

	waitErr := cmd.Wait()
	content := strings.TrimSpace(finalContent)

	if waitErr != nil && content == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("codex run failed: %w: %s", waitErr, stderrStr)
		}
		return nil, fmt.Errorf("codex run failed: %w", waitErr)
	}
	if content == "" {
		return nil, fmt.Errorf("codex run returned no text output")
	}

	additional := map[string]any{
		"provider":        "codex-cli",
		"codex_mode":      "structured",
		"codex_thread_id": threadID,
		"codex_model":     modelToUse,
	}
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtrIfNonZero(totalUsage.InputTokens),
		OutputTokens: intPtrIfNonZero(totalUsage.OutputTokens),
		TotalTokens:  intPtrIfNonZero(totalUsage.TotalTokens),
		Additional:   additional,
	}
	if totalUsage.CacheTokens != nil && *totalUsage.CacheTokens > 0 {
		v := *totalUsage.CacheTokens
		genInfo.CachedContentTokens = &v
		additional["cache_read_input_tokens"] = v
	}
	costLookupModel := modelToUse
	if costLookupModel != "" {
		if meta, _ := c.GetModelMetadata(costLookupModel); meta != nil {
			if cost := llmtypes.ComputeUSDCostFromMetadata(meta, genInfo); cost > 0 {
				additional["cost_usd_estimated"] = cost
				additional["cost_model_id"] = costLookupModel
			}
		}
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        content,
				StopReason:     "stop",
				GenerationInfo: genInfo,
			},
		},
		Usage: &totalUsage,
	}, nil
}

// buildCodexStructuredPrompt concatenates the FULL non-system history — unlike
// buildCodexInteractivePrompt (tmux), a structured call is a fresh process
// each time with no persistent session to hold prior turns, so every call must
// carry the whole conversation itself.
func buildCodexStructuredPrompt(messages []llmtypes.MessageContent) string {
	var b strings.Builder
	for _, m := range messages {
		text := extractTextFromMessage(m)
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch m.Role {
		case llmtypes.ChatMessageTypeHuman:
			b.WriteString("User: ")
		case llmtypes.ChatMessageTypeAI:
			b.WriteString("Assistant: ")
		}
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func intPtrIfNonZero(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func buildCodexStructuredEnv(apiKey string) []string {
	env := os.Environ()
	if strings.TrimSpace(apiKey) != "" {
		env = append(env, "OPENAI_API_KEY="+strings.TrimSpace(apiKey))
	}
	return env
}
