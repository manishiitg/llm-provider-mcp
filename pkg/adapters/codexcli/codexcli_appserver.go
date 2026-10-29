package codexcli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
)

type codexAppSession struct {
	turnMu                       sync.Mutex
	mu                           sync.Mutex
	rpc                          *codexRPC
	threadID, turnID, cwd, model string
	fingerprint                  [32]byte
	status                       *llmtypes.StatusLine
	// A successful steer response acknowledges acceptance; the corresponding
	// userMessage item is the durable acknowledgement, scoped to this send.
	receipts []*codexAppReceipt
	// Steers the server accepted whose userMessage item has not been seen
	// (receipt id -> text). Codex runs a steer its turn did not consume as its
	// own next turn, which consumeAppTurn must then follow.
	steers     map[string]string
	rateLimits *codexRateLimits
	idle       *time.Timer
}
type codexAppReceipt struct {
	id        string
	text      string
	accepted  time.Time
	durable   chan struct{}
	confirmed bool
}

var codexAppSessions = struct {
	sync.Mutex
	sessions map[string]*codexAppSession
}{sessions: make(map[string]*codexAppSession)}

func codexAppSessionForOwner(owner string) *codexAppSession {
	codexAppSessions.Lock()
	defer codexAppSessions.Unlock()
	return codexAppSessions.sessions[owner]
}
func closeCodexAppSession(owner string) {
	codexAppSessions.Lock()
	s := codexAppSessions.sessions[owner]
	delete(codexAppSessions.sessions, owner)
	codexAppSessions.Unlock()
	if s != nil {
		s.mu.Lock()
		r := s.rpc
		if s.idle != nil {
			s.idle.Stop()
			s.idle = nil
		}
		s.mu.Unlock()
		if r != nil {
			r.close()
		}
	}
}
func closeCodexAppSessions() {
	codexAppSessions.Lock()
	owners := make([]string, 0, len(codexAppSessions.sessions))
	for o := range codexAppSessions.sessions {
		owners = append(owners, o)
	}
	codexAppSessions.Unlock()
	for _, o := range owners {
		closeCodexAppSession(o)
	}
}
func codexOptionString(opts *llmtypes.CallOptions, key string) string {
	if opts.Metadata == nil {
		return ""
	}
	v, _ := opts.Metadata.Custom[key].(string)
	return strings.TrimSpace(v)
}
func codexOptionBool(opts *llmtypes.CallOptions, key string) bool {
	if opts.Metadata == nil {
		return false
	}
	v, _ := opts.Metadata.Custom[key].(bool)
	return v
}

func (c *CodexCLIAdapter) launchAppServer(ctx context.Context, opts *llmtypes.CallOptions, cwd string) (*codexRPC, error) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil, err
	}
	policy := codexOptionString(opts, MetadataKeyApprovalPolicy)
	if policy == "" {
		policy = "never"
	}
	args := []string{}
	args = appendCodexDisableUpdateArgs(args)
	if codexOptionBool(opts, MetadataKeyDisableShellTool) {
		for _, feature := range codexBridgeOnlyDisabledFeatures {
			args = append(args, "--disable", feature)
		}
	}
	args = appendCodexFeatureCSV(args, "--disable", codexOptionString(opts, MetadataKeyDisableFeatures))
	args = appendCodexFeatureCSV(args, "--enable", codexOptionString(opts, MetadataKeyEnableFeatures))
	if opts.Metadata != nil {
		if overrides, ok := opts.Metadata.Custom[MetadataKeyConfigOverrides].([]string); ok {
			for _, v := range overrides {
				args = append(args, "-c", v)
			}
		}
	}
	args = append(args, "app-server", "--listen", "stdio://")
	life, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(life, bin, args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	procshutdown.KillGroupOnCancel(cmd)
	cmd.Env = llmtypes.MergeCodingAgentSecretEnvironment(buildCodexStructuredEnv(c.apiKey), opts)
	// A nil stderr goes directly to the null device (no copying goroutine
	// or inherited stderr pipe can delay process cleanup). Protocol errors
	// arrive on stdout; stderr may contain credentials.
	if llmtypes.NormalizeCLISecurityMode(policyMode(opts)) != llmtypes.CLISecurityModeCompatibility && !opts.CLISecurity.Confined() {
		stop()
		return nil, fmt.Errorf("Codex app-server requires enforced CLI confinement")
	}
	// MCP commands arrive later in thread configuration, so argv discovery
	// cannot grant their executable at launch (PLAT-872-57).
	mcpReads, err := strictCodexMCPRuntimePaths(opts)
	if err != nil {
		stop()
		return nil, err
	}
	cleanupSandbox, err := clisandbox.LandlockCmd(opts.CLISecurity, cmd, cwd, mcpReads, nil)
	if err != nil {
		stop()
		return nil, err
	}
	cleanup := func() { cleanupSandbox() }
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stop()
		cleanup()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		stdin.Close()
		cleanup()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		stop()
		stdin.Close()
		cleanup()
		return nil, err
	}
	r := &codexRPC{logger: c.logger, cmd: cmd, stdin: stdin, pending: make(map[string]chan codexRPCMessage), events: make(chan codexRPCMessage, 1024), done: make(chan struct{}), exited: make(chan struct{}), stop: stop, cleanup: cleanup}
	go r.read(stdout)
	go func() {
		err := cmd.Wait()
		r.finish(fmt.Errorf("codex app-server exited: %v", err))
		cleanup()
		close(r.exited)
	}()
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = r.call(initCtx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "agentworks", "title": "AgentWorks", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}, nil); err != nil {
		r.close()
		return nil, err
	}
	if err = r.write(map[string]any{"method": "initialized"}); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

