package codexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// quotaExhaustedEntry records when a model's usage quota was exhausted.
type quotaExhaustedEntry struct {
	message   string    // original error message from Codex
	expiresAt time.Time // when the quota resets (parsed from error, or +1h default)
}

// globalQuotaCache is a process-wide cache of quota-exhausted Codex models.
// Keyed by modelID. Survives Agent recreation between workflow turns.
var (
	globalQuotaMu    sync.RWMutex
	globalQuotaCache = map[string]quotaExhaustedEntry{}

	codexRateLimitPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b429\b`),
		regexp.MustCompile(`\b503\b`),
		regexp.MustCompile(`(?i)\brate[- ]limit(ed|ing)?\b`),
		regexp.MustCompile(`(?i)\btoo many requests\b`),
		regexp.MustCompile(`(?i)\bservice unavailable\b`),
		regexp.MustCompile(`(?i)\busage limit\b`),
		regexp.MustCompile(`(?i)\boverloaded\b`),
	}
)

func looksLikeCodexRateLimit(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	for _, pattern := range codexRateLimitPatterns {
		if pattern.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// markQuotaExhausted records a quota exhaustion for modelID.
// It tries to parse "try again at HH:MM AM/PM" from msg to set an accurate expiry.
func markQuotaExhausted(modelID, msg string) {
	expiry := time.Now().Add(1 * time.Hour) // safe default

	// Try to parse "try again at HH:MM AM" or "try again at HH:MM PM"
	lower := strings.ToLower(msg)
	if idx := strings.Index(lower, "try again at "); idx >= 0 {
		rest := msg[idx+len("try again at "):]
		// rest looks like "8:32 PM." — parse it
		rest = strings.TrimRight(rest, ". ")
		if t, err := time.ParseInLocation("3:04 PM", rest, time.Local); err == nil {
			now := time.Now()
			candidate := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
			if candidate.Before(now) {
				candidate = candidate.Add(24 * time.Hour) // next day
			}
			expiry = candidate.Add(2 * time.Minute) // small buffer
		}
	}

	globalQuotaMu.Lock()
	globalQuotaCache[modelID] = quotaExhaustedEntry{message: msg, expiresAt: expiry}
	globalQuotaMu.Unlock()
}

// checkQuotaExhausted returns the cached error message if the model is still exhausted.
func checkQuotaExhausted(modelID string) (string, bool) {
	globalQuotaMu.RLock()
	entry, ok := globalQuotaCache[modelID]
	globalQuotaMu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		// Expired — evict
		globalQuotaMu.Lock()
		delete(globalQuotaCache, modelID)
		globalQuotaMu.Unlock()
		return "", false
	}
	return entry.message, true
}

// pendingToolCall tracks a tool call that has started but hasn't received its result yet
type pendingToolCall struct {
	toolName  string
	toolID    string
	toolArgs  string
	startTime time.Time
}

// CodexCLIAdapter implements the LLM interface for the OpenAI Codex CLI.
type CodexCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewCodexCLIAdapter creates a new instance of the CodexCLIAdapter.
func NewCodexCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *CodexCLIAdapter {
	return &CodexCLIAdapter{
		apiKey:  apiKey,
		modelID: modelID,
		logger:  logger,
	}
}

// inactivityTimeout is the maximum time to wait for output from the Codex CLI
// before killing the process.
const inactivityTimeout = 10 * time.Minute

// GenerateContent generates content using the OpenAI Codex CLI.
func (c *CodexCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	// 0a. Check process-wide quota cache — skip the CLI entirely if quota is known exhausted.
	if cachedMsg, exhausted := checkQuotaExhausted(c.modelID); exhausted {
		c.logger.Errorf("Codex CLI quota still exhausted for model %s (cached): %s", c.modelID, cachedMsg)
		return nil, fmt.Errorf("codex cli execution failed: usage limit still active for %s: %s", c.modelID, cachedMsg)
	}

	// 0b. Check for 'codex' binary
	if _, err := exec.LookPath("codex"); err != nil {
		return nil, fmt.Errorf("codex cli not found in PATH. Please install it first (npm install -g @openai/codex)")
	}

	// Parse options
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	// 1. Prepare Command Arguments
	// Use exec subcommand for non-interactive mode with JSON output
	args := []string{"exec", "--json"}

	// Set model if specified via metadata or adapter default
	modelToUse := c.modelID
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyCodexModel].(string); ok && model != "" {
			modelToUse = model
		}
	}
	if modelToUse != "" && modelToUse != "codex-cli" {
		args = append(args, "--model", modelToUse)
	}

	// Handle full-auto mode (default for non-interactive use).
	// Codex CLI 0.128 deprecates --full-auto and no longer uses it to approve
	// non-interactive MCP calls; those calls come back as "user cancelled MCP
	// tool call". The workflow runtime already provides its own sandbox and
	// bridge-level guards, so bypass Codex's interactive approval layer here.
	fullAuto := true // default to full-auto for programmatic use
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if fa, ok := opts.Metadata.Custom[MetadataKeyFullAuto].(bool); ok {
			fullAuto = fa
		}
	}
	if fullAuto {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}

	// Handle approval mode (overrides full-auto if set)
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mode, ok := opts.Metadata.Custom[MetadataKeyApprovalMode].(string); ok && mode != "" {
			args = append(args, "--ask-for-approval", mode)
		}
	}

	// Handle sandbox mode
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if sandbox, ok := opts.Metadata.Custom[MetadataKeySandbox].(string); ok && sandbox != "" {
			args = append(args, "--sandbox", sandbox)
		}
	}

	// Handle config profile
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if profile, ok := opts.Metadata.Custom[MetadataKeyConfigProfile].(string); ok && profile != "" {
			args = append(args, "--profile", profile)
		}
	}

	// Handle output schema for structured output
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if schema, ok := opts.Metadata.Custom[MetadataKeyOutputSchema].(string); ok && schema != "" {
			args = append(args, "--output-schema", schema)
		}
	}

	// Handle approval policy
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if policy, ok := opts.Metadata.Custom[MetadataKeyApprovalPolicy].(string); ok && policy != "" {
			args = append(args, "-c", fmt.Sprintf("approval_policy=%q", policy))
		}
	}

	// Handle reasoning effort
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if effort, ok := opts.Metadata.Custom[MetadataKeyReasoningEffort].(string); ok && effort != "" {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
		}
	}

	// Handle reasoning summary
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if summary, ok := opts.Metadata.Custom[MetadataKeyReasoningSummary].(string); ok && summary != "" {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_summary=%q", summary))
		}
	}

	// Handle disable shell tool (to only use MCP tools)
	// Uses --disable flag which is equivalent to -c features.shell_tool=false
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if disable, ok := opts.Metadata.Custom[MetadataKeyDisableShellTool].(bool); ok && disable {
			args = append(args, "--disable", "shell_tool")
		}
	}

	// Handle feature enable/disable flags
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if features, ok := opts.Metadata.Custom[MetadataKeyDisableFeatures].(string); ok && features != "" {
			for _, f := range strings.Split(features, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					args = append(args, "--disable", f)
				}
			}
		}
		if features, ok := opts.Metadata.Custom[MetadataKeyEnableFeatures].(string); ok && features != "" {
			for _, f := range strings.Split(features, ",") {
				f = strings.TrimSpace(f)
				if f != "" {
					args = append(args, "--enable", f)
				}
			}
		}
	}

	// Handle arbitrary config overrides
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if overrides, ok := opts.Metadata.Custom[MetadataKeyConfigOverrides].([]string); ok {
			for _, override := range overrides {
				args = append(args, "-c", override)
			}
		}
	}

	// Handle additional directories
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if dirs, ok := opts.Metadata.Custom[MetadataKeyAdditionalDirs].(string); ok && dirs != "" {
			for _, dir := range strings.Split(dirs, ",") {
				dir = strings.TrimSpace(dir)
				if dir != "" {
					args = append(args, "--add-dir", dir)
				}
			}
		}
	}

	// Handle working directory
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if dir, ok := opts.Metadata.Custom[MetadataKeyProjectDirID].(string); ok && dir != "" {
			args = append(args, "--cd", dir)
		}
	}

	// Handle resume session
	resumeID := ""
	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if rid, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && rid != "" {
			resumeID = rid
		}
	}

	// Extract system prompt and conversation messages
	var systemPrompts []string
	var convoMessages []llmtypes.MessageContent

	for _, msg := range messages {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			for _, part := range msg.Parts {
				if textPart, ok := part.(llmtypes.TextContent); ok {
					systemPrompts = append(systemPrompts, textPart.Text)
				}
			}
		} else {
			convoMessages = append(convoMessages, msg)
		}
	}

	// Pass system prompt via developer_instructions config instead of
	// prepending to the user message. This lets codex treat it as a proper
	// system-level instruction rather than part of the user prompt.
	if len(systemPrompts) > 0 {
		combined := strings.Join(systemPrompts, "\n\n")
		args = append(args, "-c", fmt.Sprintf("developer_instructions=%q", combined))
	}

	// 2. Build the prompt text
	// Codex CLI takes the prompt as a positional argument
	var promptText string
	if resumeID != "" {
		// Resume mode: `codex exec resume --json --dangerously-bypass-approvals-and-sandbox <session_id> "prompt"`
		// Resume flags go after the `resume` subcommand
		args = []string{"exec", "resume", "--json"}
		if fullAuto {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
		if modelToUse != "" && modelToUse != "codex-cli" {
			args = append(args, "--model", modelToUse)
		}
		args = append(args, resumeID)

		// Only send the last user message
		for i := len(convoMessages) - 1; i >= 0; i-- {
			if convoMessages[i].Role == llmtypes.ChatMessageTypeHuman {
				promptText = extractTextFromMessage(convoMessages[i])
				break
			}
		}
	} else if len(convoMessages) > 1 {
		// Multiple messages: build a conversation transcript (system prompt handled via developer_instructions)
		var parts []string
		for _, msg := range convoMessages {
			role := "User"
			if msg.Role == llmtypes.ChatMessageTypeAI {
				role = "Assistant"
			}
			text := extractTextFromMessage(msg)
			if text != "" {
				parts = append(parts, fmt.Sprintf("%s: %s", role, text))
			}
		}
		promptText = strings.Join(parts, "\n\n")
	} else {
		// Single message: extract user prompt (system prompt handled via developer_instructions)
		for i := len(convoMessages) - 1; i >= 0; i-- {
			if convoMessages[i].Role == llmtypes.ChatMessageTypeHuman {
				promptText = extractTextFromMessage(convoMessages[i])
				break
			}
		}
	}

	// Add prompt as positional argument
	if promptText != "" {
		args = append(args, promptText)
	}

	// 3. Execute Command
	c.logger.Infof("Executing Codex CLI: codex %v", args)
	cmd := exec.CommandContext(ctx, "codex", args...)

	// Build environment
	env := os.Environ()
	if c.apiKey != "" {
		env = append(env, "CODEX_API_KEY="+c.apiKey)
	}
	cmd.Env = env

	// Use Pipe for stdout to parse JSONL stream
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr via pipe for real-time error detection
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Put Codex CLI in its own process group so we can kill the entire tree
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start codex cli: %w", err)
	}

	// Monitor stderr in real-time
	var stderrBuf strings.Builder
	var detectedRateLimit atomic.Bool
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			stderrBuf.WriteString(line + "\n")
			// Detect rate limiting or API errors
			if !detectedRateLimit.Load() && looksLikeCodexRateLimit(line) {
				detectedRateLimit.Store(true)
				c.logger.Errorf("Codex CLI: rate limit/API overload detected in stderr: %s", truncate(line, 300))
				if opts.StreamChan != nil {
					opts.StreamChan <- llmtypes.StreamChunk{
						Type:    llmtypes.StreamChunkTypeContent,
						Content: "\n⚠️ Codex model is experiencing rate limiting. Retrying automatically, please wait…\n",
					}
				}
			}
		}
	}()

	// 4. Parse Streamed Output (JSONL, one JSON object per line)
	var finalResponse *llmtypes.ContentResponse
	decodeDone := make(chan struct{})
	pendingTools := make(map[string]*pendingToolCall)

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var threadID string
	var accumulatedText strings.Builder
	var totalInputTokens, totalOutputTokens, totalCachedInputTokens int
	var lastCLIErrorMessage atomic.Value

	// Inactivity watchdog
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	var pendingToolCalls atomic.Int64

	// Progress heartbeat
	var lastContentTime atomic.Int64
	lastContentTime.Store(time.Now().UnixNano())
	var heartbeatSent atomic.Bool
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if heartbeatSent.Load() {
					continue
				}
				lastNano := lastContentTime.Load()
				elapsed := time.Since(time.Unix(0, lastNano))
				if elapsed >= 30*time.Second && opts.StreamChan != nil {
					heartbeatSent.Store(true)
					c.logger.Infof("Codex CLI: no content for %ds, sending progress heartbeat", int(elapsed.Seconds()))
					select {
					case opts.StreamChan <- llmtypes.StreamChunk{
						Type:    llmtypes.StreamChunkTypeContent,
						Content: "\n⏳ Codex is still working on it, please wait…\n",
					}:
					default:
					}
				}
			case <-decodeDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Inactivity watchdog goroutine
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				lastNano := lastActivity.Load()
				elapsed := time.Since(time.Unix(0, lastNano))
				if elapsed >= inactivityTimeout {
					if pendingToolCalls.Load() > 0 {
						c.logger.Infof("Inactivity watchdog: no output for %v but %d tool call(s) in flight, resetting timer", elapsed, pendingToolCalls.Load())
						lastActivity.Store(time.Now().UnixNano())
						continue
					}
					c.logger.Errorf("Inactivity watchdog: no output for %v, killing Codex CLI process group", elapsed)
					if cmd.Process != nil {
						syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
					return
				}
			case <-decodeDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		c.logger.Infof("Starting Codex stream decode loop...")
		for scanner.Scan() {
			lastActivity.Store(time.Now().UnixNano())
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}

			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				c.logger.Errorf("Failed to decode Codex stream-json line: %v (line: %s)", err, truncate(line, 200))
				continue
			}
			c.logger.Debugf("Codex CLI raw stream line: %s", truncate(line, 1000))

			msgType, _ := raw["type"].(string)
			switch msgType {

			case "thread.started":
				if tid, ok := raw["thread_id"].(string); ok {
					threadID = tid
				}
				c.logger.Infof("Codex thread started: %s", threadID)

			case "turn.started":
				c.logger.Debugf("Codex turn started")

			case "turn.completed":
				// Extract usage from turn.completed
				// Format: {"usage":{"input_tokens":N,"cached_input_tokens":N,"output_tokens":N}}
				if usage, ok := raw["usage"].(map[string]interface{}); ok {
					if v, ok := usage["input_tokens"].(float64); ok {
						totalInputTokens += int(v)
					}
					if v, ok := usage["output_tokens"].(float64); ok {
						totalOutputTokens += int(v)
					}
					if v, ok := usage["cached_input_tokens"].(float64); ok {
						totalCachedInputTokens += int(v)
					}
				}
				c.logger.Infof("Codex turn completed, cumulative usage: input=%d cached=%d output=%d", totalInputTokens, totalCachedInputTokens, totalOutputTokens)

			case "item.started":
				item, _ := raw["item"].(map[string]interface{})
				if item == nil {
					continue
				}
				itemType, _ := item["type"].(string)
				itemID, _ := item["id"].(string)

				switch itemType {
				case "command_execution":
					// Command execution starting — "command" field has the full command string
					// e.g. {"id":"item_1","type":"command_execution","command":"/bin/zsh -lc ls","status":"in_progress"}
					toolName := "command_execution"
					toolArgs := ""
					if cmdStr, ok := item["command"].(string); ok && cmdStr != "" {
						toolName = cmdStr
						toolArgs = cmdStr
					}

					pendingTools[itemID] = &pendingToolCall{
						toolName:  toolName,
						toolID:    itemID,
						toolArgs:  toolArgs,
						startTime: time.Now(),
					}
					pendingToolCalls.Add(1)
					lastContentTime.Store(time.Now().UnixNano())

					if opts.StreamChan != nil {
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:       llmtypes.StreamChunkTypeToolCallStart,
							ToolName:   toolName,
							ToolCallID: itemID,
							ToolArgs:   toolArgs,
						}
					}

				case "file_change":
					// File change (apply_patch) starting
					toolName := "apply_patch"
					toolArgs := ""
					if changes, ok := item["changes"].([]interface{}); ok {
						if changesBytes, err := json.Marshal(changes); err == nil {
							toolArgs = string(changesBytes)
						}
					}

					pendingTools[itemID] = &pendingToolCall{
						toolName:  toolName,
						toolID:    itemID,
						toolArgs:  toolArgs,
						startTime: time.Now(),
					}
					pendingToolCalls.Add(1)
					lastContentTime.Store(time.Now().UnixNano())

					if opts.StreamChan != nil {
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:       llmtypes.StreamChunkTypeToolCallStart,
							ToolName:   toolName,
							ToolCallID: itemID,
							ToolArgs:   toolArgs,
						}
					}

				case "web_search":
					// Web search starting
					toolName := "web_search"
					toolArgs := ""
					if query, ok := item["query"].(string); ok && query != "" {
						toolArgs = query
					}

					pendingTools[itemID] = &pendingToolCall{
						toolName:  toolName,
						toolID:    itemID,
						toolArgs:  toolArgs,
						startTime: time.Now(),
					}
					pendingToolCalls.Add(1)
					lastContentTime.Store(time.Now().UnixNano())

					if opts.StreamChan != nil {
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:       llmtypes.StreamChunkTypeToolCallStart,
							ToolName:   toolName,
							ToolCallID: itemID,
							ToolArgs:   toolArgs,
						}
					}

				case "mcp_call", "mcp_tool_call":
					// MCP tool call starting
					toolName := "mcp_call"
					if name, ok := item["tool"].(string); ok && name != "" {
						toolName = name
					} else if name, ok := item["name"].(string); ok && name != "" {
						toolName = name
					}
					// Prepend server name if available
					if server, ok := item["server"].(string); ok && server != "" {
						toolName = "mcp__" + server + "__" + toolName
					}
					toolArgs := ""
					if argsRaw, ok := item["arguments"]; ok && argsRaw != nil {
						if argsBytes, err := json.Marshal(argsRaw); err == nil {
							toolArgs = string(argsBytes)
						}
					}

					pendingTools[itemID] = &pendingToolCall{
						toolName:  toolName,
						toolID:    itemID,
						toolArgs:  toolArgs,
						startTime: time.Now(),
					}
					pendingToolCalls.Add(1)
					lastContentTime.Store(time.Now().UnixNano())

					if opts.StreamChan != nil {
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:       llmtypes.StreamChunkTypeToolCallStart,
							ToolName:   toolName,
							ToolCallID: itemID,
							ToolArgs:   toolArgs,
						}
					}
				}

			case "item.completed":
				item, _ := raw["item"].(map[string]interface{})
				if item == nil {
					continue
				}
				itemType, _ := item["type"].(string)
				itemID, _ := item["id"].(string)

				switch itemType {
				case "agent_message":
					// Codex uses "text" field for agent messages (not "content")
					if text, ok := item["text"].(string); ok && text != "" {
						if accumulatedText.Len() > 0 {
							accumulatedText.WriteString("\n\n")
						}
						accumulatedText.WriteString(text)
						lastContentTime.Store(time.Now().UnixNano())
						heartbeatSent.Store(false)
						if opts.StreamChan != nil {
							opts.StreamChan <- llmtypes.StreamChunk{
								Type:    llmtypes.StreamChunkTypeContent,
								Content: "\n\n" + text,
							}
						}
					}
					// Also check "content" as fallback in case format changes
					if content, ok := item["content"].(string); ok && content != "" {
						if accumulatedText.Len() == 0 || !strings.Contains(accumulatedText.String(), content) {
							accumulatedText.WriteString(content)
							lastContentTime.Store(time.Now().UnixNano())
							heartbeatSent.Store(false)
							if opts.StreamChan != nil {
								opts.StreamChan <- llmtypes.StreamChunk{
									Type:    llmtypes.StreamChunkTypeContent,
									Content: content,
								}
							}
						}
					}

				case "command_execution":
					// Command execution completed
					// "aggregated_output" has the command output, "exit_code" has the exit code
					resultContent, _ := item["aggregated_output"].(string)

					lastContentTime.Store(time.Now().UnixNano())
					if pt, ok := pendingTools[itemID]; ok {
						duration := time.Since(pt.startTime)
						if opts.StreamChan != nil {
							opts.StreamChan <- llmtypes.StreamChunk{
								Type:         llmtypes.StreamChunkTypeToolCallEnd,
								ToolName:     pt.toolName,
								ToolCallID:   pt.toolID,
								ToolArgs:     pt.toolArgs,
								ToolResult:   resultContent,
								ToolDuration: duration,
							}
						}
						delete(pendingTools, itemID)
						pendingToolCalls.Add(-1)
					}

				case "mcp_call", "mcp_tool_call":
					// MCP tool call completed
					resultContent := ""
					// Result can be nested: {"result":{"content":[{"type":"text","text":"..."}]}}
					if resultObj, ok := item["result"].(map[string]interface{}); ok {
						if content, ok := resultObj["content"].([]interface{}); ok {
							var parts []string
							for _, c := range content {
								if cm, ok := c.(map[string]interface{}); ok {
									if text, ok := cm["text"].(string); ok {
										parts = append(parts, text)
									}
								}
							}
							resultContent = strings.Join(parts, "")
						}
					} else if output, ok := item["output"].(string); ok {
						resultContent = output
					} else if result, ok := item["result"].(string); ok {
						resultContent = result
					}
					if resultContent == "" {
						if errObj, ok := item["error"].(map[string]interface{}); ok {
							if msg, ok := errObj["message"].(string); ok {
								resultContent = msg
							}
						}
					}

					lastContentTime.Store(time.Now().UnixNano())
					if pt, ok := pendingTools[itemID]; ok {
						duration := time.Since(pt.startTime)
						if opts.StreamChan != nil {
							opts.StreamChan <- llmtypes.StreamChunk{
								Type:         llmtypes.StreamChunkTypeToolCallEnd,
								ToolName:     pt.toolName,
								ToolCallID:   pt.toolID,
								ToolArgs:     pt.toolArgs,
								ToolResult:   resultContent,
								ToolDuration: duration,
							}
						}
						delete(pendingTools, itemID)
						pendingToolCalls.Add(-1)
					}

				case "web_search":
					// Web search completed — extract query and action details
					resultContent := ""
					if query, ok := item["query"].(string); ok && query != "" {
						resultContent = query
					}
					if action, ok := item["action"].(map[string]interface{}); ok {
						if actionBytes, err := json.Marshal(action); err == nil {
							resultContent = string(actionBytes)
						}
					}

					lastContentTime.Store(time.Now().UnixNano())
					if pt, ok := pendingTools[itemID]; ok {
						// Update args with actual query (item.started has empty query)
						if query, ok := item["query"].(string); ok && query != "" {
							pt.toolArgs = query
						}
						duration := time.Since(pt.startTime)
						if opts.StreamChan != nil {
							opts.StreamChan <- llmtypes.StreamChunk{
								Type:         llmtypes.StreamChunkTypeToolCallEnd,
								ToolName:     pt.toolName,
								ToolCallID:   pt.toolID,
								ToolArgs:     pt.toolArgs,
								ToolResult:   resultContent,
								ToolDuration: duration,
							}
						}
						delete(pendingTools, itemID)
						pendingToolCalls.Add(-1)
					}

				case "file_change":
					// File change items (apply_patch) — no item.started, just completed
					resultContent := ""
					if changes, ok := item["changes"].([]interface{}); ok {
						if changesBytes, err := json.Marshal(changes); err == nil {
							resultContent = string(changesBytes)
						}
					}
					// Emit as a complete tool call (start + end) since file_change has no item.started
					if opts.StreamChan != nil {
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:       llmtypes.StreamChunkTypeToolCallStart,
							ToolName:   "apply_patch",
							ToolCallID: itemID,
							ToolArgs:   resultContent,
						}
						opts.StreamChan <- llmtypes.StreamChunk{
							Type:         llmtypes.StreamChunkTypeToolCallEnd,
							ToolName:     "apply_patch",
							ToolCallID:   itemID,
							ToolArgs:     resultContent,
							ToolResult:   resultContent,
							ToolDuration: 0,
						}
					}

				case "reasoning", "todo_list":
					// Reasoning/plan items — log but don't stream to user
					c.logger.Debugf("Codex %s item: %s", itemType, itemID)
				}

			case "item.updated":
				// Plan/todo list updates — just log
				c.logger.Debugf("Codex item updated: %s", truncate(line, 200))

			case "error":
				errMsg := extractCodexErrorMessage(raw)
				if strings.TrimSpace(errMsg) != "" {
					lastCLIErrorMessage.Store(strings.TrimSpace(errMsg))
				}
				c.logger.Errorf("Codex CLI error event: %s", errMsg)

			case "turn.failed":
				// Turn failed — extract error details
				if errObj, ok := raw["error"].(map[string]interface{}); ok {
					errMsg, _ := errObj["message"].(string)
					if strings.TrimSpace(errMsg) != "" {
						lastCLIErrorMessage.Store(strings.TrimSpace(errMsg))
					}
					c.logger.Errorf("Codex CLI turn failed: %s", errMsg)
				}

			case "event_msg":
				// Handle event_msg type (used for token counts, rate limits, etc.)
				if payload, ok := raw["payload"].(map[string]interface{}); ok {
					payloadType, _ := payload["type"].(string)
					if payloadType == "token_count" {
						if info, ok := payload["info"].(map[string]interface{}); ok {
							if v, ok := info["input_tokens"].(float64); ok {
								totalInputTokens = int(v)
							}
							if v, ok := info["output_tokens"].(float64); ok {
								totalOutputTokens = int(v)
							}
						}
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			c.logger.Errorf("Scanner error reading Codex CLI stdout: %v", err)
		}

		c.logger.Infof("[LIFECYCLE] decode goroutine done, broadcasting close(decodeDone)")
		close(decodeDone)
	}()

	// Wait for command completion or context cancellation
	var cmdErr error
	select {
	case <-ctx.Done():
		c.logger.Errorf("Context cancelled/timed out: %v", ctx.Err())
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-decodeDone
		cmd.Wait()
		cmdErr = ctx.Err()
	case <-decodeDone:
		cmdErr = cmd.Wait()
	}

	// Wait for all goroutines to exit
	<-watchdogDone
	<-heartbeatDone
	<-stderrDone

	// Log stderr
	if stderrOutput := stderrBuf.String(); stderrOutput != "" {
		c.logger.Infof("Codex CLI stderr:\n%s", stderrOutput)
	}

	// Flush any remaining pending tool calls
	for _, pt := range pendingTools {
		if opts.StreamChan != nil {
			opts.StreamChan <- llmtypes.StreamChunk{
				Type:         llmtypes.StreamChunkTypeToolCallEnd,
				ToolName:     pt.toolName,
				ToolCallID:   pt.toolID,
				ToolArgs:     pt.toolArgs,
				ToolDuration: time.Since(pt.startTime),
			}
		}
	}

	// Build final response
	totalTokens := totalInputTokens + totalOutputTokens
	resultText := accumulatedText.String()

	additional := map[string]interface{}{
		"codex_thread_id": threadID,
	}
	if totalCachedInputTokens > 0 {
		additional["codex_cached_input_tokens"] = totalCachedInputTokens
	}

	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  &totalInputTokens,
		OutputTokens: &totalOutputTokens,
		TotalTokens:  &totalTokens,
		Additional:   additional,
	}
	if totalCachedInputTokens > 0 {
		genInfo.CachedContentTokens = &totalCachedInputTokens
	}

	finalResponse = &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        resultText,
				GenerationInfo: genInfo,
			},
		},
		Usage: &llmtypes.Usage{
			InputTokens:  totalInputTokens,
			OutputTokens: totalOutputTokens,
			TotalTokens:  totalTokens,
			CacheTokens:  &totalCachedInputTokens,
		},
	}

	if cmdErr != nil {
		c.logger.Errorf("Codex CLI failed with error: %v. stderr: %s", cmdErr, stderrBuf.String())
		if resultText == "" {
			if opts.StreamChan != nil {
				close(opts.StreamChan)
			}
			if detectedRateLimit.Load() {
				return nil, fmt.Errorf("codex cli rate limited: model is experiencing high demand. Please try again later")
			}
			if errMsg, ok := lastCLIErrorMessage.Load().(string); ok && errMsg != "" {
				// Cache quota exhaustion globally so future agent instances skip the CLI immediately.
				if strings.Contains(strings.ToLower(errMsg), "usage limit") ||
					strings.Contains(strings.ToLower(errMsg), "hit your usage") {
					markQuotaExhausted(c.modelID, errMsg)
					c.logger.Errorf("Codex CLI usage quota exhausted for %s — cached until reset time", c.modelID)
				}
				return nil, fmt.Errorf("codex cli execution failed: %s", errMsg)
			}
			if stderrOutput := strings.TrimSpace(stderrBuf.String()); stderrOutput != "" {
				return nil, fmt.Errorf("codex cli execution failed: %s", stderrOutput)
			}
			return nil, fmt.Errorf("codex cli execution failed: %w", cmdErr)
		}
	}

	if resultText == "" && threadID != "" {
		c.logger.Infof("Empty result detected, retrying with finalization prompt (threadID=%s)", threadID)
		retryResp, retryErr := c.retryForFinalAnswer(ctx, threadID, opts, modelToUse, fullAuto)
		if retryErr != nil {
			c.logger.Errorf("Retry for final answer failed: %v", retryErr)
		} else if retryResp != nil && len(retryResp.Choices) > 0 && retryResp.Choices[0].Content != "" {
			c.logger.Infof("Retry produced final answer (%d chars)", len(retryResp.Choices[0].Content))
			finalResponse = retryResp
		}
	}

	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	return finalResponse, nil
}

// SearchWeb uses Codex CLI's native web search capability and returns the final text response.
func (c *CodexCLIAdapter) SearchWeb(ctx context.Context, query string, options ...llmtypes.CallOption) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	searchPrompt := "Use web search to answer the following query.\n\n" + query
	resp, err := c.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: searchPrompt},
			},
		},
	}, options...)
	if err != nil {
		return "", err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("codex cli web search returned no response")
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	if content == "" {
		return "", fmt.Errorf("codex cli web search returned empty response")
	}
	return content, nil
}

// GetModelID returns the model ID.
func (c *CodexCLIAdapter) GetModelID() string {
	return c.modelID
}

// GetModelMetadata returns metadata for the model.
func (c *CodexCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = c.modelID
	}

	// Known model metadata
	switch {
	case strings.Contains(modelID, "gpt-5.4-mini"):
		return &llmtypes.ModelMetadata{
			ModelID:                    modelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.4 Mini",
			ContextWindow:              400000,
			InputCostPer1MTokens:       0.75,
			OutputCostPer1MTokens:      4.50,
			CachedInputCostPer1MTokens: 0.075,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"low", "medium", "high", "xhigh"},
		}, nil

	case strings.Contains(modelID, "gpt-5.4"):
		return &llmtypes.ModelMetadata{
			ModelID:                    modelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.4",
			ContextWindow:              1100000,
			InputCostPer1MTokens:       2.50,
			OutputCostPer1MTokens:      15.00,
			CachedInputCostPer1MTokens: 0.25,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"none", "low", "medium", "high", "xhigh"},
		}, nil

	case strings.Contains(modelID, "gpt-5.3-codex-spark"):
		return &llmtypes.ModelMetadata{
			ModelID:                    modelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.3-Codex-Spark",
			ContextWindow:              400000,
			InputCostPer1MTokens:       1.75,
			OutputCostPer1MTokens:      14.00,
			CachedInputCostPer1MTokens: 0.175,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"low", "medium", "high", "xhigh"},
		}, nil

	case strings.Contains(modelID, "gpt-5.3-codex"):
		return &llmtypes.ModelMetadata{
			ModelID:                    modelID,
			Provider:                   "codex-cli",
			ModelName:                  "GPT-5.3-Codex",
			ContextWindow:              400000,
			InputCostPer1MTokens:       1.75,
			OutputCostPer1MTokens:      14.00,
			CachedInputCostPer1MTokens: 0.175,
			SupportsToolCalls:          true,
			SupportsJSONMode:           true,
			SupportsReasoningEffort:    true,
			ReasoningEffortLevels:      []string{"low", "medium", "high", "xhigh"},
		}, nil

	default:
		// Generic fallback for unknown models
		return &llmtypes.ModelMetadata{
			ModelID:                 modelID,
			Provider:                "codex-cli",
			ModelName:               "OpenAI Codex CLI (pricing varies)",
			ContextWindow:           200000,
			SupportsToolCalls:       true,
			SupportsReasoningEffort: true,
			ReasoningEffortLevels:   []string{"low", "medium", "high", "xhigh"},
		}, nil
	}
}

// --- Helper Functions ---

func extractCodexErrorMessage(raw map[string]interface{}) string {
	if msg, ok := raw["message"].(string); ok && strings.TrimSpace(msg) != "" {
		return msg
	}
	switch errVal := raw["error"].(type) {
	case string:
		return errVal
	case map[string]interface{}:
		if msg, ok := errVal["message"].(string); ok && strings.TrimSpace(msg) != "" {
			return msg
		}
		if typ, ok := errVal["type"].(string); ok && strings.TrimSpace(typ) != "" {
			return typ
		}
	}
	return ""
}

func extractTextFromMessage(msg llmtypes.MessageContent) string {
	var parts []string
	for _, part := range msg.Parts {
		if textPart, ok := part.(llmtypes.TextContent); ok {
			parts = append(parts, textPart.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// retryForFinalAnswer resumes a Codex CLI session that produced an empty result.
func (c *CodexCLIAdapter) retryForFinalAnswer(
	ctx context.Context,
	threadID string,
	opts *llmtypes.CallOptions,
	modelID string,
	fullAuto bool,
) (*llmtypes.ContentResponse, error) {
	finalizationPrompt := "You have run out of turns. Please provide your final answer now based on what you have accomplished so far. Summarize results, findings, and any remaining work."

	args := []string{"exec", "resume", "--json"}
	if fullAuto {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if modelID != "" && modelID != "codex-cli" {
		args = append(args, "--model", modelID)
	}
	args = append(args, threadID, finalizationPrompt)

	c.logger.Infof("Retry: executing Codex CLI: codex %v", args)
	cmd := exec.CommandContext(ctx, "codex", args...)

	env := os.Environ()
	if c.apiKey != "" {
		env = append(env, "CODEX_API_KEY="+c.apiKey)
	}
	cmd.Env = env

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("retry: failed to create stdout pipe: %w", err)
	}

	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("retry: failed to start codex cli: %w", err)
	}

	var retryAccumulatedText strings.Builder
	var retryInputTokens, retryOutputTokens int
	var retryLastCLIErrorMessage atomic.Value
	decodeDone := make(chan struct{})

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	// Inactivity watchdog for retry
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				lastNano := lastActivity.Load()
				elapsed := time.Since(time.Unix(0, lastNano))
				if elapsed >= inactivityTimeout {
					c.logger.Errorf("Retry: inactivity watchdog: no output for %v, killing process group", elapsed)
					if cmd.Process != nil {
						syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
					return
				}
			case <-decodeDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		for scanner.Scan() {
			lastActivity.Store(time.Now().UnixNano())
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(line), &raw); err != nil {
				c.logger.Errorf("Retry: failed to decode stream-json: %v", err)
				continue
			}

			msgType, _ := raw["type"].(string)
			switch msgType {
			case "item.completed":
				item, _ := raw["item"].(map[string]interface{})
				if item == nil {
					continue
				}
				itemType, _ := item["type"].(string)
				if itemType == "agent_message" {
					if content, ok := item["content"].(string); ok && content != "" {
						retryAccumulatedText.WriteString(content)
						if opts.StreamChan != nil {
							opts.StreamChan <- llmtypes.StreamChunk{
								Type:    llmtypes.StreamChunkTypeContent,
								Content: content,
							}
						}
					}
					if contentArr, ok := item["content"].([]interface{}); ok {
						for _, part := range contentArr {
							if partMap, ok := part.(map[string]interface{}); ok {
								if text, ok := partMap["text"].(string); ok && text != "" {
									retryAccumulatedText.WriteString(text)
									if opts.StreamChan != nil {
										opts.StreamChan <- llmtypes.StreamChunk{
											Type:    llmtypes.StreamChunkTypeContent,
											Content: text,
										}
									}
								}
							}
						}
					}
				}

			case "turn.completed":
				if usage, ok := raw["usage"].(map[string]interface{}); ok {
					if v, ok := usage["input_tokens"].(float64); ok {
						retryInputTokens += int(v)
					}
					if v, ok := usage["output_tokens"].(float64); ok {
						retryOutputTokens += int(v)
					}
				}

			case "error":
				if msg, ok := raw["message"].(string); ok && strings.TrimSpace(msg) != "" {
					retryLastCLIErrorMessage.Store(strings.TrimSpace(msg))
					c.logger.Errorf("Retry: Codex CLI error event: %s", msg)
				}

			case "turn.failed":
				if errObj, ok := raw["error"].(map[string]interface{}); ok {
					if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
						retryLastCLIErrorMessage.Store(strings.TrimSpace(msg))
						c.logger.Errorf("Retry: Codex CLI turn failed: %s", msg)
					}
				}
			}
		}
		close(decodeDone)
	}()

	var cmdErr error
	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-decodeDone
		cmd.Wait()
		cmdErr = ctx.Err()
	case <-decodeDone:
		cmdErr = cmd.Wait()
	}

	<-watchdogDone

	if stderrOutput := stderrBuf.String(); stderrOutput != "" {
		c.logger.Infof("Retry: Codex CLI stderr:\n%s", stderrOutput)
	}

	if cmdErr != nil {
		c.logger.Errorf("Retry: Codex CLI failed: %v", cmdErr)
		if retryAccumulatedText.Len() == 0 {
			if errMsg, ok := retryLastCLIErrorMessage.Load().(string); ok && errMsg != "" {
				return nil, fmt.Errorf("retry: codex cli execution failed: %s", errMsg)
			}
			if stderrOutput := strings.TrimSpace(stderrBuf.String()); stderrOutput != "" {
				return nil, fmt.Errorf("retry: codex cli execution failed: %s", stderrOutput)
			}
			return nil, fmt.Errorf("retry: codex cli execution failed: %w", cmdErr)
		}
	}

	totalTokens := retryInputTokens + retryOutputTokens
	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content: retryAccumulatedText.String(),
				GenerationInfo: &llmtypes.GenerationInfo{
					InputTokens:  &retryInputTokens,
					OutputTokens: &retryOutputTokens,
					TotalTokens:  &totalTokens,
					Additional:   map[string]interface{}{},
				},
			},
		},
		Usage: &llmtypes.Usage{
			InputTokens:  retryInputTokens,
			OutputTokens: retryOutputTokens,
			TotalTokens:  totalTokens,
		},
	}, nil
}
