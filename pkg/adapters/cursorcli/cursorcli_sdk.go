package cursorcli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
)

// The Cursor SDK transport (CURSOR_CLI_TRANSPORT=sdk) runs the official @cursor/sdk local agent in a
// node bridge, one per chat, kept between turns. Unlike ACP it steers a running turn: run.steer adds
// the message at the next safe step (owner decision 2026-10-27, PLAT-872).

//go:embed cursor_sdk_bridge.cjs
var cursorSDKBridge []byte

const cursorSDKMarker = "\x01aw "

type cursorSDKEvent struct {
	Type     string          `json:"type"`
	AgentID  string          `json:"agent_id"`
	Resumed  bool            `json:"resumed"`
	Kind     string          `json:"kind"`
	Text     string          `json:"text"`
	CallID   string          `json:"call_id"`
	Name     string          `json:"name"`
	Status   string          `json:"status"`
	Args     json.RawMessage `json:"args"`
	Result   json.RawMessage `json:"result"`
	Usage    *cursorSDKUsage `json:"usage"`
	ID       string          `json:"id"`
	Accepted bool            `json:"accepted"`
	Error    string          `json:"error"`
	// Stale is how the bridge cleared a run an earlier bridge left active: cancelled or new_agent.
	Stale      string `json:"stale"`
	StaleAgent string `json:"stale_agent"`
	Action     string `json:"action"`
}

type cursorSDKUsage struct {
	InputTokens     int `json:"inputTokens"`
	OutputTokens    int `json:"outputTokens"`
	CacheReadTokens int `json:"cacheReadTokens"`
	TotalTokens     int `json:"totalTokens"`
}

type cursorSDKProcess struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	events  chan cursorSDKEvent
	done    chan struct{}
	exited  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	err     error
	acks    map[string]chan bool
	stop    context.CancelFunc
}

type cursorSDKSession struct {
	turnMu      sync.Mutex
	mu          sync.Mutex
	proc        *cursorSDKProcess
	agentID     string
	cwd, model  string
	fingerprint [32]byte
	idle        *time.Timer
	hasHistory  bool
	running     bool
	prompted    bool // the bridge has this turn's prompt: a steer handed over now cannot be lost to a failed launch
}

var cursorSDKSessions = struct {
	sync.Mutex
	sessions map[string]*cursorSDKSession
}{sessions: map[string]*cursorSDKSession{}}

func (p *cursorSDKProcess) send(v any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return json.NewEncoder(p.stdin).Encode(v)
}

func (p *cursorSDKProcess) finish(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
		p.stop()
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = p.stdin.Close()
	})
}

func (p *cursorSDKProcess) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	return fmt.Errorf("Cursor SDK bridge closed")
}

// close lets the bridge cancel its in-flight run before it is killed: a SIGKILL first left the run
// active in Cursor's agent store, and every later turn of that chat failed "already has active run".
func (p *cursorSDKProcess) close() {
	_ = p.stdin.Close()
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
	}
	p.finish(fmt.Errorf("Cursor SDK bridge closed"))
	<-p.exited
}

// read keeps only marked protocol lines: the SDK logs to the same stdout, and that text is never kept.
func (p *cursorSDKProcess) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line, ok := bytes.CutPrefix(scanner.Bytes(), []byte(cursorSDKMarker))
		if !ok {
			continue
		}
		var event cursorSDKEvent
		if err := json.Unmarshal(line, &event); err != nil {
			p.finish(fmt.Errorf("invalid Cursor SDK bridge event: %w", err))
			return
		}
		if event.Type == "steer_accepted" {
			p.mu.Lock()
			ack := p.acks[event.ID]
			delete(p.acks, event.ID)
			p.mu.Unlock()
			if ack != nil {
				ack <- event.Accepted
			}
			continue
		}
		select {
		case p.events <- event:
		case <-p.done:
			return
		}
	}
	if err := scanner.Err(); err != nil {
		p.finish(fmt.Errorf("read Cursor SDK bridge: %w", err))
	}
}

