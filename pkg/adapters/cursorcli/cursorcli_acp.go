package cursorcli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type cursorACPSession struct {
	turnMu             sync.Mutex
	mu                 sync.Mutex
	rpc                *cursorACP
	native, cwd, model string
	fingerprint        [32]byte
	idle               *time.Timer
	status             *llmtypes.StatusLine
	hasHistory         bool
}

var cursorACPSessions = struct {
	sync.Mutex
	sessions map[string]*cursorACPSession
}{sessions: map[string]*cursorACPSession{}}

func closeCursorACP(owner string) { closeCursorACPInstance(owner, nil) }
func closeCursorACPInstance(owner string, expected *cursorACPSession) {
	cursorACPSessions.Lock()
	s := cursorACPSessions.sessions[owner]
	if expected != nil && s != expected {
		cursorACPSessions.Unlock()
		return
	}
	delete(cursorACPSessions.sessions, owner)
	cursorACPSessions.Unlock()
	if s != nil {
		s.mu.Lock()
		r := s.rpc
		if s.idle != nil {
			s.idle.Stop()
		}
		s.mu.Unlock()
		if r != nil {
			r.close()
		}
	}
}
func cursorACPString(opts *llmtypes.CallOptions, key string) string {
	if opts.Metadata == nil {
		return ""
	}
	v, _ := opts.Metadata.Custom[key].(string)
	return strings.TrimSpace(v)
}
func cursorACPBool(opts *llmtypes.CallOptions, key string) bool {
	if opts.Metadata == nil {
		return false
	}
	v, _ := opts.Metadata.Custom[key].(bool)
	return v
}
func (c *CursorCLIAdapter) launchACP(ctx context.Context, opts *llmtypes.CallOptions, cwd, owner, system, model, resume string) (*cursorACP, string, error) {
	cleanup, err := prepareCursorProjectFiles(llmtypes.CLIArtifactDirectory(opts, cwd), system, opts, owner)
	if err != nil {
		return nil, "", err
	}
	if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
		if err = c.ProjectSkills(llmtypes.CLIArtifactDirectory(opts, cwd), skills); err != nil {
			cleanup()
			return nil, "", err
		}
	}
	bin, err := exec.LookPath("cursor-agent")
	if err != nil {
		cleanup()
		return nil, "", err
	}
	args := []string{"--model", model, "--workspace", cwd, "--trust", "--approve-mcps"}
	if mode := cursorACPString(opts, MetadataKeyMode); mode != "" && mode != "agent" {
		args = append(args, "--mode", mode)
	}
	if sandbox := cursorSandboxUnderSeatbelt(opts, cursorACPString(opts, MetadataKeySandbox)); sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	// Preserve hooks and existing Full CLI/bridge policy. --force bypasses hooks.
	if cursorACPBool(opts, MetadataKeyAutoReview) {
		args = append(args, "--auto-review")
	} else if !cursorACPBool(opts, MetadataKeyDenyBuiltinTools) && !cursorFullNativeFromOptions(opts) && cursorACPBool(opts, MetadataKeyForce) {
		args = append(args, "--force")
	}
	if opts.Metadata != nil {
		if headers, ok := opts.Metadata.Custom[MetadataKeyHeaders].([]string); ok {
			for _, header := range headers {
				args = append(args, "--header", header)
			}
		}
		if dirs, ok := opts.Metadata.Custom[MetadataKeyPluginDirs].([]string); ok {
			for _, dir := range dirs {
				args = append(args, "--plugin-dir", dir)
			}
		}
	}
	if llmtypes.CLIHasPrivateRuntime(opts) {
		args = append(args, "--plugin-dir", opts.CLIRuntimeDir)
	}
	args = append(args, "acp")
	processCtx, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(processCtx, bin, args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	procshutdown.KillGroupOnCancel(cmd)
	cmd.Env = append(llmtypes.MergeCodingAgentSecretEnvironment(buildCursorStructuredEnv(c.apiKey), opts), "NO_OPEN_BROWSER=1")
	if llmtypes.CLIHasPrivateRuntime(opts) {
		configDir, configErr := prepareCursorPrivateRuntime(opts, cmd.Env)
		if configErr != nil {
			stop()
			cleanup()
			return nil, "", configErr
		}
		cmd.Env = append(cmd.Env, "CURSOR_CONFIG_DIR="+configDir)
	}
	sandboxCleanup, err := clisandbox.LandlockCmd(opts.CLISecurity, cmd, cwd, cursorLandlockReads(args, cwd), nil)
	if err != nil {
		stop()
		cleanup()
		return nil, "", err
	}
	allCleanup := func() { sandboxCleanup(); cleanup() }
	input, err := cmd.StdinPipe()
	if err != nil {
		stop()
		allCleanup()
		return nil, "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		input.Close()
		allCleanup()
		return nil, "", err
	}
	diagnostic := &cursorACPLaunchDiagnostic{}
	cmd.Stderr = diagnostic

	r := &cursorACP{cmd: cmd, stdin: input, pending: map[string]chan cursorACPMessage{}, events: make(chan cursorACPMessage, 4096), done: make(chan struct{}), exited: make(chan struct{}), stop: stop, cleanup: allCleanup, logger: c.logger}
	if err = cmd.Start(); err != nil {
		stop()
		input.Close()
		stdout.Close()
		allCleanup()
		return nil, "", err
	}
	go r.read(stdout)
	go func() {
		err := cmd.Wait()
		// Wait drains stderr before classifying process exit; stdout EOF can
		// arrive first. Never retain or forward the native stderr text.
		if diagnostic.modelUnavailable {
			err = &llmerrors.Error{Kind: llmerrors.KindModelNotFound, Provider: "cursor-cli", Model: model,
				Err: fmt.Errorf("Selected Cursor model is unavailable for this account. Choose Composer 2.5 or another available model in Models.")}
		}
		r.finish(err)
		allCleanup()
		close(r.exited)
	}()
	initCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err = r.call(initCtx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]any{"name": "agentworks", "version": "1"}}, nil); err != nil {
		r.close()
		return nil, "", err
	}
	// Existing CLI login/API-key auth is used by session/new. authenticate starts
	// a new interactive login even for signed-in clients, so never invoke it here.
	servers, err := cursorACPMCPServers(cursorACPString(opts, MetadataKeyMCPConfig))
	if err != nil {
		r.close()
		return nil, "", err
	}
	native, err := r.openSession(initCtx, cwd, servers, resume)
	if err != nil {
		r.close()
		return nil, "", err
	}
	// session/load replays history. It must not become output for the next turn.
	for len(r.events) > 0 {
		<-r.events
	}
	return r, native, nil
}

