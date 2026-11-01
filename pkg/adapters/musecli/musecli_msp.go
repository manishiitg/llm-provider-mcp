package musecli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

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
	argv := append([]string{"muse"}, museBaseURLArgv(opts)...)
	argv = append(argv, a.museMSPLaunchArgs(opts)...)
	launch := musePreludeArgv(opts, argv)
	life, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(life, launch[0], launch[1:]...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	procshutdown.KillGroupOnCancel(cmd)
	cmd.Env = llmtypes.MergeCodingAgentSecretEnvironment(museEnvironmentWithConfigHome(configHome), opts)
	cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+configHome)
	cmd.Env = append(cmd.Env, museRuntimeEnv(opts)...)
	if a.apiKey != "" {
		// stdin carries the protocol, so the key goes in the environment.
		cmd.Env = append(cmd.Env, "META_API_KEY="+a.apiKey)
	}
	cleanupSandbox, err := museLandlockCmd(opts, cmd, workdir)
	if err != nil {
		stop()
		return nil, err
	}
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
		r.finish(fmt.Errorf("muse serve exited: %v", err))
		cleanupSandbox()
		close(r.exited)
	}()
	initCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := r.call(initCtx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "agentworks", "version": "1"}}, nil); err != nil {
		r.close()
		return nil, err
	}
	if err := r.notify("initialized"); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
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
		return nil, false, err
	}
	s := &museMSPSession{owner: owner, rpc: rpc, workdir: workdir, dataHome: museAccountDataHome(ctx), fingerprint: fingerprint, cleanup: cleanupConfig}
	var opened museMSPSessionResult
	if resumeID != "" {
		err = rpc.call(ctx, "session/resume", map[string]any{"commandId": museCommandID(), "sessionId": resumeID}, &opened)
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
	museMSPSessions.Unlock()
	return s, true, nil
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
	llmtypes.SignalPromptAccepted(opts.StreamChan)
	response, err := a.consumeMuseMSPTurn(ctx, s, started.TurnID, opts, emit)
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
	return response, err
}
