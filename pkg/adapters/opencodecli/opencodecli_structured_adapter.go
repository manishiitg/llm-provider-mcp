package opencodecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
)

// errOpencodeSilentEmpty is the sentinel returned by runOpencodeAttempt
// when opencode exited 0 but emitted no text part. Observed on cold-start
// of `opencode run --format json` (v1.15.4): a step_start event is
// emitted, then the process exits cleanly without any text events. The
// outer generateContentStructured retries once on this sentinel.
var errOpencodeSilentEmpty = errors.New("opencode emitted no text on clean exit")

type opencodeAttemptResult struct {
	textParts        []string
	usage            llmtypes.Usage
	sessionID        string
	lastFinishReason string
}

type opencodeEvent struct {
	Type      string          `json:"type"`
	Timestamp float64         `json:"timestamp"`
	SessionID string          `json:"sessionID"`
	Part      json.RawMessage `json:"part"`
}

type opencodeTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type opencodeToolUsePart struct {
	Type   string               `json:"type"`
	Tool   string               `json:"tool"`
	CallID string               `json:"callID"`
	State  opencodeToolUseState `json:"state"`
}

type opencodeToolUseState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
}

type opencodeStepFinishPart struct {
	Reason string                   `json:"reason"`
	Tokens opencodeStepFinishTokens `json:"tokens"`
	Cost   float64                  `json:"cost"`
}

type opencodeStepFinishTokens struct {
	Total     int                     `json:"total"`
	Input     int                     `json:"input"`
	Output    int                     `json:"output"`
	Reasoning int                     `json:"reasoning"`
	Cache     opencodeStepFinishCache `json:"cache"`
}

type opencodeStepFinishCache struct {
	Write int `json:"write"`
	Read  int `json:"read"`
}

