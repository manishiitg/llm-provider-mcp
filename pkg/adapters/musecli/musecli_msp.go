package musecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/internal/slotfs"
	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/procshutdown"
	"github.com/manishiitg/multi-llm-provider-go/pkg/sessiontransfer"
)

// The MSP lane runs a chat on `muse serve`: one process per chat, speaking the
// Muse Session Protocol. Turn start and end, streaming, steering, stopping,
// compaction, usage and questions all arrive as protocol messages, so nothing
// is read off a screen. The session journal is the one the tmux and exec lanes
// write (XDG_DATA_HOME/muse/sessions/...), so a chat keeps its native id and
// moves between lanes and computers as before.

// MuseTransportEnv picks Muse's transport for a server: msp (default), tmux or
// structured (exec).
const MuseTransportEnv = "MUSE_CLI_TRANSPORT"

// MuseMSPSelected reports whether this server runs Muse chats on `muse serve`.
func MuseMSPSelected() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(MuseTransportEnv))) {
	case "tmux", "structured", "exec":
		return false
	}
	return true
}

// museMSPKeepMuseSandbox keeps Muse's own sandbox on in Full CLI mode; only
// the laptop live tests set it.
var museMSPKeepMuseSandbox = false

// museMSPTestCapabilities: AgentWorks asks for no MSP capabilities; a live
// probe may ask for userShell.
var museMSPTestCapabilities []string

type museMSPSession struct {
	turnMu    sync.Mutex
	mu        sync.Mutex
	rpc       *museRPC
	owner     string
	sessionID string
	workdir   string
	dataHome  string
	// roots is set when a transferred chat moved to this folder: every turn
	// names it, since Muse otherwise runs in the journal's original folder.
	roots []string
	// handoffPending: the next turn restates the current instructions (the
	// first turn after a transfer); it clears only when that turn succeeds.
	handoffPending bool
	fingerprint    string
	turnID         string
	usage          *museMSPPlanUsage
	context        museMSPContext
	cleanup        func()
	// listener receives the host's events while one of this chat's own turns
	// (or a compaction) runs; between them pumpMuseMSP handles them.
	listener chan museRPCMessage
	// wake: replies of turns Muse ran on its own, by turn, until the turn ends.
	wake map[string]*museMSPWake
	// steered: messages turn/steer accepted, for the durable receipt.
	steered map[string]time.Time
	// steers: what the running turn was given; see museMSPMissedSteers.
	steers []*museMSPSteer
}

func (s *museMSPSession) steerReceipt(message string) (MuseDurableAck, error) {
	s.mu.Lock()
	at, ok := s.steered[message]
	delete(s.steered, message)
	path := museSessionLogPath(s.sessionID, s.dataHome)
	s.mu.Unlock()
	if !ok {
		return MuseDurableAck{}, fmt.Errorf("Muse did not take the message")
	}
	return MuseDurableAck{Outcome: MuseDurableAckConfirmed, Latency: time.Since(at), ProofPath: path, RowTimestamp: at}, nil
}

func closeAllMuseMSPSessions() {
	museMSPSessions.Lock()
	owners := make([]string, 0, len(museMSPSessions.m))
	for owner := range museMSPSessions.m {
		owners = append(owners, owner)
	}
	museMSPSessions.Unlock()
	for _, owner := range owners {
		CloseMuseMSPSession(owner)
	}
}

type museMSPContext struct {
	used, window int
}

type museMSPPlanUsage struct {
	Window *struct {
		UsedPercent        int   `json:"usedPercent"`
		ResetsAtMs         int64 `json:"resetsAtMs"`
		WindowDurationMins int   `json:"windowDurationMins"`
	} `json:"window,omitempty"`
	Weekly *struct {
		UsedPercent int   `json:"usedPercent"`
		ResetsAtMs  int64 `json:"resetsAtMs"`
	} `json:"weekly,omitempty"`
	Tier string `json:"tier,omitempty"`
}

