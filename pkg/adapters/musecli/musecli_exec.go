package musecli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// EnvMuseCLIExecProvider overrides the muse startup provider for one exec
// lane run (default "meta"). Tests set it to "echo": deterministic, no
// auth, no API spend.
const EnvMuseCLIExecProvider = "MUSE_CLI_EXEC_PROVIDER"

const defaultMuseExecProvider = "meta"

// museWireEvent is one JSONL line from `muse exec --json` (MSP wire schema,
// `muse schema`). Shapes verified live against muse 1.1.1.
type museWireEvent struct {
	PayloadType string          `json:"payload_type"`
	Stream      museWireStream  `json:"stream"`
	CausationID string          `json:"causation_id"`
	Payload     museWirePayload `json:"payload"`
}

type museWireStream struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type museWirePayload struct {
	Kind      string `json:"kind"`
	CommandID string `json:"command_id"`
	Text      string `json:"text"`
	Terminal  string `json:"terminal"`
	Reason    string `json:"reason"`
}

func (a *MuseCLIAdapter) museExecProvider() string {
	if p := strings.TrimSpace(os.Getenv(EnvMuseCLIExecProvider)); p != "" {
		return p
	}
	return defaultMuseExecProvider
}

// museBuildExecPrompt folds messages into one exec prompt: system texts
// become a header (exec has no system-prompt flag), the last human text is
// the prompt. Non-text parts are rejected: pass image paths as text.
func museBuildExecPrompt(messages []llmtypes.MessageContent) (string, error) {
	var system []string
	human := ""
	for _, m := range messages {
		var parts []string
		for _, part := range m.Parts {
			switch c := part.(type) {
			case llmtypes.TextContent:
				parts = append(parts, c.Text)
			case *llmtypes.TextContent:
				if c != nil {
					parts = append(parts, c.Text)
				}
			default:
				return "", fmt.Errorf("muse-cli exec lane supports text only; got %T (pass image paths as text)", part)
			}
		}
		text := strings.Join(parts, "")
		switch m.Role {
		case llmtypes.ChatMessageTypeSystem:
			if strings.TrimSpace(text) != "" {
				system = append(system, text)
			}
		case llmtypes.ChatMessageTypeHuman:
			human = text
		}
	}
	if strings.TrimSpace(human) == "" {
		return "", fmt.Errorf("muse-cli exec lane needs a human prompt, got none")
	}
	if len(system) > 0 {
		return strings.Join(system, "\n\n") + "\n\n" + human, nil
	}
	return human, nil
}

func museStderrTail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2048 {
		s = s[len(s)-2048:]
	}
	return s
}

// generateContentExec runs one headless turn via `muse exec --json` and
// returns the terminal text (falling back to joined deltas). run.output.delta
// chunks are forwarded to opts.StreamChan when set; the channel stays
// caller-owned and is never closed here. Usage is nil: token counts come
// from the session sidecar (transcript reader, later step), not the wire.
func (a *MuseCLIAdapter) generateContentExec(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}
	prompt, err := museBuildExecPrompt(messages)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(a.modelID)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	provider := a.museExecProvider()
	argv := []string{"exec", "--json", "--provider", provider, "--trust-workspace"}
	if a.apiKey != "" {
		argv = append(argv, "--api-key-stdin")
	}
	// --model only for concrete non-placeholder models; the echo test
	// provider takes no model flag.
	if model != "" && model != DefaultModelID && provider != "echo" {
		argv = append(argv, "--model", model)
	}
	if resumeID := strings.TrimSpace(museResumeSessionIDFromOptions(opts)); resumeID != "" {
		argv = append(argv, "--session-id", resumeID)
	}
	// MCP servers reach muse through the user-level settings.json (there is
	// no --mcp-config flag). Merge for the duration of this run only.
	if mcpJSON := strings.TrimSpace(museMCPConfigFromOptions(opts)); mcpJSON != "" {
		restoreMCP, err := museApplyMCPConfig(mcpJSON)
		if err != nil {
			return nil, err
		}
		if restoreMCP != nil {
			defer restoreMCP()
		}
	}
	argv = append(argv, prompt)

	cmd := exec.CommandContext(ctx, "muse", argv...)
	if a.apiKey != "" {
		cmd.Stdin = strings.NewReader(a.apiKey)
	} else {
		cmd.Stdin = strings.NewReader("")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("muse exec stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if _, lookErr := exec.LookPath("muse"); lookErr != nil {
			return nil, fmt.Errorf("muse CLI not in PATH: %w", lookErr)
		}
		return nil, fmt.Errorf("start muse exec: %w", err)
	}
	var deltas strings.Builder
	terminalText := ""
	failedReason := ""
	sessionID := ""
	commandID := ""
	unparsed := 0
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev museWireEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			unparsed++
			continue
		}
		if sessionID == "" && ev.Stream.ID != "" {
			sessionID = ev.Stream.ID
		}
		if commandID == "" {
			if ev.Payload.CommandID != "" {
				commandID = ev.Payload.CommandID
			} else if ev.CausationID != "" {
				commandID = ev.CausationID
			}
		}
		switch ev.PayloadType {
		case "run.output.delta":
			deltas.WriteString(ev.Payload.Text)
			if opts.StreamChan != nil {
				opts.StreamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: ev.Payload.Text}
			}
		case "run.terminal.completed":
			if ev.Payload.Text != "" {
				terminalText = ev.Payload.Text
			}
		case "run.terminal.failed":
			if ev.Payload.Reason != "" {
				failedReason = ev.Payload.Reason
			} else if ev.Payload.Text != "" {
				failedReason = ev.Payload.Text
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return nil, fmt.Errorf("read muse exec stream: %w (stderr: %s)", err, museStderrTail(stderr.String()))
	}
	if err := cmd.Wait(); err != nil {
		if failedReason != "" {
			return nil, fmt.Errorf("muse exec failed: %s: %w (stderr: %s)", failedReason, err, museStderrTail(stderr.String()))
		}
		return nil, fmt.Errorf("muse exec failed: %w (stderr: %s)", err, museStderrTail(stderr.String()))
	}
	final := terminalText
	if strings.TrimSpace(final) == "" {
		final = deltas.String()
	}
	if strings.TrimSpace(final) == "" {
		return nil, fmt.Errorf("muse exec returned no text (session %s, %d unparsed lines, stderr: %s)", sessionID, unparsed, museStderrTail(stderr.String()))
	}
	turnUsage, hasTurnUsage := museUsageForTurn(sessionID, commandID)
	gi := &llmtypes.GenerationInfo{}
	llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
		Provider:        "muse-cli",
		Transport:       llmtypes.CodingProviderTransportStructured,
		NativeSessionID: sessionID,
		Model:           model,
	})
	resp := &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{
		Content:        final,
		StopReason:     "completed",
		GenerationInfo: gi,
	}}}
	if hasTurnUsage {
		resp.Usage = &turnUsage
	}
	return resp, nil
}