func (c *CodexCLIAdapter) generateContentAppServer(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if opts.StreamChan != nil {
		defer close(opts.StreamChan)
	}
	emit := func(ch llmtypes.StreamChunk) {
		if opts.StreamChan != nil {
			select {
			case opts.StreamChan <- ch:
			case <-ctx.Done():
			}
		}
	}
	system, conversation := splitCodexSystemPrompt(messages)
	prompt := buildCodexInteractivePrompt(conversation)
	launchOnly := llmtypes.CodingProviderLaunchOnlyFromOptions(opts)
	idleCompact := llmtypes.CodingProviderIdleCompactFromOptions(opts)
	if prompt == "" && !launchOnly && !idleCompact {
		return nil, fmt.Errorf("codex-cli prompt is empty")
	}
	resume, err := codexValidatedResumeSessionIDFromOptions(opts)
	if err != nil {
		return nil, err
	}
	if idleCompact && resume == "" {
		return llmtypes.IdleCompactSkipResponse("codex-cli", llmtypes.IdleCompactReasonNoSession), nil
	}
	cwd := codexWorkingDirFromOptions(opts)
	system += llmtypes.CLIPrivateSkillInstructions(opts, codexSkillsSubdir)
	if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
		if err = c.ProjectSkills(llmtypes.CLIArtifactDirectory(opts, cwd), skills); err != nil {
			return nil, err
		}
	}
	owner := codexInteractiveSessionIDFromOptions(opts)
	if owner == "" {
		owner = "codex-app-" + codexRandomHex(12)
	}
	ctx = llmtypes.ScopeCodingAgentQuestions(ctx, "codex-cli", owner)
	persistent := codexPersistentInteractiveFromOptions(opts)
	codexAppSessions.Lock()
	s := codexAppSessions.sessions[owner]
	if s == nil {
		s = &codexAppSession{}
		codexAppSessions.sessions[owner] = s
	}
	codexAppSessions.Unlock()
	if !s.turnMu.TryLock() {
		return nil, fmt.Errorf("Codex session already has a turn; use live input")
	}
	defer s.turnMu.Unlock()
	s.mu.Lock()
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	s.mu.Unlock()
	if persistent {
		defer func() {
			s.mu.Lock()
			s.idle = time.AfterFunc(codexInteractiveIdleTimeout(), func() {
				if !s.turnMu.TryLock() {
					return
				}
				defer s.turnMu.Unlock()
				codexAppSessions.Lock()
				if codexAppSessions.sessions[owner] != s {
					codexAppSessions.Unlock()
					return
				}
				delete(codexAppSessions.sessions, owner)
				codexAppSessions.Unlock()
				s.mu.Lock()
				r := s.rpc
				s.mu.Unlock()
				if r != nil {
					r.close()
				}
			})
			s.mu.Unlock()
		}()
	}

	if !persistent {
		defer closeCodexAppSession(owner)
	}
	// Never reuse a process after its account, secret grants, sandbox or launch
	// config changes. Store only a digest, not another copy of credentials.
	config := map[string]any{"model": c.modelID, "cwd": cwd, "runtime_dir": opts.CLIRuntimeDir, "system": system, "native_questions": llmtypes.CodingAgentQuestionsAvailable(ctx), "security": opts.CLISecurity, "env": llmtypes.MergeCodingAgentSecretEnvironment(buildCodexStructuredEnv(c.apiKey), opts)}
	if opts.Metadata != nil {
		for k, v := range opts.Metadata.Custom {
			if k != MetadataKeyResumeSessionID && k != llmtypes.CodingProviderIdleCompactMetadataKey {
				config[k] = v
			}
		}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(raw)
	s.mu.Lock()
	r := s.rpc
	thread := s.threadID
	oldFingerprint := s.fingerprint
	s.mu.Unlock()
	if r != nil {
		select {
		case <-r.done:
			r.close()
			r = nil
		default:
		}
	}
	if r != nil && (oldFingerprint != fingerprint || (resume != "" && resume != thread)) {
		r.close()
		r = nil
	}
	if r == nil {
		if oldFingerprint != ([32]byte{}) && oldFingerprint != fingerprint {
			thread = ""
		} // caller supplies the authorized resume identity
		if resume != "" {
			thread = resume
		}
		r, err = c.launchAppServer(ctx, opts, cwd)
		if err != nil {
			return nil, err
		}
		sandbox := codexOptionString(opts, MetadataKeySandbox)
		if sandbox == "" {
			sandbox = "workspace-write"
		}
		sandbox = codexSandboxUnderConfinement(opts, sandbox)
		approval := codexOptionString(opts, MetadataKeyApprovalPolicy)
		if approval == "" {
			approval = "never"
			if llmtypes.CodingAgentQuestionsAvailable(ctx) {
				approval = "on-request"
			}
		}
		threadConfig, configErr := codexAppThreadConfig(opts, approval)
		if configErr != nil {
			r.close()
			return nil, configErr
		}
		threadConfig["features.default_mode_request_user_input"] = llmtypes.CodingAgentQuestionsAvailable(ctx)
		params := map[string]any{"cwd": cwd, "sandbox": sandbox, "approvalPolicy": approval, "developerInstructions": system, "config": threadConfig}
		model := codexOptionString(opts, MetadataKeyCodexModel)
		if model == "" {
			model = c.modelID
		}
		if model != "" && model != "codex-cli" {
			params["model"] = resolveCodexCLIModelID(model)
		}
		method := "thread/start"
		if thread != "" {
			method = "thread/resume"
			params["threadId"] = thread
			params["excludeTurns"] = true
		}
		var result struct {
			Thread struct {
				ID string `json:"id"`
			}
			Model string `json:"model"`
		}
		if err = r.call(ctx, method, params, &result); err != nil {
			r.close()
			return nil, err
		}
		if result.Thread.ID == "" {
			r.close()
			return nil, fmt.Errorf("codex app-server returned no thread id")
		}
		thread = result.Thread.ID
		s.mu.Lock()
		s.rpc = r
		s.threadID = thread
		s.cwd = cwd
		s.model = result.Model
		s.fingerprint = fingerprint
		s.status = nil
		s.rateLimits = nil
		s.mu.Unlock()
	}
	// Account snapshots are private to this process/account. Some short turns
	// emit no rateLimits/updated notification, so seed from the native read.
	rateCtx, rateCancel := context.WithTimeout(ctx, 10*time.Second)
	var limits struct {
		RateLimits json.RawMessage            `json:"rateLimits"`
		ByID       map[string]json.RawMessage `json:"rateLimitsByLimitId"`
	}
	if r.call(rateCtx, "account/rateLimits/read", map[string]any{}, &limits) == nil {
		raw := limits.RateLimits
		if codex, ok := limits.ByID["codex"]; ok {
			raw = codex
		}
		s.mu.Lock()
		s.rateLimits = codexAppRateLimits(raw)
		s.mu.Unlock()
	}
	rateCancel()
	if launchOnly {
		return codexAppResponse(s, "", codexAppUsage{}), nil
	}
	if idleCompact {
		return c.compactAppThread(ctx, s, r, thread, emit)
	}
	params := map[string]any{"threadId": thread, "input": codexAppTextInput(prompt)}
	if effort := codexOptionString(opts, MetadataKeyReasoningEffort); effort != "" {
		params["effort"] = effort
	}
	if summary := codexOptionString(opts, MetadataKeyReasoningSummary); summary != "" {
		params["summary"] = summary
	}
	var started struct {
		Turn codexAppTurn `json:"turn"`
	}
	if err = r.call(ctx, "turn/start", params, &started); err != nil {
		r.close()
		return nil, err
	}
	turn := started.Turn.ID
	if turn == "" {
		r.close()
		return nil, fmt.Errorf("codex app-server returned no turn id")
	}

	s.mu.Lock()
	s.turnID = turn
	s.steers = nil
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.turnID = ""; s.mu.Unlock() }()
	emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypePromptAccepted, Metadata: map[string]any{"codex_thread_id": thread, "codex_turn_id": turn}})
	if c.logger != nil {
		c.logger.Infof("codex app-server turn started owner=%s thread=%s turn=%s", owner, thread, turn)
	}
	return c.consumeAppTurn(ctx, s, r, thread, turn, emit)
}