var museMSPSessions = struct {
	sync.Mutex
	m map[string]*museMSPSession
}{m: map[string]*museMSPSession{}}

func museMSPSessionFor(owner string) *museMSPSession {
	museMSPSessions.Lock()
	defer museMSPSessions.Unlock()
	return museMSPSessions.m[owner]
}

// CloseMuseMSPSession ends a chat's `muse serve` process: it leaves the session
// so the journal has no writer (an import takes the session lock next), then
// stops the process.
func CloseMuseMSPSession(owner string) {
	museMSPSessions.Lock()
	s := museMSPSessions.m[owner]
	delete(museMSPSessions.m, owner)
	museMSPSessions.Unlock()
	if s != nil {
		s.close()
	}
}

func (s *museMSPSession) close() {
	s.mu.Lock()
	rpc, sessionID := s.rpc, s.sessionID
	s.mu.Unlock()
	if rpc == nil {
		return
	}
	if sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = rpc.call(ctx, "session/leave", map[string]any{"commandId": museCommandID(), "sessionId": sessionID}, nil)
		cancel()
	}
	rpc.close()
	if s.cleanup != nil {
		s.cleanup()
	}
}

// museMSPLaunchArgs is the serve command line: the same provider, model and
// native-tool policy as the TUI and exec lanes. Full CLI matches --yolo (no
// Muse sandbox; AgentWorks' confinement is the boundary); an allowlist turns
// Muse's own shell and writes off.
func (a *MuseCLIAdapter) museMSPLaunchArgs(opts *llmtypes.CallOptions) []string {
	provider := a.museExecProvider()
	argv := []string{"serve", "--provider", provider, "--trust-workspace"}
	model := strings.TrimSpace(a.modelID)
	if strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model != "" && model != DefaultModelID && provider != "echo" {
		argv = append(argv, "--model", model)
	}
	if _, allowlist := museToolAllowlistFromOptions(opts); allowlist {
		argv = append(argv, museNativeContainmentArgv()...)
	} else if !museMSPKeepMuseSandbox {
		argv = append(argv, "--disable-sandbox")
	}
	return argv
}