// cursorSDKEntry checks the explicit path, managed CLI prefix, then global npm root.
func cursorSDKEntry() (string, error) {
	if path := strings.TrimSpace(os.Getenv("CURSOR_SDK_PATH")); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", errors.New("Cursor needs a component that is unavailable. Update the app and try again")
		}
		return filepath.Abs(path)
	}
	if binary, err := exec.LookPath("cursor-agent"); err == nil {
		path := filepath.Join(filepath.Dir(binary), "..", "lib", "node_modules", "@cursor", "sdk", "dist", "cjs", "index.js")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "npm", "root", "-g").Output(); err == nil {
		path := filepath.Join(strings.TrimSpace(string(out)), "@cursor", "sdk", "dist", "cjs", "index.js")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", errors.New("Cursor needs a component that isn't installed. Update the app and try again")
}

// cursorSDKModel turns a Cursor selector ("claude-opus-4-8[effort=high,fast=false]") into the SDK's
// {id, params} form.
func cursorSDKModel(selector string) map[string]any {
	base, parameters, ok := strings.Cut(strings.TrimSpace(selector), "[")
	model := map[string]any{"id": base}
	if !ok {
		return model
	}
	params := []map[string]string{}
	for _, setting := range strings.Split(strings.TrimSuffix(parameters, "]"), ",") {
		if key, value, found := strings.Cut(strings.TrimSpace(setting), "="); found && key != "" {
			params = append(params, map[string]string{"id": key, "value": value})
		}
	}
	if len(params) > 0 {
		model["params"] = params
	}
	return model
}

