package claudecode

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/nativeinput"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/sessionregistry"
)

//go:embed claude_sdk_bridge.cjs
var claudeSDKBridge []byte

// SetSDKTransport selects the official Agent SDK for this adapter instance.
func (c *ClaudeCodeInteractiveAdapter) SetSDKTransport(enabled bool) { c.sdkTransport = enabled }

type claudeSDKProcess struct {
	mu         sync.Mutex
	input      io.WriteCloser
	cancel     context.CancelFunc
	controlsMu sync.Mutex
	controls   map[string]chan error
	done       chan struct{}
}

var claudeSDKProcesses = sessionregistry.NewOwnerRegistry[*claudeSDKProcess]()

func (p *claudeSDKProcess) write(value any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return json.NewEncoder(p.input).Encode(value)
}

func claudeSDKEntry() (string, error) {
	if path := strings.TrimSpace(os.Getenv("CLAUDE_AGENT_SDK_PATH")); path != "" {
		path, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("Claude Agent SDK entry is unavailable: %w", err)
		}
		return path, nil
	}
	// No dependency downloads at request time. Operators install the official SDK
	// beside the other coding runtimes; the optional path supports local installs.
	out, err := exec.Command("npm", "root", "-g").Output()
	if err == nil {
		path := filepath.Join(strings.TrimSpace(string(out)), "@anthropic-ai", "claude-agent-sdk", "sdk.mjs")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Claude Agent SDK is not installed; install @anthropic-ai/claude-agent-sdk@0.3.311 with npm, or set CLAUDE_AGENT_SDK_PATH to its sdk.mjs")
}

func claudeSDKCommand(ctx context.Context, executable string, args []string) (*exec.Cmd, func(), error) {
	entry, err := claudeSDKEntry()
	if err != nil {
		return nil, func() {}, err
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, func() {}, fmt.Errorf("Claude Agent SDK requires Node.js: %w", err)
	}
	// A private folder per launch: the bridge runs outside Landlock, so neither
	// it nor the policy files it writes may sit where a confined CLI can write
	// (PLAT-872-51). Confined CLIs get their own TMPDIR, never this one.
	dir, err := os.MkdirTemp("", "claude-sdk-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	bridge := filepath.Join(dir, "bridge.cjs")
	if err = os.WriteFile(bridge, claudeSDKBridge, 0o600); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return exec.CommandContext(ctx, node, append([]string{bridge, entry, executable}, args...)...), cleanup, nil
}

func claudeSDKPermission(ctx context.Context, tool string, input json.RawMessage) map[string]any {
	deny := map[string]any{"behavior": "deny", "message": "This request was not approved. Use the chat or account setup to continue."}
	// Tool policy still runs natively (including the existing deny hooks). Only
	// native question/plan requests that reach canUseTool use attended cards.
	var value map[string]any
	if json.Unmarshal(input, &value) != nil {
		return deny
	}
	switch tool {
	case "AskUserQuestion":
		var request struct {
			Questions []struct {
				Question    string               `json:"question"`
				Options     []nativeinput.Option `json:"options"`
				MultiSelect bool                 `json:"multiSelect"`
			} `json:"questions"`
		}
		if json.Unmarshal(input, &request) != nil || len(request.Questions) == 0 {
			return deny
		}
		questions := make([]nativeinput.Question, 0, len(request.Questions))
		for i, q := range request.Questions {
			questions = append(questions, nativeinput.Question{ID: fmt.Sprint(i), Question: q.Question, Options: q.Options, MultiSelect: q.MultiSelect, AllowOther: true})
		}
		answers, err := nativeinput.Ask(ctx, questions)
		if err != nil {
			return deny
		}
		mapped := map[string]string{}
		for i, a := range answers {
			mapped[request.Questions[i].Question] = strings.Join(nativeinput.Values(a), ", ")
		}
		value["answers"] = mapped
	case "ExitPlanMode":
		plan, _ := value["plan"].(string)
		answers, err := nativeinput.Ask(ctx, []nativeinput.Question{{ID: "plan", Question: "Approve this plan?\n" + plan, Options: []nativeinput.Option{{Label: "Approve"}, {Label: "Reject"}}}})
		if err != nil || len(answers) != 1 || len(answers[0].SelectedLabels) != 1 || answers[0].SelectedLabels[0] != "Approve" {
			return deny
		}
	default:
		return deny
	}
	return map[string]any{"behavior": "allow", "updatedInput": value}
}

func (p *claudeSDKProcess) interrupt(ctx context.Context) error {
	return p.control(ctx, "interrupt", "")
}

func (p *claudeSDKProcess) steer(ctx context.Context, message string) error {
	return p.control(ctx, "steer", message)
}

func (p *claudeSDKProcess) control(ctx context.Context, kind, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := randomHex(12)
	reply := make(chan error, 1)
	p.controlsMu.Lock()
	if p.controls == nil {
		p.controls = map[string]chan error{}
	}
	p.controls[id] = reply
	p.controlsMu.Unlock()
	defer func() { p.controlsMu.Lock(); delete(p.controls, id); p.controlsMu.Unlock() }()
	if err := p.write(map[string]any{"type": kind, "id": id, "text": message}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return fmt.Errorf("Claude SDK turn ended before native input/control acknowledgement")
	}
}
func (p *claudeSDKProcess) acknowledgeControl(id string, failed, notSent bool) {
	p.controlsMu.Lock()
	reply := p.controls[id]
	p.controlsMu.Unlock()
	if reply != nil {
		var err error
		if notSent {
			err = fmt.Errorf("%w: Claude SDK turn is no longer accepting input", llmtypes.ErrCodingAgentInputNotSent)
		} else if failed {
			err = fmt.Errorf("Claude SDK rejected control request")
		}
		select {
		case reply <- err:
		default:
		}
	}
}

// SendClaudeCodeSDKInput never falls back to a tmux session from another transport.
func SendClaudeCodeSDKInput(ctx context.Context, owner, message string) error {
	process, ok := claudeSDKProcesses.Get(strings.TrimSpace(owner))
	if !ok {
		return fmt.Errorf("%w: no active Claude SDK turn", llmtypes.ErrCodingAgentInputNotSent)
	}
	return process.steer(ctx, message)
}
