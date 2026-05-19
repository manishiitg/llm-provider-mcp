package opencodecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

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

func (c *OpenCodeCLIAdapter) generateContentStructured(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
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

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = buildOpenCodeEnvForCall(c.apiKey, activeSubProvider, opts)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	c.logInfof("Executing OpenCode CLI structured: opencode %s", strings.Join(args[:3], " "))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode start: %w", err)
	}

	var textParts []string
	var totalUsage llmtypes.Usage
	var sessionID string
	var lastFinishReason string

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

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

		if sessionID == "" && event.SessionID != "" {
			sessionID = event.SessionID
		}

		switch event.Type {
		case "text":
			var part opencodeTextPart
			if err := json.Unmarshal(event.Part, &part); err == nil && part.Text != "" {
				textParts = append(textParts, part.Text)
				if opts.StreamChan != nil {
					select {
					case opts.StreamChan <- llmtypes.StreamChunk{
						Type:    llmtypes.StreamChunkTypeContent,
						Content: part.Text,
					}:
					default:
					}
				}
			}

		case "tool_use":
			var part opencodeToolUsePart
			if err := json.Unmarshal(event.Part, &part); err == nil && opts.StreamChan != nil {
				inputStr := string(part.State.Input)
				select {
				case opts.StreamChan <- llmtypes.StreamChunk{
					Type:    llmtypes.StreamChunkTypeToolCallStart,
					Content: fmt.Sprintf("%s(%s)", part.Tool, inputStr),
				}:
				default:
				}
				select {
				case opts.StreamChan <- llmtypes.StreamChunk{
					Type:    llmtypes.StreamChunkTypeToolCallEnd,
					Content: part.State.Output,
				}:
				default:
				}
			}

		case "step_finish":
			var part opencodeStepFinishPart
			if err := json.Unmarshal(event.Part, &part); err == nil {
				totalUsage.InputTokens += part.Tokens.Input
				totalUsage.OutputTokens += part.Tokens.Output
				totalUsage.TotalTokens += part.Tokens.Total
				if part.Tokens.Cache.Read > 0 {
					cacheRead := part.Tokens.Cache.Read
					totalUsage.CacheTokens = &cacheRead
				}
				lastFinishReason = part.Reason
			}
		}
	}

	waitErr := cmd.Wait()

	content := strings.TrimSpace(strings.Join(textParts, ""))

	if waitErr != nil && content == "" {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("opencode run failed: %w: %s", waitErr, stderrStr)
		}
		return nil, fmt.Errorf("opencode run failed: %w", waitErr)
	}

	if content == "" {
		return nil, fmt.Errorf("opencode run returned no text output")
	}

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