func (c *CursorCLIAdapter) launchSDK(ctx context.Context, opts *llmtypes.CallOptions, cwd, owner, system, model, resume string) (*cursorSDKProcess, string, bool, error) {
	if err := llmtypes.PrepareCodingSDK(ctx, "cursor-cli", opts.StreamChan); err != nil {
		return nil, "", false, err
	}
	// Rules, MCP and skills go where ACP puts them: the private runtime for a Crew bound to a person's own
	// folder (PLAT-856-145), never .cursor/ inside that folder.
	artifacts := llmtypes.CLIArtifactDirectory(opts, cwd)
	cleanup, err := prepareCursorProjectFiles(artifacts, system, opts, owner)
	if err != nil {
		return nil, "", false, err
	}
	if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
		if err = c.ProjectSkills(artifacts, skills); err != nil {
			cleanup()
			return nil, "", false, err
		}
	}
	entry, err := cursorSDKEntry()
	if err != nil {
		cleanup()
		return nil, "", false, err
	}
	node, err := exec.LookPath("node")
	if err != nil {
		cleanup()
		return nil, "", false, fmt.Errorf("Cursor SDK requires Node.js: %w", err)
	}
	dir, err := os.MkdirTemp("", "cursor-sdk-*")
	if err != nil {
		cleanup()
		return nil, "", false, err
	}
	cleanup = func(inner func()) func() { return func() { inner(); _ = os.RemoveAll(dir) } }(cleanup)
	bridge := filepath.Join(dir, "bridge.cjs")
	if err = os.WriteFile(bridge, cursorSDKBridge, 0o600); err != nil {
		cleanup()
		return nil, "", false, err
	}
	processCtx, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(processCtx, node, bridge, entry)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	procshutdown.KillGroupOnCancel(cmd)
	cmd.Env = append(llmtypes.MergeCodingAgentSecretEnvironment(buildCursorStructuredEnv(c.apiKey), opts), "NO_OPEN_BROWSER=1")
	if llmtypes.CLIHasPrivateRuntime(opts) {
		configDir, configErr := prepareCursorPrivateRuntime(opts, cmd.Env)
		if configErr != nil {
			stop()
			cleanup()
			return nil, "", false, configErr
		}
		cmd.Env = append(cmd.Env, "CURSOR_CONFIG_DIR="+configDir)
	}
	// The agent loop runs in node itself, so node is the confined process (no spawned CLI child).
	sdkPackages := cursorSDKReadRoot(entry)
	reads := append(cursorLandlockReads(opts, nil, cwd), bridge, sdkPackages, artifacts)
	sandboxCleanup, err := clisandbox.LandlockCmd(opts.CLISecurity, cmd, cwd, reads, nil)
	if err != nil {
		stop()
		cleanup()
		return nil, "", false, err
	}
	allCleanup := func() { sandboxCleanup(); cleanup() }
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stop()
		allCleanup()
		return nil, "", false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		stdin.Close()
		allCleanup()
		return nil, "", false, err
	}
	p := &cursorSDKProcess{cmd: cmd, stdin: stdin, events: make(chan cursorSDKEvent, 4096), done: make(chan struct{}), exited: make(chan struct{}), acks: map[string]chan bool{}, stop: stop}
	if err = cmd.Start(); err != nil {
		stop()
		stdin.Close()
		stdout.Close()
		allCleanup()
		return nil, "", false, err
	}
	go p.read(stdout)
	go func() {
		err := cmd.Wait()
		if err == nil {
			err = fmt.Errorf("Cursor SDK bridge exited")
		}
		p.finish(err)
		allCleanup()
		close(p.exited)
	}()
	start := map[string]any{"type": "start", "cwd": cwd, "model": cursorSDKModel(model)}
	if artifacts != cwd {
		start["dirs"] = []string{artifacts}
		// The SDK documents rules and skills from local.dirs, not MCP: pass the prepared servers (with the
		// AgentWorks bridge) explicitly so a bound-folder chat never loses its tools.
		if raw, readErr := os.ReadFile(filepath.Join(artifacts, ".cursor", "mcp.json")); readErr == nil {
			var mcp struct {
				Servers map[string]any `json:"mcpServers"`
			}
			if json.Unmarshal(raw, &mcp) == nil && len(mcp.Servers) > 0 {
				start["mcp_servers"] = mcp.Servers
			}
		}
	}
	// Only an SDK agent handle can be resumed; an ACP session ID starts a new agent with saved history.
	if strings.HasPrefix(resume, "agent-") {
		start["agent_id"] = resume
	}
	if mode := cursorACPString(opts, MetadataKeyMode); mode == "plan" {
		start["mode"] = mode
	}
	if cursorACPBool(opts, MetadataKeyDenyBuiltinTools) && !cursorFullNativeFromOptions(opts) {
		start["tools"] = []string{"mcp"}
	}
	if err = p.send(start); err != nil {
		p.close()
		return nil, "", false, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	select {
	case event := <-p.events:
		if event.Type != "ready" || event.AgentID == "" {
			p.close()
			return nil, "", false, fmt.Errorf("Cursor SDK did not start: %s", event.Error)
		}
		if event.Stale != "" && c.logger != nil {
			c.logger.Infof("[CURSOR_SDK] stale_active_run agent=%s action=%s new=%s", event.StaleAgent, event.Stale, event.AgentID)
		}
		return p, event.AgentID, event.Resumed, nil
	case <-p.done:
		return nil, "", false, p.failure()
	case <-readyCtx.Done():
		p.close()
		return nil, "", false, readyCtx.Err()
	}
}

func closeCursorSDKInstance(owner string, expected *cursorSDKSession) {
	cursorSDKSessions.Lock()
	s := cursorSDKSessions.sessions[owner]
	if expected != nil && s != expected {
		cursorSDKSessions.Unlock()
		return
	}
	delete(cursorSDKSessions.sessions, owner)
	cursorSDKSessions.Unlock()
	if s != nil {
		s.mu.Lock()
		p := s.proc
		if s.idle != nil {
			s.idle.Stop()
		}
		s.mu.Unlock()
		if p != nil {
			p.close()
		}
	}
}