func (a *MuseCLIAdapter) launchMuseMSP(ctx context.Context, opts *llmtypes.CallOptions, workdir, configHome string) (*museRPC, error) {
	if _, err := exec.LookPath("muse"); err != nil {
		return nil, fmt.Errorf("muse CLI not in PATH: %w", err)
	}
	argv := append([]string{"muse"}, a.museMSPLaunchArgs(opts)...)
	argv = append(argv, museMSPBaseURLArgv(opts)...)
	launch := musePreludeArgv(opts, argv)
	life, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(life, launch[0], launch[1:]...)
	cmd.Dir = workdir
	procshutdown.SetGroup(cmd)
	procshutdown.KillGroupOnCancel(cmd)
	cmd.Env = llmtypes.MergeCodingAgentSecretEnvironment(museEnvironmentWithConfigHome(configHome), opts)
	cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+configHome)
	cmd.Env = append(cmd.Env, museRuntimeEnv(opts)...)
	var keyFiles []string
	removeKey := func() {}
	// The key Muse uses (the selected one, else one the account or the server
	// gives in the environment, such as a company account's model-proxy key)
	// goes in this launch's own key file, never in the environment Muse's
	// shells inherit.
	key := a.apiKey
	if key == "" {
		key = museEnvValue(cmd.Env, "META_API_KEY")
	}
	if key != "" && runtime.GOOS == "darwin" {
		// On a Mac `muse auth set` saves into the macOS Keychain, not a file: a
		// launch folder has no keychain, so it hangs and macOS asks the person
		// about a missing keychain. The Mac keeps the key in the environment.
		if a.apiKey != "" {
			cmd.Env = append(museWithoutEnv(cmd.Env, "META_API_KEY"), "META_API_KEY="+a.apiKey)
		}
		key = ""
	}
	if key != "" {
		cmd.Env = museWithoutEnv(cmd.Env, "META_API_KEY")
		keyFile, cleanupKey, err := a.museMSPStoreKey(ctx, opts, workdir, configHome, key, cmd.Env)
		if err != nil {
			stop()
			return nil, err
		}
		keyFiles, removeKey = []string{keyFile}, cleanupKey
	}
	cleanupLandlock, err := museLandlockCmdWithCredentials(opts, cmd, workdir, keyFiles)
	cleanupSandbox := func() { cleanupLandlock(); removeKey() }
	if err != nil {
		removeKey()
		stop()
		return nil, err
	}
	stderr := &clisandbox.StderrTail{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stop()
		cleanupSandbox()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		_ = stdin.Close()
		cleanupSandbox()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stop()
		_ = stdin.Close()
		cleanupSandbox()
		return nil, fmt.Errorf("start muse serve: %w", err)
	}
	r := &museRPC{logger: a.logger, cmd: cmd, stdin: stdin, pending: map[string]chan museRPCMessage{}, events: make(chan museRPCMessage, 4096), done: make(chan struct{}), exited: make(chan struct{}), stop: stop}
	go r.read(stdout)
	go func() {
		err := cmd.Wait()
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			a.logger.Infof("[CLI_EXIT] provider=muse-cli serve exited (%v): %s", err, tail)
		}
		r.finish(fmt.Errorf("muse serve exited: %v", err))
		cleanupSandbox()
		close(r.exited)
	}()
	// A large chat's serve can take minutes to start (PLAT-872-119: a 60s bound killed it before it finished, every turn).
	started := time.Now()
	initCtx, cancel := context.WithTimeout(ctx, museMSPStartTimeout)
	defer cancel()
	initParams := map[string]any{"clientInfo": map[string]string{"name": "agentworks", "version": "1"}}
	if len(museMSPTestCapabilities) > 0 {
		initParams["capabilities"] = map[string]any{"requestedCapabilities": museMSPTestCapabilities}
	}
	if err := r.call(initCtx, "initialize", initParams, nil); err != nil {
		a.logger.Infof("[MUSE_MSP] serve did not start in %s: %v", time.Since(started).Round(time.Millisecond), err)
		r.close()
		return nil, err
	}
	a.logger.Infof("[MUSE_MSP] serve started in %s", time.Since(started).Round(time.Millisecond))
	if err := r.notify("initialized"); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

// museMSPStoreKey saves the selected Meta key with `muse auth set`, the key on
// stdin, run as the same account as serve (stdin carries the protocol for serve
// itself, and an environment variable would reach every shell Muse starts).
// The file lives in its own private folder, outside every folder Muse's shells
// are granted; the launch's config folder links to it, and the Landlock policy
// treats it as a login file: Muse reads it, its shells cannot (as for the
// account's own login file). It is removed when the chat closes.
func (a *MuseCLIAdapter) museMSPStoreKey(ctx context.Context, opts *llmtypes.CallOptions, workdir, configHome, key string, env []string) (string, func(), error) {
	keyHome, err := os.MkdirTemp("", "agentworks-muse-key-*")
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.RemoveAll(keyHome) }
	if opts != nil && opts.CLISecurity != nil {
		if err := slotfs.ShareTree(opts.CLISecurity.PrivateHome, keyHome); err != nil {
			remove()
			return "", nil, err
		}
	}
	argv := musePreludeArgv(opts, []string{"muse", "auth", "set", "--api-key-stdin"})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workdir
	cmd.Env = append(museWithoutEnv(env, "XDG_CONFIG_HOME"), "XDG_CONFIG_HOME="+keyHome)
	cmd.Stdin = strings.NewReader(key + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		remove()
		return "", nil, fmt.Errorf("store the Muse key for this chat: %w: %s", err, strings.TrimSpace(string(out)))
	}
	keyFile := filepath.Join(keyHome, "muse", "auth.json")
	_ = os.Chmod(keyFile, 0o600)
	link := filepath.Join(configHome, "muse", "auth.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		remove()
		return "", nil, err
	}
	_ = os.Remove(link)
	if err := os.Symlink(keyFile, link); err != nil {
		remove()
		return "", nil, fmt.Errorf("link the Muse key for this chat: %w", err)
	}
	return keyFile, remove, nil
}