func (c *OpenCodeCLIAdapter) generateContentStructured(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions, sink *llmtypes.StreamSink) (*llmtypes.ContentResponse, error) {
	emitChunk := func(chunk llmtypes.StreamChunk) {
		if sink != nil {
			if err := sink.Emit(ctx, chunk); err != nil {
				c.logDebugf("opencode: stream emit failed: %v", err)
			}
			return
		}
		if opts == nil || opts.StreamChan == nil {
			return
		}
		select {
		case opts.StreamChan <- chunk:
		case <-ctx.Done():
		}
	}

	binPath, err := opencodeBinaryPath()
	if err != nil {
		return nil, err
	}

	systemPrompt, conversationMessages := splitOpenCodeSystemPrompt(messages)
	prompt := buildOpenCodePrompt(conversationMessages, false)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("opencode-cli prompt is empty")
	}

	if strings.TrimSpace(systemPrompt) != "" {
		prompt = "[System Instructions]\n" + systemPrompt + "\n\n[User Message]\n" + prompt
	}

	args := []string{"run", "--format", "json"}

	dangerouslySkip := true
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if v, ok := opts.Metadata.Custom[MetadataKeyDangerouslySkipPermissions]; ok {
			if b, isBool := v.(bool); isBool {
				dangerouslySkip = b
			}
		}
	}
	if dangerouslySkip {
		args = append(args, "--dangerously-skip-permissions")
	}

	workingDir := opencodeWorkingDirFromOptions(opts)
	if workingDir != "" {
		args = append(args, "--dir", workingDir)
	}

	var configCleanups []func()
	defer func() {
		for _, fn := range configCleanups {
			fn()
		}
	}()
	if workingDir != "" && opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if configJSON, ok := opts.Metadata.Custom[MetadataKeyProjectConfig].(string); ok && strings.TrimSpace(configJSON) != "" {
			cleanup, werr := writeOpenCodeRestoredFile(filepath.Join(workingDir, "opencode.jsonc"), []byte(configJSON))
			if werr != nil {
				return nil, fmt.Errorf("opencode project config: %w", werr)
			}
			configCleanups = append(configCleanups, cleanup)
		}
		if mcpJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpJSON) != "" {
			configJSON, merr := buildOpenCodeMCPConfigJSON(mcpJSON)
			if merr != nil {
				return nil, merr
			}
			cleanup, werr := writeOpenCodeRestoredFile(filepath.Join(workingDir, "opencode.jsonc"), configJSON)
			if werr != nil {
				return nil, fmt.Errorf("opencode MCP config: %w", werr)
			}
			configCleanups = append(configCleanups, cleanup)
		}
		// OFF-by-default: also drop the system prompt at
		// <workingDir>/AGENTS.md so the workspace itself carries
		// OpenCode's conventional project instructions. Byte-restore via
		// the same configCleanups defer above keeps operator-owned
		// content safe on successful runs. We piggyback on the existing
		// systemPrompt extracted at the top of the function so the
		// in-prompt "[System Instructions]" prefix and the workspace
		// AGENTS.md stay in lockstep.
		if enabled, _ := opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile].(bool); enabled && strings.TrimSpace(systemPrompt) != "" {
			body := []byte("<!-- mlp-session-instructions: orchestrator-generated per-session system prompt. Auto-removed at session cleanup. -->\n\n" + systemPrompt)
			cleanup, werr := writeOpenCodeRestoredFile(filepath.Join(workingDir, "AGENTS.md"), body)
			if werr != nil {
				return nil, fmt.Errorf("opencode project instruction file: %w", werr)
			}
			configCleanups = append(configCleanups, cleanup)
		}
	}

	// Resolve the model id. If the call is scoped to a sub-provider tile
	// (Kimi/DeepSeek/Qwen/MiniMax/GLM/Free), tier shortcuts resolve inside
	// that tile's namespace; otherwise we fall back to the legacy global
	// resolver.
	//
	// Precedence: call-time options win over the adapter's construction-
	// time default. That lets a dispatcher rebuild credentials per-call
	// without rebuilding the adapter.
	var activeSubProvider *OpenCodeSubProvider
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if id, ok := opts.Metadata.Custom[MetadataKeySubProviderID].(string); ok && strings.TrimSpace(id) != "" {
			if sp, found := FindOpenCodeSubProvider(strings.TrimSpace(id)); found {
				activeSubProvider = &sp
			}
		}
	}
	if activeSubProvider == nil && c.defaultSubProviderID != "" {
		if sp, found := FindOpenCodeSubProvider(c.defaultSubProviderID); found {
			activeSubProvider = &sp
			// Also inherit the construction-time API key for this tile
			// unless the call already provided one via options.
			if c.defaultSubProviderAPIKey != "" {
				if opts.Metadata == nil {
					opts.Metadata = &llmtypes.Metadata{Custom: map[string]interface{}{}}
				}
				if opts.Metadata.Custom == nil {
					opts.Metadata.Custom = map[string]interface{}{}
				}
				existing, _ := opts.Metadata.Custom[MetadataKeySubProviderAPIKeys].(map[string]string)
				merged := make(map[string]string, len(existing)+1)
				for k, v := range existing {
					merged[k] = v
				}
				if _, hasKey := merged[sp.APIKeyEnvVar]; !hasKey && sp.APIKeyEnvVar != "" {
					merged[sp.APIKeyEnvVar] = c.defaultSubProviderAPIKey
				}
				opts.Metadata.Custom[MetadataKeySubProviderAPIKeys] = merged
			}
		}
	}

	rawModel := strings.TrimSpace(c.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyOpenCodeModel].(string); ok && strings.TrimSpace(model) != "" {
			rawModel = strings.TrimSpace(model)
		}
	}
	var modelToUse string
	if activeSubProvider != nil {
		modelToUse = resolveOpenCodeSubProviderModelID(*activeSubProvider, rawModel)
	} else {
		modelToUse = resolveOpenCodeCLIModelID(rawModel)
	}
	if modelToUse != "" {
		args = append(args, "--model", modelToUse)
	}

	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if sessionID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && strings.TrimSpace(sessionID) != "" {
			args = append(args, "--session", strings.TrimSpace(sessionID))
		} else if cont, ok := opts.Metadata.Custom[MetadataKeyContinueLastSession].(bool); ok && cont {
			args = append(args, "--continue")
		}
	}

	args = append(args, "--", prompt)

	env := buildOpenCodeEnvForCall(c.apiKey, activeSubProvider, opts)

	c.logInfof("Executing OpenCode CLI structured: opencode %s", strings.Join(args[:3], " "))
	res, err := c.runOpencodeAttempt(ctx, binPath, args, env, workingDir, emitChunk)
	if errors.Is(err, errOpencodeSilentEmpty) {
		// Retry once. opencode v1.15.4 occasionally exits 0 with only
		// a step_start event on first invocation in a process (likely
		// a cold-start race in its bundled provider init). A second
		// invocation against the same args+env reliably succeeds.
		// Retry is unconditional-but-bounded: we trade one extra
		// opencode launch for resilience to that known flake. The
		// stream channel (if any) received nothing on the first
		// attempt — by construction, since textParts was empty — so
		// retrying does not double-emit chunks.
		c.logInfof("opencode emitted no text on clean exit; retrying once")
		res, err = c.runOpencodeAttempt(ctx, binPath, args, env, workingDir, emitChunk)
	}
	if err != nil {
		return nil, err
	}
	textParts := res.textParts
	totalUsage := res.usage
	sessionID := res.sessionID
	lastFinishReason := res.lastFinishReason
	content := strings.TrimSpace(strings.Join(textParts, ""))

	stopReason := "stop"
	if lastFinishReason == "tool-calls" {
		stopReason = "tool_calls"
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:    content,
				StopReason: stopReason,
				GenerationInfo: &llmtypes.GenerationInfo{
					Additional: map[string]interface{}{
						"provider":            "opencode-cli",
						"opencode_mode":       "structured",
						"opencode_session_id": sessionID,
					},
				},
			},
		},
		Usage: &totalUsage,
	}, nil
}