func (c *CursorCLIAdapter) generateContentSDK(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (response *llmtypes.ContentResponse, err error) {
	if opts.StreamChan != nil {
		defer close(opts.StreamChan)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	emit := func(chunk llmtypes.StreamChunk) {
		if opts.StreamChan != nil {
			select {
			case opts.StreamChan <- chunk:
			case <-ctx.Done():
			}
		}
	}
	cwd := cursorWorkingDirFromOptions(opts)
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	if cwd, err = filepath.Abs(cwd); err != nil {
		return nil, err
	}
	owner := cursorInteractiveSessionIDFromOptions(opts)
	if owner == "" {
		owner = fmt.Sprintf("cursor-sdk-%d", time.Now().UnixNano())
	}
	ctx = llmtypes.ScopeCodingAgentQuestions(ctx, "cursor-cli", owner)
	system, conversation := splitCursorSystemPrompt(messages)
	if system == "" {
		system = llmtypes.CodingProviderLaunchSystemPromptFromOptions(opts)
	}
	model := cursorModelWithReasoning(cursorCLIModelForLaunch(c.modelID), opts)
	if override := cursorACPString(opts, MetadataKeyCursorModel); override != "" {
		model = cursorModelWithReasoning(override, opts)
	}
	config := map[string]any{"cwd": cwd, "runtime": opts.CLIRuntimeDir, "system": system, "model": model, "env": llmtypes.MergeCodingAgentSecretEnvironment(buildCursorStructuredEnv(c.apiKey), opts), "security": opts.CLISecurity, "skills": llmtypes.AttachedSkillsFromOptions(opts)}
	if opts.Metadata != nil {
		for k, v := range opts.Metadata.Custom {
			if k != MetadataKeyResumeSessionID && k != llmtypes.CodingProviderLaunchOnlyMetadataKey {
				config[k] = v
			}
		}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(raw)
	cursorSDKSessions.Lock()
	s := cursorSDKSessions.sessions[owner]
	if s == nil {
		s = &cursorSDKSession{}
		cursorSDKSessions.sessions[owner] = s
	}
	cursorSDKSessions.Unlock()
	if !s.turnMu.TryLock() {
		return nil, fmt.Errorf("Cursor is already working; queue this message")
	}
	defer s.turnMu.Unlock()
	// The turn takes steering from its first moment, including while the bridge starts and before the
	// prompt is sent: a message 2s in otherwise fell back to an after-turn injection (RTS 2026-10-27).
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running, s.prompted = false, false; s.mu.Unlock() }()
	s.mu.Lock()
	if s.idle != nil {
		s.idle.Stop()
	}
	p, agentID, old, hasHistory := s.proc, s.agentID, s.fingerprint, s.hasHistory
	s.mu.Unlock()
	defer func() {
		if !cursorPersistentInteractiveFromOptions(opts) || err != nil {
			closeCursorSDKInstance(owner, s)
			return
		}
		s.mu.Lock()
		s.idle = time.AfterFunc(cursorInteractiveIdleTimeout(), func() {
			if s.turnMu.TryLock() {
				defer s.turnMu.Unlock()
				closeCursorSDKInstance(owner, s)
			}
		})
		s.mu.Unlock()
	}()
	resume := cursorResumeSessionIDFromOptions(opts)
	if p != nil {
		select {
		case <-p.done:
			p.close()
			p = nil
		default:
		}
	}
	if p != nil && (old != fingerprint || (resume != "" && resume != agentID)) {
		p.close()
		p = nil
	}
	if p == nil {
		if old != ([32]byte{}) && old != fingerprint {
			agentID = ""
		}
		if resume != "" {
			agentID = resume
		}
		requested := agentID
		var resumed bool
		if p, agentID, resumed, err = c.launchSDK(ctx, opts, cwd, owner, system, model, agentID); err != nil {
			return nil, err
		}
		hasHistory = resumed
		s.mu.Lock()
		s.proc, s.agentID, s.cwd, s.model, s.fingerprint, s.hasHistory = p, agentID, cwd, model, fingerprint, hasHistory
		s.mu.Unlock()
		if requested != "" && !resumed && c.logger != nil {
			c.logger.Infof("[CURSOR_SDK] resume_missing fallback=new chat=%q requested=%q new=%q", owner, requested, agentID)
		}
		if opts.OnCodingProviderSession != nil {
			opts.OnCodingProviderSession(cursorSDKSessionHandle(s))
		}
	}
	if llmtypes.CodingProviderLaunchOnlyFromOptions(opts) {
		return cursorSDKResponse(s, "", nil), nil
	}
	resumePrompt := ""
	if hasHistory {
		resumePrompt = agentID
	} else if len(opts.CodingProviderHistory) > 0 {
		_, conversation = splitCursorSystemPrompt(opts.CodingProviderHistory)
	}
	prompt := buildCursorStructuredPrompt("", conversation, resumePrompt)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("Cursor prompt is empty")
	}
	response, err = c.consumeSDKTurn(ctx, s, p, prompt, emit)
	if err == nil {
		s.mu.Lock()
		s.hasHistory = true
		s.mu.Unlock()
	}
	return response, err
}

func (c *CursorCLIAdapter) consumeSDKTurn(ctx context.Context, s *cursorSDKSession, p *cursorSDKProcess, prompt string, emit func(llmtypes.StreamChunk)) (*llmtypes.ContentResponse, error) {
	meta := map[string]any{"cursor_session_id": s.agentID, "cursor_stream_source": "sdk"}
	if err := p.send(map[string]any{"type": "prompt", "text": prompt}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.prompted = true
	s.mu.Unlock()
	emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypePromptAccepted, Metadata: meta})
	var text strings.Builder
	afterTool := false
	type tool struct {
		name, args string
		started    time.Time
		ended      bool
	}
	tools := map[string]*tool{}
	for {
		select {
		case event := <-p.events:
			switch event.Type {
			case "delta":
				kind := llmtypes.StreamChunkTypeContent
				if event.Kind == "thinking" {
					kind = llmtypes.StreamChunkTypeReasoning
				} else {
					// Text written after a tool step is a new paragraph, not glued to the last sentence.
					if afterTool && text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
						event.Text = "\n\n" + event.Text
					}
					afterTool = false
					text.WriteString(event.Text)
				}
				emit(llmtypes.StreamChunk{Type: kind, Content: event.Text, Metadata: map[string]any{"cursor_session_id": s.agentID, "cursor_stream_source": "sdk", llmtypes.ContentDeltaMetadataKey: true}})
			case "tool":
				afterTool = true
				t := tools[event.CallID]
				if t == nil {
					args := string(event.Args)
					if args == "" || args == "null" {
						args = "{}"
					}
					t = &tool{name: event.Name, args: args, started: time.Now()}
					tools[event.CallID] = t
					emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallStart, ToolCallID: event.CallID, ToolName: t.name, ToolArgs: t.args, Metadata: meta})
				}
				if !t.ended && (event.Status == "completed" || event.Status == "error") {
					t.ended = true
					result := string(event.Result)
					var plain string
					if json.Unmarshal(event.Result, &plain) == nil {
						result = plain
					}
					emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallEnd, ToolCallID: event.CallID, ToolName: t.name, ToolArgs: t.args, ToolResult: result, ToolDuration: time.Since(t.started), Metadata: meta})
				}
			case "stale":
				if c.logger != nil {
					c.logger.Infof("[CURSOR_SDK] stale_active_run agent=%s action=%s", event.AgentID, event.Action)
				}
			case "steer_outcome":
				// Whether the running run took a mid-turn message ("complete_delivered") or the bridge answers
				// it as a follow-up in this turn (any other outcome).
				if c.logger != nil {
					c.logger.Infof("[CURSOR_SDK] steer_outcome agent=%s steer=%s outcome=%s", s.agentID, event.ID, event.Status)
				}
			case "result":
				// A message sent after this point starts the next turn rather than joining this one.
				s.mu.Lock()
				s.running, s.prompted = false, false
				s.mu.Unlock()
				switch event.Status {
				case "finished":
				case "cancelled":
					return nil, fmt.Errorf("Cursor turn cancelled")
				default:
					return nil, fmt.Errorf("Cursor SDK turn failed: %s", event.Error)
				}
				if len(tools) == 0 {
					if err := cursorACPPlanFailure(text.String(), s.model); err != nil {
						return nil, err
					}
				}
				// The streamed text spans every run of this turn: a steer the SDK did not take is answered
				// by a follow-up run, and its result alone would drop the first run's answer.
				final := text.String()
				if strings.TrimSpace(final) == "" {
					_ = json.Unmarshal(event.Result, &final)
				}
				if strings.TrimSpace(final) == "" && len(tools) == 0 {
					return nil, fmt.Errorf("Cursor returned no answer")
				}
				var usage *llmtypes.Usage
				if u := event.Usage; u != nil {
					cached := u.CacheReadTokens
					usage = &llmtypes.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens, CacheTokens: &cached}
				}
				return cursorSDKResponse(s, final, usage), nil
			}
		case <-ctx.Done():
			_ = p.send(map[string]any{"type": "cancel"})
			p.close()
			return nil, ctx.Err()
		case <-p.done:
			return nil, p.failure()
		}
	}
}

