package opencodecli

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
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("opencode-cli prompt is empty")
	}

	if strings.TrimSpace(systemPrompt) != "" {
		prompt = "[System Instructions]\n" + systemPrompt + "\n\n[User Message]\n" + prompt
	}

	args := []string{"run", "--format", "json", "--dangerously-skip-permissions"}

	workingDir := opencodeWorkingDirFromOptions(opts)
	if workingDir != "" {
		args = append(args, "--dir", workingDir)
	}

	modelToUse := resolveOpenCodeCLIModelID(c.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyOpenCodeModel].(string); ok && strings.TrimSpace(model) != "" {
			modelToUse = resolveOpenCodeCLIModelID(model)
		}
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
	cmd.Env = buildOpenCodeEnv(c.apiKey)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("opencode stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	c.logInfof("Executing OpenCode CLI structured: opencode %s", strings.Join(args[:3], " "))
	if err := cmd.Start(); err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
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

	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

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

func buildOpenCodeEnv(apiKey string) []string {
	env := os.Environ()
	if strings.TrimSpace(apiKey) != "" {
		env = append(env, "OPENCODE_API_KEY="+strings.TrimSpace(apiKey))
	}
	return env
}