// museEnvValue is the last value of name in env (the one a process sees).
func museEnvValue(env []string, name string) string {
	value := ""
	for _, entry := range env {
		if key, v, ok := strings.Cut(entry, "="); ok && key == name {
			value = v
		}
	}
	return value
}

func museWithoutEnv(env []string, name string) []string {
	out := env[:0:0]
	for _, entry := range env {
		if key, _, _ := strings.Cut(entry, "="); key != name {
			out = append(out, entry)
		}
	}
	return out
}

type museMSPSessionResult struct {
	Session struct {
		SessionID     string  `json:"sessionId"`
		WorkspaceRoot *string `json:"workspaceRoot"`
		Status        string  `json:"status"`
	} `json:"session"`
}

// museMSPOpen returns the chat's live serve session, launching and opening it
// when needed: a chat with a native id always resumes that id, never a fresh
// session. A resumed session in another folder is used only when the transfer
// sidecar binds this exact id to this folder.
func (a *MuseCLIAdapter) museMSPOpen(ctx context.Context, opts *llmtypes.CallOptions, owner, workdir, resumeID, fingerprint string) (*museMSPSession, bool, error) {
	if s := museMSPSessionFor(owner); s != nil {
		s.mu.Lock()
		alive := s.rpc != nil && s.fingerprint == fingerprint && (resumeID == "" || resumeID == s.sessionID)
		select {
		case <-s.rpc.done:
			alive = false
		default:
		}
		s.mu.Unlock()
		if alive {
			return s, false, nil
		}
		CloseMuseMSPSession(owner)
	}
	mcpJSON := strings.TrimSpace(museMCPConfigFromOptions(opts))
	toolAllowlist, toolAllowlistSet := museToolAllowlistFromOptions(opts)
	if !toolAllowlistSet {
		toolAllowlist = nil
	}
	configHome, cleanupConfig, err := museAccountConfig(ctx, mcpJSON, toolAllowlist)
	if err != nil {
		return nil, false, err
	}
	rpc, err := a.launchMuseMSP(ctx, opts, workdir, configHome)
	if err != nil {
		cleanupConfig()
		return nil, false, museReopenError(resumeID, err)
	}
	s := &museMSPSession{owner: owner, rpc: rpc, workdir: workdir, dataHome: museAccountDataHome(ctx), fingerprint: fingerprint, cleanup: cleanupConfig}
	var opened museMSPSessionResult
	if resumeID != "" {
		resumeStarted := time.Now()
		err = rpc.call(ctx, "session/resume", map[string]any{"commandId": museCommandID(), "sessionId": resumeID}, &opened)
		a.logger.Infof("[MUSE_MSP] session %s reopen took %s (err=%v)", resumeID, time.Since(resumeStarted).Round(time.Millisecond), err)
		err = museReopenError(resumeID, err)
		if err == nil {
			root := ""
			if opened.Session.WorkspaceRoot != nil {
				root = *opened.Session.WorkspaceRoot
			}
			if root != "" && !museSameFolder(root, workdir) {
				if !sessiontransfer.MuseHandoffWorkspace(museAccountDataHome(ctx), resumeID, workdir) {
					err = fmt.Errorf("Muse session %s belongs to another folder (%s); it can move here only after a transfer to this folder", resumeID, root)
				} else {
					s.roots = []string{workdir}
					s.handoffPending = true
				}
			}
		}
	} else {
		err = rpc.call(ctx, "session/start", map[string]any{"commandId": museCommandID(), "workspaceRoot": workdir, "approvalMode": "allowAll"}, &opened)
	}
	if err != nil {
		s.close()
		return nil, false, err
	}
	s.sessionID = opened.Session.SessionID
	// The plan usage the host last observed, for the chat's usage line.
	var usage struct {
		Usage *museMSPPlanUsage `json:"usage"`
	}
	if rpc.call(ctx, "usage/read", nil, &usage) == nil {
		s.usage = usage.Usage
	}
	museMSPSessions.Lock()
	museMSPSessions.m[owner] = s
	go a.pumpMuseMSP(s)
	museMSPSessions.Unlock()
	return s, true, nil
}