// runOpencodeAttempt is one execution of `opencode run` with the args
// and env already assembled by generateContentStructured. It collects
// the JSON event stream and returns the parsed result.
//
// Returns errOpencodeSilentEmpty when opencode exited 0 but produced no
// text part — the caller may retry. All other terminal conditions
// (start failure, non-zero exit, fatal scanner error) return a normal
// wrapped error and must not be retried.
func (c *OpenCodeCLIAdapter) runOpencodeAttempt(ctx context.Context, binPath string, args []string, env []string, workingDir string, emitChunk func(llmtypes.StreamChunk)) (*opencodeAttemptResult, error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode start: %w", err)
	}

	res := &opencodeAttemptResult{}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	// scannerDone closes when the scanner loop returns — stdout has reached
	// EOF and the opencode process is gone. Used by procshutdown.Graceful
	// to observe end-of-life (structured shutdown contract §9). Writes to
	// `res` inside the loop are made visible to the main goroutine via the
	// close → receive happens-before.
	scannerDone := make(chan struct{})
	go func() {
		defer close(scannerDone)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}

			var event opencodeEvent
			if err := json.Unmarshal(line, &event); err != nil {
				c.logDebugf("opencode: failed to parse event: %v", err)
				continue
			}

			if res.sessionID == "" && event.SessionID != "" {
				res.sessionID = event.SessionID
			}

			switch event.Type {
			case "text":
				var part opencodeTextPart
				if err := json.Unmarshal(event.Part, &part); err == nil && part.Text != "" {
					res.textParts = append(res.textParts, part.Text)
					emitChunk(llmtypes.StreamChunk{
						Type:    llmtypes.StreamChunkTypeContent,
						Content: part.Text,
					})
				}

			case "tool_use":
				var part opencodeToolUsePart
				if err := json.Unmarshal(event.Part, &part); err == nil {
					inputStr := string(part.State.Input)
					emitChunk(llmtypes.StreamChunk{
						Type:       llmtypes.StreamChunkTypeToolCallStart,
						Content:    fmt.Sprintf("%s(%s)", part.Tool, inputStr),
						ToolName:   part.Tool,
						ToolCallID: part.CallID,
						ToolArgs:   inputStr,
					})
					emitChunk(llmtypes.StreamChunk{
						Type:       llmtypes.StreamChunkTypeToolCallEnd,
						Content:    part.State.Output,
						ToolName:   part.Tool,
						ToolCallID: part.CallID,
						ToolArgs:   inputStr,
						ToolResult: part.State.Output,
					})
				}

			case "step_finish":
				// End-of-turn teardown per the structured-CLI shutdown contract
				// (docs/coding_sdk_structured_contract.md §9): SIGTERM → 5s
				// grace for opencode session-state flush → SIGKILL.
				go procshutdown.Graceful(cmd, scannerDone, c.logger)
				var part opencodeStepFinishPart
				if err := json.Unmarshal(event.Part, &part); err == nil {
					res.usage.InputTokens += part.Tokens.Input
					res.usage.OutputTokens += part.Tokens.Output
					res.usage.TotalTokens += part.Tokens.Total
					if part.Tokens.Cache.Read > 0 {
						cacheRead := part.Tokens.Cache.Read
						res.usage.CacheTokens = &cacheRead
					}
					res.lastFinishReason = part.Reason
				}
			}
		}
	}()
	<-scannerDone

	waitErr := cmd.Wait()
	content := strings.TrimSpace(strings.Join(res.textParts, ""))

	if waitErr != nil && content == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("opencode run failed: %w: %s", waitErr, stderrStr)
		}
		return nil, fmt.Errorf("opencode run failed: %w", waitErr)
	}

	if content == "" {
		// Clean exit with no text — the retriable cold-start flake.
		// Surface stderr in the diagnostic so a second silent-empty
		// after retry still tells the operator what opencode said.
		if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
			return nil, fmt.Errorf("%w; stderr: %s", errOpencodeSilentEmpty, stderrStr)
		}
		return nil, errOpencodeSilentEmpty
	}

	return res, nil
}