// SendCursorSDKInput steers the running turn. Delivery is the bridge's receipt: the SDK adds the
// message at its next safe step, or the bridge answers it right after the run in the same turn.
func SendCursorSDKInput(ctx context.Context, owner, message string) error {
	cursorSDKSessions.Lock()
	s := cursorSDKSessions.sessions[strings.TrimSpace(owner)]
	cursorSDKSessions.Unlock()
	if s == nil {
		return fmt.Errorf("%w: no active Cursor SDK turn", llmtypes.ErrCodingAgentInputNotSent)
	}
	// While the turn is still starting, wait until the bridge holds its prompt. If the launch fails first,
	// the turn stops running and the message is reported not sent, so the host keeps it (never a lost ack).
	var p *cursorSDKProcess
	for {
		s.mu.Lock()
		proc, running, prompted := s.proc, s.running, s.prompted
		s.mu.Unlock()
		if !running {
			return fmt.Errorf("%w: no active Cursor SDK turn", llmtypes.ErrCodingAgentInputNotSent)
		}
		if proc != nil && prompted {
			p = proc
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	token := make([]byte, 8)
	_, _ = rand.Read(token)
	id := hex.EncodeToString(token)
	ack := make(chan bool, 1)
	p.mu.Lock()
	p.acks[id] = ack
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.acks, id); p.mu.Unlock() }()
	if err := p.send(map[string]any{"type": "steer", "id": id, "text": message}); err != nil {
		return fmt.Errorf("%w: %v", llmtypes.ErrCodingAgentInputNotSent, err)
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return fmt.Errorf("%w: the Cursor turn had finished", llmtypes.ErrCodingAgentInputNotSent)
		}
		return nil
	case <-p.done:
		return errors.Join(llmtypes.ErrCodingAgentInputNotSent, p.failure())
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cursorSDKSessionHandle(s *cursorSDKSession) llmtypes.CodingProviderSessionHandle {
	return llmtypes.CodingProviderSessionHandle{Provider: "cursor-cli", Transport: "sdk", NativeSessionID: s.agentID, WorkingDir: s.cwd, Model: s.model, Status: llmtypes.CodingProviderSessionStatusIdle}
}

func cursorSDKResponse(s *cursorSDKSession, text string, usage *llmtypes.Usage) *llmtypes.ContentResponse {
	gi := &llmtypes.GenerationInfo{Additional: map[string]any{"cursor_session_id": s.agentID}}
	llmtypes.AttachCodingProviderSessionHandle(gi, cursorSDKSessionHandle(s))
	return &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{Content: text, StopReason: "stop", GenerationInfo: gi}}, Usage: usage}
}