// museMSPStartTimeout bounds `muse serve` answering initialize.
const museMSPStartTimeout = 5 * time.Minute

// museReopenError gives a chat that could not reopen in time a plain next step: its history is too long to load, so
// a new chat continues the work (the app shows "Start a new chat" for a too-long conversation).
func museReopenError(resumeID string, err error) error {
	if err == nil || resumeID == "" || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &llmerrors.Error{Kind: llmerrors.KindContextTooLong, Provider: "muse-cli",
		Err: fmt.Errorf("Muse could not reopen this chat in time: its history is too long to load (%v)", err)}
}

func museSameFolder(a, b string) bool {
	clean := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	return clean(a) == clean(b)
}

// generateContentMSP runs one turn on the chat's `muse serve` session.
func (a *MuseCLIAdapter) generateContentMSP(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	emit := func(chunk llmtypes.StreamChunk) {
		if opts.StreamChan != nil {
			select {
			case opts.StreamChan <- chunk:
			case <-ctx.Done():
			}
		}
	}
	ctx = museWithAccount(ctx, opts, a.apiKey)
	workdir := strings.TrimSpace(museWorkingDirFromOptions(opts))
	if workdir == "" {
		var err error
		if workdir, err = os.Getwd(); err != nil {
			return nil, fmt.Errorf("resolve Muse working directory: %w", err)
		}
	}
	owner := strings.TrimSpace(museInteractiveSessionIDFromOptions(opts))
	if owner == "" {
		owner = "msp-oneshot-" + museCommandID()
		defer CloseMuseMSPSession(owner)
	}
	resumeID := strings.TrimSpace(museResumeSessionIDFromOptions(opts))
	fingerprint := strings.Join(append(a.museMSPLaunchArgs(opts), workdir, museMCPConfigFromOptions(opts)), "\x00")
	s, _, err := a.museMSPOpen(ctx, opts, owner, workdir, resumeID, fingerprint)
	if err != nil {
		return nil, err
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	if llmtypes.CodingProviderLaunchOnlyFromOptions(opts) {
		gi := &llmtypes.GenerationInfo{Additional: map[string]interface{}{"provider": "muse-cli", "muse_cli_mode": "msp", "muse_session_id": s.sessionID}}
		llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{Provider: "muse-cli", Transport: llmtypes.CodingProviderTransportMSP, NativeSessionID: s.sessionID, WorkingDir: s.workdir, Status: llmtypes.CodingProviderSessionStatusIdle})
		return &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{GenerationInfo: gi}}}, nil
	}
	if llmtypes.CodingProviderIdleCompactFromOptions(opts) {
		return a.compactMuseMSP(ctx, s, emit)
	}
	prompt, err := museBuildExecPrompt(messages, resumeID != "")
	if err != nil {
		return nil, err
	}
	prompt = llmtypes.CLIPrivateSkillInstructions(opts, museSkillsSubdir) + prompt
	prompt = museWithResumeNote(prompt, resumeID != "", museMCPConfigFromOptions(opts))
	handoff := s.handoffPending
	if handoff {
		// The first turn after a transfer restates the current instructions.
		system, human, splitErr := museSplitPrompt(messages)
		if splitErr != nil {
			return nil, splitErr
		}
		prompt = nativeHandoffInstructions(museWithResumeNote(human, true, museMCPConfigFromOptions(opts)), llmtypes.CLIPrivateSkillInstructions(opts, museSkillsSubdir)+strings.Join(system, "\n\n"))
	}
	if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
		_ = a.ProjectSkills(llmtypes.CLIArtifactDirectory(opts, workdir), skills)
	}
	params := map[string]any{"commandId": museCommandID(), "sessionId": s.sessionID, "input": []map[string]any{{"type": "text", "text": prompt}}}
	if effort, effortErr := museExecEffort(opts); effortErr != nil {
		return nil, effortErr
	} else if effort != "" {
		params["reasoningEffort"] = effort
	}
	if len(s.roots) > 0 {
		params["workspaceRoots"] = s.roots
	}
	return a.runMuseMSPTurns(ctx, s, owner, params, handoff, opts, emit)
}