func (r *cursorACP) openSession(ctx context.Context, cwd string, servers any, resume string) (string, error) {
	params := map[string]any{"cwd": cwd, "mcpServers": servers}
	method := "session/new"
	if resume != "" {
		method = "session/load"
		params["sessionId"] = resume
	}
	var state struct {
		SessionID string `json:"sessionId"`
	}
	err := r.call(ctx, method, params, &state)
	var refusal *cursorACPError
	var detail struct {
		Message string `json:"message"`
	}
	missing := resume != "" && errors.As(err, &refusal) && refusal.Code == -32602 &&
		json.Unmarshal(refusal.Data, &detail) == nil && strings.Contains(detail.Message, resume) &&
		strings.Contains(strings.ToLower(detail.Message), "not found")
	if missing {
		// PLAT-872-50: an incompatible saved handle is refused before any
		// prompt. Start a new session and let the caller supply saved history.
		// Never recover transport failures or errors after submitting a prompt.
		resume = ""
		delete(params, "sessionId")
		err = r.call(ctx, "session/new", params, &state)
	}
	if err != nil {
		return "", err
	}
	native := state.SessionID
	if resume != "" {
		native = resume
	}
	if native == "" {
		return "", fmt.Errorf("Cursor ACP did not return a session ID")
	}
	return native, nil
}
func (c *CursorCLIAdapter) generateContentACP(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (response *llmtypes.ContentResponse, err error) {
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
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	owner := cursorInteractiveSessionIDFromOptions(opts)
	if owner == "" {
		owner = fmt.Sprintf("cursor-acp-%d", time.Now().UnixNano())
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
	cursorACPSessions.Lock()
	s := cursorACPSessions.sessions[owner]
	if s == nil {
		s = &cursorACPSession{}
		cursorACPSessions.sessions[owner] = s
	}
	cursorACPSessions.Unlock()
	if !s.turnMu.TryLock() {
		return nil, fmt.Errorf("Cursor is already working; queue this message")
	}
	defer s.turnMu.Unlock()
	s.mu.Lock()
	if s.idle != nil {
		s.idle.Stop()
	}
	r, native, old, hasHistory := s.rpc, s.native, s.fingerprint, s.hasHistory
	s.mu.Unlock()
	defer func() {
		if !cursorPersistentInteractiveFromOptions(opts) || err != nil {
			closeCursorACPInstance(owner, s)
			return
		}
		s.mu.Lock()
		s.idle = time.AfterFunc(cursorInteractiveIdleTimeout(), func() {
			if s.turnMu.TryLock() {
				defer s.turnMu.Unlock()
				closeCursorACPInstance(owner, s)
			}
		})
		s.mu.Unlock()
	}()
	resume := cursorResumeSessionIDFromOptions(opts)
	if r != nil {
		select {
		case <-r.done:
			r.close()
			r = nil
		default:
		}
	}
	if r != nil && (old != fingerprint || (resume != "" && resume != native)) {
		r.close()
		r = nil
	}
	if r == nil {
		if old != ([32]byte{}) && old != fingerprint {
			native = ""
		}
		if resume != "" {
			native = resume
		}
		requestedNative := native
		r, native, err = c.launchACP(ctx, opts, cwd, owner, system, model, native)
		if err != nil {
			return nil, err
		}
		hasHistory = requestedNative != "" && native == requestedNative
		s.mu.Lock()
		s.rpc = r
		s.native = native
		s.cwd = cwd
		s.model = model
		s.fingerprint = fingerprint
		s.hasHistory = hasHistory
		s.mu.Unlock()
		if requestedNative != "" && native != requestedNative && c.logger != nil {
			c.logger.Infof("[CURSOR_ACP] session_load_missing fallback=new chat=%q requested=%q new=%q", owner, requestedNative, native)
		}
		if opts.OnCodingProviderSession != nil {
			opts.OnCodingProviderSession(cursorACPSessionHandle(s))
		}
	}
	if llmtypes.CodingProviderLaunchOnlyFromOptions(opts) {
		return cursorACPResponse(s, "", nil), nil
	}
	resumePrompt := ""
	if hasHistory {
		resumePrompt = native
	} else if len(opts.CodingProviderHistory) > 0 {
		_, conversation = splitCursorSystemPrompt(opts.CodingProviderHistory)
	}
	prompt := buildCursorStructuredPrompt("", conversation, resumePrompt)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("Cursor prompt is empty")
	}
	response, err = c.consumeACPTurn(ctx, s, r, prompt, opts, emit)
	if err == nil {
		s.mu.Lock()
		s.hasHistory = true
		s.mu.Unlock()
	}
	return response, err
}
func cursorACPSessionHandle(s *cursorACPSession) llmtypes.CodingProviderSessionHandle {
	return llmtypes.CodingProviderSessionHandle{Provider: "cursor-cli", Transport: "acp", NativeSessionID: s.native, WorkingDir: s.cwd, Model: s.model, Status: llmtypes.CodingProviderSessionStatusIdle}
}

func cursorACPResponse(s *cursorACPSession, text string, usage *llmtypes.Usage) *llmtypes.ContentResponse {
	info := map[string]any{"cursor_session_id": s.native}
	handle := cursorACPSessionHandle(s)
	gi := &llmtypes.GenerationInfo{Additional: info}
	llmtypes.AttachCodingProviderSessionHandle(gi, handle)
	response := &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{Content: text, StopReason: "stop", GenerationInfo: gi}}, Usage: usage}
	return response
}

func cursorACPMCPServers(raw string) ([]any, error) {
	out := []any{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	var config struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, err
	}
	for name, s := range config.Servers {
		if s.Command != "" {
			env := []any{}
			for k, v := range s.Env {
				env = append(env, map[string]string{"name": k, "value": v})
			}
			args := s.Args
			if args == nil {
				args = []string{}
			}
			out = append(out, map[string]any{"name": name, "command": s.Command, "args": args, "env": env})
		} else if s.URL != "" {
			headers := []any{}
			for k, v := range s.Headers {
				headers = append(headers, map[string]string{"name": k, "value": v})
			}
			kind := "http"
			if s.Type == "sse" {
				kind = "sse"
			}
			out = append(out, map[string]any{"name": name, "type": kind, "url": s.URL, "headers": headers})
		} else {
			return nil, fmt.Errorf("Cursor MCP server %s needs command or URL", name)
		}
	}
	return out, nil
}