func codexAppTextInput(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
}

type codexAppTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}
type codexAppUsage struct {
	Input      int `json:"inputTokens"`
	Cached     int `json:"cachedInputTokens"`
	CacheWrite int `json:"cacheWriteInputTokens"`
	Output     int `json:"outputTokens"`
	Reasoning  int `json:"reasoningOutputTokens"`
	Total      int `json:"totalTokens"`
}
type codexAppEvent struct {
	ThreadID   string          `json:"threadId"`
	TurnID     string          `json:"turnId"`
	ItemID     string          `json:"itemId"`
	Delta      string          `json:"delta"`
	Turn       codexAppTurn    `json:"turn"`
	Item       json.RawMessage `json:"item"`
	TokenUsage struct {
		Total  codexAppUsage `json:"total"`
		Last   codexAppUsage `json:"last"`
		Window int           `json:"modelContextWindow"`
	} `json:"tokenUsage"`
}

func codexAppResponse(s *codexAppSession, content string, usage codexAppUsage) *llmtypes.ContentResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := &llmtypes.GenerationInfo{InputTokens: intPtrIfNonZero(usage.Input), OutputTokens: intPtrIfNonZero(usage.Output), TotalTokens: intPtrIfNonZero(usage.Total), CachedContentTokens: intPtrIfNonZero(usage.Cached), ReasoningTokens: intPtrIfNonZero(usage.Reasoning), Additional: map[string]any{"provider": "codex-cli", "codex_mode": "app-server", "codex_thread_id": s.threadID, "codex_model": s.model, "prompt_tokens_include_cache": true, "cache_read_input_tokens": usage.Cached, "cache_creation_input_tokens": usage.CacheWrite}}
	if s.status != nil {
		for key, value := range s.status.Metadata {
			info.Additional[key] = value
		}
	}
	if windows := codexStructuredRateLimitWindows(s.rateLimits); len(windows) > 0 {
		info.Additional[llmtypes.RateLimitWindowsMetaKey] = windows
	}
	llmtypes.AttachCodingProviderSessionHandle(info, llmtypes.CodingProviderSessionHandle{Provider: "codex-cli", Transport: "app-server", NativeSessionID: s.threadID, WorkingDir: s.cwd, ProjectDirID: s.cwd, Model: s.model, Status: llmtypes.CodingProviderSessionStatusIdle})
	return &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{Content: content, StopReason: "stop", GenerationInfo: info}}, Usage: &llmtypes.Usage{InputTokens: usage.Input, OutputTokens: usage.Output, TotalTokens: usage.Total, CacheTokens: intPtrIfNonZero(usage.Cached), ReasoningTokens: intPtrIfNonZero(usage.Reasoning)}}
}