// buildOpenCodeEnvForCall constructs the env passed to `opencode run`. It
// always carries the parent process environment plus, when set, the legacy
// shared `OPENCODE_API_KEY`. When the call is scoped to a sub-provider tile
// it also injects the matching per-sub-provider env var
// (KIMI_API_KEY / DEEPSEEK_API_KEY / DASHSCOPE_API_KEY / MINIMAX_API_KEY /
// ZHIPU_API_KEY) drawn from the call options' sub-provider key map.
//
// The function deliberately does NOT export every sub-provider's key on
// every call: doing so would let a misrouted request silently authenticate
// against the wrong provider. Instead, each call only carries the secret
// for the sub-provider that owns it.
func buildOpenCodeEnvForCall(apiKey string, activeSubProvider *OpenCodeSubProvider, opts *llmtypes.CallOptions) []string {
	env := os.Environ()
	if strings.TrimSpace(apiKey) != "" {
		env = append(env, "OPENCODE_API_KEY="+strings.TrimSpace(apiKey))
	}

	if activeSubProvider == nil || activeSubProvider.APIKeyEnvVar == "" || opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return env
	}
	keys, _ := opts.Metadata.Custom[MetadataKeySubProviderAPIKeys].(map[string]string)
	if keys == nil {
		return env
	}
	if v, ok := keys[activeSubProvider.APIKeyEnvVar]; ok && strings.TrimSpace(v) != "" {
		env = append(env, activeSubProvider.APIKeyEnvVar+"="+strings.TrimSpace(v))
	}
	return env
}