// runMuseMSPTurns runs the turn in params, then one more for any message that
// turn was given and never read. The caller's Run stays open across them, so
// the message is answered inside the same completion that a steer read in time
// would have been.
func (a *MuseCLIAdapter) runMuseMSPTurns(ctx context.Context, s *museMSPSession, owner string, params map[string]any, handoff bool, opts *llmtypes.CallOptions, emit func(llmtypes.StreamChunk)) (*llmtypes.ContentResponse, error) {
	events := s.listen()
	defer s.stopListening()
	totals := &museMSPTurnTotals{}
	for {
		var started struct {
			TurnID string `json:"turnId"`
		}
		if err := s.rpc.call(ctx, "turn/start", params, &started); err != nil {
			CloseMuseMSPSession(owner)
			return nil, err
		}
		s.mu.Lock()
		s.turnID = started.TurnID
		s.mu.Unlock()
		if totals.followups == 0 {
			llmtypes.SignalPromptAccepted(opts.StreamChan)
		} else {
			log.Printf("[MUSE_MSP] steer handover session=%s owner=%s next_turn=%s", s.sessionID, owner, started.TurnID)
		}
		response, err := a.consumeMuseMSPTurn(ctx, s, events, started.TurnID, totals, opts, emit)
		s.mu.Lock()
		s.turnID = ""
		if handoff && err == nil {
			s.handoffPending = false
		}
		s.mu.Unlock()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = s.rpc.call(interruptCtx, "turn/interrupt", map[string]any{"commandId": museCommandID(), "sessionId": s.sessionID, "turnId": started.TurnID}, nil)
			cancel()
		}
		if err != nil {
			s.clearSteers()
			return response, err
		}
		missed := a.museMSPMissedSteers(ctx, s, events)
		if len(missed) == 0 {
			return response, nil
		}
		log.Printf("[MUSE_MSP] steer handover session=%s owner=%s finished_turn=%s missed=%d: the turn ended before reading them, running them as the next turn", s.sessionID, owner, started.TurnID, len(missed))
		totals.followups++
		params["commandId"] = museCommandID()
		params["input"] = []map[string]any{{"type": "text", "text": strings.Join(missed, "\n\n")}}
	}
}

// museMSPBaseURLArgv points serve at an account's own Meta address (a company account reaches Meta through this
// Mac's backend). serve takes no --base-url flag (it exits at once, "unknown argument"); the address is its
// endpoint_transport setting, with the bearer key pinned to it so Muse sends the key there.
func museMSPBaseURLArgv(opts *llmtypes.CallOptions) []string {
	url := llmtypes.ProviderAccountEnvironment(opts)[llmtypes.MuseBaseURLKey]
	if url == "" {
		return nil
	}
	quoted, _ := json.Marshal(url)
	return []string{"-c", "endpoint_transport.base_url=" + string(quoted), "-c", `endpoint_transport.auth="bearer"`}
}