func SendCodexAppServerInput(ctx context.Context, owner, message string) error {
	s := codexAppSessionForOwner(owner)
	if s == nil {
		return fmt.Errorf("%w: no Codex app-server session for owner", llmtypes.ErrCodingAgentInputNotSent)
	}
	s.mu.Lock()
	r, thread, turn := s.rpc, s.threadID, s.turnID
	if r == nil || turn == "" {
		s.mu.Unlock()
		return fmt.Errorf("%w: no active Codex turn; start the next turn through GenerateContent", llmtypes.ErrCodingAgentInputNotSent)
	}
	receipt := &codexAppReceipt{id: codexRandomHex(16), text: message, accepted: time.Now(), durable: make(chan struct{})}
	s.receipts = append(s.receipts, receipt)
	if s.steers == nil {
		s.steers = map[string]string{}
	}
	s.steers[receipt.id] = message
	s.mu.Unlock()
	err := r.call(ctx, "turn/steer", map[string]any{"threadId": thread, "expectedTurnId": turn, "input": codexAppTextInput(message), "clientUserMessageId": receipt.id}, nil)
	if err != nil {
		s.mu.Lock()
		delete(s.steers, receipt.id)
		for i, v := range s.receipts {
			if v == receipt {
				s.receipts = append(s.receipts[:i], s.receipts[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
	return err
}
func InterruptCodexAppServer(ctx context.Context, owner string) error {
	s := codexAppSessionForOwner(owner)
	if s == nil {
		return fmt.Errorf("%w: no Codex app-server session for owner", llmtypes.ErrCodingAgentInputNotSent)
	}
	s.mu.Lock()
	r, thread, turn := s.rpc, s.threadID, s.turnID
	s.mu.Unlock()
	if r == nil || turn == "" {
		return fmt.Errorf("no active Codex turn")
	}
	return r.call(ctx, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn}, nil)
}

// app-server does not accept the CLI --profile flag. Send session config over
// its private stdio connection; MCP credentials never enter argv or a shared
// project config. Reuse the same config renderer and account-scoped exclusions.
func codexAppThreadConfig(opts *llmtypes.CallOptions, approval string) (map[string]any, error) {
	var servers map[string]codexMCPServerSpec
	raw := codexOptionString(opts, MetadataKeyMCPServers)
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &servers); err != nil {
			return nil, fmt.Errorf("parse Codex MCP config: %w", err)
		}
		if len(servers) == 0 {
			return nil, fmt.Errorf("Codex MCP config is empty")
		}
	}
	for name, spec := range servers {
		if approval == "never" && spec.DefaultToolsApprovalMode == "" {
			spec.DefaultToolsApprovalMode = "approve"
			servers[name] = spec
		}
	}
	home := llmtypes.CLIHomeEnvironment(opts)["CODEX_HOME"]
	if home == "" {
		home = os.Getenv("CODEX_HOME")
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".codex")
	}
	config := map[string]any{}
	if profile := codexOptionString(opts, MetadataKeyConfigProfile); profile != "" {
		if filepath.Base(profile) != profile {
			return nil, fmt.Errorf("invalid Codex config profile")
		}
		data, err := os.ReadFile(filepath.Join(home, profile+".config.toml"))
		if err != nil {
			return nil, err
		}
		if err = toml.Unmarshal(data, &config); err != nil {
			return nil, err
		}
	}
	defaults := map[string]any{}
	if err := toml.Unmarshal([]byte(renderCodexConfigTOML(servers, personalCodexMCPServers(home))), &defaults); err != nil {
		return nil, err
	}
	for key, value := range defaults {
		config[key] = value
	}
	// thread/start config replaces the features table supplied at process
	// startup. Carry the selected tool policy into this table too; otherwise
	// the defaults above can resurrect shell tools in an MCP-only session.
	features, _ := config["features"].(map[string]any)
	if features == nil {
		features = map[string]any{}
	}
	if codexOptionBool(opts, MetadataKeyDisableShellTool) {
		for _, name := range codexBridgeOnlyDisabledFeatures {
			features[name] = false
		}
	}
	for _, override := range []struct {
		key     string
		enabled bool
	}{{MetadataKeyDisableFeatures, false}, {MetadataKeyEnableFeatures, true}} {
		for _, name := range strings.Split(codexOptionString(opts, override.key), ",") {
			if name = strings.TrimSpace(name); name != "" {
				features[name] = override.enabled
			}
		}
	}
	config["features"] = features
	if dirs := codexOptionString(opts, MetadataKeyAdditionalDirs); dirs != "" {
		roots := []string{}
		for _, dir := range strings.Split(dirs, ",") {
			if dir = strings.TrimSpace(dir); dir != "" {
				roots = append(roots, dir)
			}
		}
		config["sandbox_workspace_write.writable_roots"] = roots
	}
	return config, nil
}
