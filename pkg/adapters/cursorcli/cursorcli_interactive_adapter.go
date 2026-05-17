package cursorcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	defaultCursorInteractiveTimeout     = 30 * time.Minute
	defaultCursorInteractiveIdleTimeout = 20 * time.Minute
	defaultCursorInteractivePromptWait  = 25 * time.Second
	cursorInteractiveStableWindow       = 1200 * time.Millisecond

	EnvCursorInteractiveSessionPrefix      = "CURSOR_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvCursorInteractiveTimeoutSeconds     = "CURSOR_CLI_INTERACTIVE_TIMEOUT_SECONDS"
	EnvCursorInteractiveIdleTimeoutSeconds = "CURSOR_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvCursorInteractivePromptWaitSeconds  = "CURSOR_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvCursorInteractiveStreamTmuxScreen   = "CURSOR_CLI_STREAM_TMUX_SCREEN"
)

type cursorInteractiveSession struct {
	ownerSessionID    string
	tmuxSessionName   string
	workingDir        string
	launchFingerprint string
	persistent        bool
	cleanupFiles      func()
	idleTimer         *time.Timer
	initErr           error
	createdAt         time.Time
	lastUsed          time.Time
	mu                sync.Mutex
}

var cursorInteractiveRegistry = struct {
	sync.RWMutex
	sessions map[string]string
}{
	sessions: map[string]string{},
}

var cursorPersistentRegistry = struct {
	sync.Mutex
	sessions map[string]*cursorInteractiveSession
}{
	sessions: map[string]*cursorInteractiveSession{},
}

func (c *CursorCLIAdapter) generateContentTmux(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; cursor-cli tmux mode requires tmux")
	}
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		return nil, fmt.Errorf("cursor-agent not found in PATH. Install Cursor Agent CLI with `curl https://cursor.com/install -fsS | bash`")
	}

	persistent := cursorPersistentInteractiveFromOptions(opts)
	ownerSessionID := cursorInteractiveSessionIDFromOptions(opts)
	if ownerSessionID == "" {
		ownerSessionID = "cursor-bounded-" + cursorRandomHex(8)
	}

	callCtx, cancel := context.WithTimeout(ctx, cursorInteractiveTimeout())
	defer cancel()

	systemPrompt, conversationMessages := splitCursorSystemPrompt(messages)
	historicalAssistantTexts := cursorAssistantHistory(conversationMessages)
	resume := cursorResumeSessionIDFromOptions(opts) != ""
	prompt := buildCursorPrompt(conversationMessages, resume)
	if strings.TrimSpace(prompt) == "" {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("cursor-cli prompt is empty")
	}

	session, err := c.acquireCursorInteractiveSession(callCtx, ownerSessionID, persistent, opts, systemPrompt)
	if err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	releaseSession := true
	defer func() {
		if !releaseSession || session == nil {
			return
		}
		if persistent {
			releaseCursorInteractiveSession(session, c.logger)
		} else {
			closeCursorSessionLocked(session, "bounded turn complete", c.logger)
		}
	}()

	if err := waitForCursorPrompt(callCtx, session.tmuxSessionName); err != nil {
		markCursorInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedCursorInteractiveSession(failedSession)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	resetCursorPaneForTurn(callCtx, session.tmuxSessionName)
	if err := waitForCursorPrompt(callCtx, session.tmuxSessionName); err != nil {
		markCursorInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedCursorInteractiveSession(failedSession)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	baseline, _ := captureCursorPane(callCtx, session.tmuxSessionName)
	c.logInfof("Executing Cursor Agent CLI tmux session: %s", session.tmuxSessionName)
	if err := sendCursorInputToTmux(callCtx, session.tmuxSessionName, prompt); err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	captured, err := waitForCursorInteractiveResponse(callCtx, session.tmuxSessionName, baseline, prompt, historicalAssistantTexts, opts.StreamChan, cursorAutoApproveWebSearchFromOptions(opts))
	if err != nil {
		if isCursorTmuxSessionLostError(err) {
			markCursorInteractiveSessionFailedLocked(session, err, c.logger)
			releaseSession = false
			failedSession := session
			session.mu.Unlock()
			session = nil
			cleanupFailedCursorInteractiveSession(failedSession)
		} else if ctx.Err() != nil {
			interruptCursorInteractiveSession(session.tmuxSessionName, c.logger)
		}
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	content := parseCursorInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content: content,
				GenerationInfo: &llmtypes.GenerationInfo{
					Additional: map[string]interface{}{
						"provider":                      "cursor-cli",
						"cursor_mode":                   "tmux",
						"cursor_interactive_session":    session.tmuxSessionName,
						"cursor_persistent_interactive": persistent,
						"cursor_uses_print_json":        false,
						"cursor_working_dir":            session.workingDir,
					},
				},
			},
		},
		Usage: &llmtypes.Usage{},
	}, nil
}

// acquireCursorInteractiveSession returns with session.mu held.
func (c *CursorCLIAdapter) acquireCursorInteractiveSession(ctx context.Context, ownerSessionID string, persistent bool, opts *llmtypes.CallOptions, systemPrompt string) (*cursorInteractiveSession, error) {
	launchFingerprint := c.cursorInteractiveLaunchFingerprint(opts, systemPrompt)

	if persistent {
		cursorPersistentRegistry.Lock()
		if existing := cursorPersistentRegistry.sessions[ownerSessionID]; existing != nil {
			existing.mu.Lock()
			if existing.initErr != nil {
				err := existing.initErr
				existing.mu.Unlock()
				cursorPersistentRegistry.Unlock()
				return nil, err
			}
			if existing.launchFingerprint != launchFingerprint {
				existing.mu.Unlock()
				cursorPersistentRegistry.Unlock()
				closeCursorPersistentSession(ownerSessionID, "launch configuration changed", c.logger)
				return c.acquireCursorInteractiveSession(ctx, ownerSessionID, persistent, opts, systemPrompt)
			}
			if existing.idleTimer != nil {
				existing.idleTimer.Stop()
				existing.idleTimer = nil
			}
			existing.lastUsed = time.Now()
			cursorPersistentRegistry.Unlock()
			return existing, nil
		}
	}

	now := time.Now()
	session := &cursorInteractiveSession{
		ownerSessionID:    ownerSessionID,
		tmuxSessionName:   newCursorTmuxSessionName(),
		launchFingerprint: launchFingerprint,
		persistent:        persistent,
		createdAt:         now,
		lastUsed:          now,
	}
	session.mu.Lock()
	if persistent {
		cursorPersistentRegistry.sessions[ownerSessionID] = session
		cursorPersistentRegistry.Unlock()
	}

	args, env, workingDir, cleanupFiles, err := c.buildCursorInteractiveLaunch(opts, systemPrompt)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		if persistent {
			removeCursorPersistentSession(ownerSessionID, session)
		}
		return nil, err
	}
	session.workingDir = workingDir
	session.cleanupFiles = cleanupFiles

	if err := startCursorTmuxSession(ctx, session.tmuxSessionName, args, env, workingDir); err != nil {
		session.initErr = err
		if cleanupFiles != nil {
			cleanupFiles()
		}
		session.mu.Unlock()
		if persistent {
			removeCursorPersistentSession(ownerSessionID, session)
		}
		return nil, err
	}
	registerCursorInteractiveSession(ownerSessionID, session.tmuxSessionName)
	return session, nil
}

func (c *CursorCLIAdapter) buildCursorInteractiveLaunch(opts *llmtypes.CallOptions, systemPrompt string) ([]string, []string, string, func(), error) {
	workingDir := cursorWorkingDirFromOptions(opts)
	if workingDir == "" {
		workingDir = cursorMustGetwd()
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, nil, "", nil, fmt.Errorf("failed to create Cursor CLI working directory: %w", err)
	}

	cleanupFiles, err := prepareCursorProjectFiles(workingDir, systemPrompt, opts)
	if err != nil {
		return nil, nil, "", nil, err
	}

	modelToUse := resolveCursorCLIModelID(c.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyCursorModel].(string); ok {
			modelToUse = resolveCursorCLIModelID(model)
		}
	}

	args := []string{"cursor-agent", "--workspace", workingDir}
	if modelToUse != "" {
		args = append(args, "--model", modelToUse)
	}
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if force, ok := opts.Metadata.Custom[MetadataKeyForce].(bool); ok && force {
			args = append(args, "--force")
		}
		if approve, ok := opts.Metadata.Custom[MetadataKeyApproveMCPs].(bool); ok && approve {
			args = append(args, "--approve-mcps")
		}
		if sandbox, ok := opts.Metadata.Custom[MetadataKeySandbox].(string); ok && strings.TrimSpace(sandbox) != "" {
			args = append(args, "--sandbox", strings.TrimSpace(sandbox))
		}
		if mode, ok := opts.Metadata.Custom[MetadataKeyMode].(string); ok && strings.TrimSpace(mode) != "" {
			args = append(args, "--mode", strings.TrimSpace(mode))
		}
		if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && strings.TrimSpace(resumeID) != "" {
			args = append(args, "--resume", strings.TrimSpace(resumeID))
		}
		if headers, ok := opts.Metadata.Custom[MetadataKeyHeaders].([]string); ok {
			for _, header := range headers {
				if strings.TrimSpace(header) != "" {
					args = append(args, "-H", strings.TrimSpace(header))
				}
			}
		}
		if pluginDirs, ok := opts.Metadata.Custom[MetadataKeyPluginDirs].([]string); ok {
			for _, dir := range pluginDirs {
				if strings.TrimSpace(dir) != "" {
					args = append(args, "--plugin-dir", strings.TrimSpace(dir))
				}
			}
		}
	}

	env := []string{}
	if strings.TrimSpace(c.apiKey) != "" {
		env = append(env, "CURSOR_API_KEY="+strings.TrimSpace(c.apiKey))
	}
	return args, env, workingDir, cleanupFiles, nil
}

func (c *CursorCLIAdapter) cursorInteractiveLaunchFingerprint(opts *llmtypes.CallOptions, systemPrompt string) string {
	custom := map[string]interface{}{}
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		custom = opts.Metadata.Custom
	}
	modelToUse := resolveCursorCLIModelID(c.modelID)
	if model, ok := custom[MetadataKeyCursorModel].(string); ok {
		modelToUse = resolveCursorCLIModelID(model)
	}

	hash := sha256.New()
	write := func(key, value string) {
		_, _ = io.WriteString(hash, key)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writeBool := func(key string) {
		if value, ok := custom[key].(bool); ok {
			write(key, strconv.FormatBool(value))
		}
	}
	writeString := func(key string) {
		if value, ok := custom[key].(string); ok {
			write(key, strings.TrimSpace(value))
		}
	}
	writeStringSlice := func(key string) {
		if values, ok := custom[key].([]string); ok {
			write(key, strings.Join(values, "\x00"))
		}
	}

	write("model", modelToUse)
	write("system_prompt", systemPrompt)
	writeString(MetadataKeyWorkingDir)
	writeString(MetadataKeyResumeSessionID)
	writeString(MetadataKeySandbox)
	writeString(MetadataKeyMode)
	writeString(MetadataKeyProjectConfig)
	writeString(MetadataKeyMCPConfig)
	writeBool(MetadataKeyForce)
	writeBool(MetadataKeyApproveMCPs)
	writeStringSlice(MetadataKeyHeaders)
	writeStringSlice(MetadataKeyPluginDirs)

	return hex.EncodeToString(hash.Sum(nil))
}

func prepareCursorProjectFiles(workingDir, systemPrompt string, opts *llmtypes.CallOptions) (func(), error) {
	cleanups := make([]func(), 0, 3)
	addCleanup := func(cleanup func()) {
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}
	cleanupAll := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	cursorDir := filepath.Join(workingDir, ".cursor")
	if strings.TrimSpace(systemPrompt) != "" {
		rulesDir := filepath.Join(cursorDir, "rules")
		if err := os.MkdirAll(rulesDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create Cursor rules dir: %w", err)
		}
		rulePath := filepath.Join(rulesDir, "mlp-system-"+cursorRandomHex(6)+".mdc")
		content := "---\nalwaysApply: true\n---\n\n" + systemPrompt
		if err := os.WriteFile(rulePath, []byte(content), 0o600); err != nil {
			return nil, fmt.Errorf("failed to write Cursor system rule: %w", err)
		}
		addCleanup(func() {
			_ = os.Remove(rulePath)
			_ = os.Remove(rulesDir)
			_ = os.Remove(cursorDir)
		})
	}

	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if configJSON, ok := opts.Metadata.Custom[MetadataKeyProjectConfig].(string); ok && strings.TrimSpace(configJSON) != "" {
			if !json.Valid([]byte(configJSON)) {
				cleanupAll()
				return nil, fmt.Errorf("cursor project config is not valid JSON")
			}
			cleanup, err := writeCursorRestoredFile(filepath.Join(cursorDir, "cli.json"), []byte(configJSON))
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
		}
		if mcpJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpJSON) != "" {
			if !json.Valid([]byte(mcpJSON)) {
				cleanupAll()
				return nil, fmt.Errorf("cursor MCP config is not valid JSON")
			}
			cleanup, err := writeCursorRestoredFile(filepath.Join(cursorDir, "mcp.json"), []byte(mcpJSON))
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
		}
	}

	return cleanupAll, nil
}

func writeCursorRestoredFile(path string, content []byte) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create Cursor config dir: %w", err)
	}
	previous, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("failed to read existing Cursor config %s: %w", path, readErr)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write Cursor config %s: %w", path, err)
	}
	return func() {
		if existed {
			_ = os.WriteFile(path, previous, 0o600)
		} else {
			_ = os.Remove(path)
			_ = os.Remove(filepath.Dir(path))
		}
	}, nil
}

func releaseCursorInteractiveSession(session *cursorInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	session.lastUsed = time.Now()
	session.idleTimer = time.AfterFunc(cursorInteractiveIdleTimeout(), func() {
		closeCursorPersistentSession(session.ownerSessionID, "idle timeout", logger)
	})
	session.mu.Unlock()
}

func closeCursorPersistentSession(ownerSessionID, reason string, logger interfaces.Logger) {
	cursorPersistentRegistry.Lock()
	session := cursorPersistentRegistry.sessions[ownerSessionID]
	if session == nil {
		cursorPersistentRegistry.Unlock()
		return
	}
	delete(cursorPersistentRegistry.sessions, ownerSessionID)
	cursorPersistentRegistry.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()
	closeCursorSessionLocked(session, reason, logger)
}

func closeCursorSessionLocked(session *cursorInteractiveSession, reason string, logger interfaces.Logger) {
	if session == nil {
		return
	}
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Closing Cursor interactive session %s for owner %s: %s", session.tmuxSessionName, session.ownerSessionID, reason)
	}
	removeCursorPersistentSession(session.ownerSessionID, session)
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = runCursorCommand(closeCtx, nil, "tmux", "send-keys", "-t", session.tmuxSessionName, "C-c")
	_ = killCursorTmuxSession(closeCtx, session.tmuxSessionName)
	if session.cleanupFiles != nil {
		session.cleanupFiles()
		session.cleanupFiles = nil
	}
	unregisterCursorInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
}

func markCursorInteractiveSessionFailedLocked(session *cursorInteractiveSession, err error, logger interfaces.Logger) {
	if session == nil {
		return
	}
	if err != nil {
		session.initErr = err
	}
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Discarding Cursor interactive session %s for owner %s: %v", session.tmuxSessionName, session.ownerSessionID, err)
	}
}

func cleanupFailedCursorInteractiveSession(session *cursorInteractiveSession) {
	if session == nil {
		return
	}
	removeCursorPersistentSession(session.ownerSessionID, session)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killCursorTmuxSession(cleanupCtx, session.tmuxSessionName)
	unregisterCursorInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
	if session.cleanupFiles != nil {
		session.cleanupFiles()
	}
}

func removeCursorPersistentSession(ownerSessionID string, session *cursorInteractiveSession) {
	cursorPersistentRegistry.Lock()
	defer cursorPersistentRegistry.Unlock()
	if current := cursorPersistentRegistry.sessions[ownerSessionID]; current == session {
		delete(cursorPersistentRegistry.sessions, ownerSessionID)
	}
}

func CleanupCursorCLIInteractiveSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	cursorPersistentRegistry.Lock()
	sessions := make([]*cursorInteractiveSession, 0, len(cursorPersistentRegistry.sessions))
	for _, session := range cursorPersistentRegistry.sessions {
		sessions = append(sessions, session)
	}
	cursorPersistentRegistry.sessions = map[string]*cursorInteractiveSession{}
	cursorPersistentRegistry.Unlock()

	var failures []string
	for _, session := range sessions {
		session.mu.Lock()
		if session.idleTimer != nil {
			session.idleTimer.Stop()
			session.idleTimer = nil
		}
		if session.cleanupFiles != nil {
			session.cleanupFiles()
			session.cleanupFiles = nil
		}
		session.mu.Unlock()
		unregisterCursorInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		if err := killCursorTmuxSession(ctx, session.tmuxSessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up Cursor interactive sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func registerCursorInteractiveSession(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if ownerSessionID == "" || tmuxSessionName == "" {
		return
	}
	cursorInteractiveRegistry.Lock()
	defer cursorInteractiveRegistry.Unlock()
	cursorInteractiveRegistry.sessions[ownerSessionID] = tmuxSessionName
}

func unregisterCursorInteractiveSession(ownerSessionID, tmuxSessionName string) {
	cursorInteractiveRegistry.Lock()
	defer cursorInteractiveRegistry.Unlock()
	if current := cursorInteractiveRegistry.sessions[ownerSessionID]; current == tmuxSessionName {
		delete(cursorInteractiveRegistry.sessions, ownerSessionID)
	}
}

func activeCursorInteractiveSession(ownerSessionID string) (string, bool) {
	cursorInteractiveRegistry.RLock()
	defer cursorInteractiveRegistry.RUnlock()
	sessionName, ok := cursorInteractiveRegistry.sessions[strings.TrimSpace(ownerSessionID)]
	return sessionName, ok && strings.TrimSpace(sessionName) != ""
}

func SendCursorInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	sessionName, ok := activeCursorInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Cursor interactive session registered for owner session %s", ownerSessionID)
	}
	return sendCursorInputToTmux(ctx, sessionName, message)
}

func cursorInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyInteractiveSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func cursorPersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyPersistentInteractive].(bool)
	return ok && enabled
}

func cursorResumeSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func cursorWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if dir, ok := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); ok {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			return filepath.Clean(trimmed)
		}
	}
	return ""
}

func cursorAutoApproveWebSearchFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyAutoApproveWebSearch].(bool)
	return enabled
}

func startCursorTmuxSession(ctx context.Context, sessionName string, args []string, env []string, workingDir string) error {
	if workingDir == "" {
		workingDir = cursorMustGetwd()
	}
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	for _, entry := range env {
		if strings.TrimSpace(entry) != "" {
			tmuxArgs = append(tmuxArgs, "-e", entry)
		}
	}
	shellCommand := "cd " + cursorShellQuote(workingDir) + " && exec " + cursorShellJoin(args)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runCursorCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start Cursor interactive session %q: %w", sessionName, err)
	}
	_ = runCursorCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	return nil
}

func waitForCursorPrompt(ctx context.Context, sessionName string) error {
	deadline, cancel := context.WithTimeout(ctx, cursorInteractivePromptWait())
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var trustSubmitted bool
	for {
		select {
		case <-deadline.Done():
			captured, _ := captureCursorPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out waiting for Cursor Agent CLI prompt; latest pane:\n%s", captured)
			}
			return fmt.Errorf("timed out waiting for Cursor Agent CLI prompt")
		case <-ticker.C:
			captured, err := captureCursorPane(deadline, sessionName)
			if err != nil {
				if isCursorTmuxSessionLostError(err) {
					return fmt.Errorf("Cursor Agent CLI tmux session ended while waiting for prompt: %w", err)
				}
				continue
			}
			if hasCursorTrustPrompt(captured) && !trustSubmitted {
				_ = runCursorCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, cursorTrustPromptResponse(captured))
				trustSubmitted = true
				continue
			}
			if hasCursorReadyPrompt(captured) {
				return nil
			}
		}
	}
}

func sendCursorInputToTmux(ctx context.Context, sessionName, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Cursor interactive input is empty")
	}
	bufferName := "mlp-cursor-input-" + cursorRandomHex(6)
	tmp, err := os.CreateTemp("", "cursor-tmux-input-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create Cursor tmux input temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(message); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write Cursor tmux input temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close Cursor tmux input temp file: %w", err)
	}
	if err := runCursorCommand(ctx, nil, "tmux", "load-buffer", "-b", bufferName, tmpPath); err != nil {
		return fmt.Errorf("failed to load Cursor input into tmux buffer: %w", err)
	}
	if err := runCursorCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-p", "-r", "-b", bufferName, "-t", sessionName); err != nil {
		return fmt.Errorf("failed to paste input into Cursor interactive session: %w", err)
	}
	waitForCursorInputDraftVisible(ctx, sessionName, message, 2*time.Second)
	if err := runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return fmt.Errorf("failed to submit input to Cursor interactive session: %w", err)
	}
	return nil
}

func waitForCursorInputDraftVisible(ctx context.Context, sessionName, message string, timeout time.Duration) {
	if strings.TrimSpace(message) == "" {
		return
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			return
		case <-ticker.C:
			captured, err := captureCursorPane(deadline, sessionName)
			if err == nil && cursorPaneShowsPromptDraft(captured, message) {
				return
			}
		}
	}
}

func waitForCursorInteractiveResponse(ctx context.Context, sessionName, baseline, prompt string, historicalAssistantTexts []string, streamChan chan<- llmtypes.StreamChunk, autoApproveWebSearch bool) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var sawActivity bool
	var idleSince time.Time
	var readyWithoutContentSince time.Time
	var submitRetryCount int
	var lastSubmitRetryAt time.Time
	var lastCaptured string
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	var lastWebSearchApprovalAt time.Time
	streamTerminalScreen := cursorInteractiveStreamTmuxScreenEnabled()
	for {
		select {
		case <-ctx.Done():
			captured, _ := captureCursorPane(context.Background(), sessionName)
			return captured, ctx.Err()
		case <-ticker.C:
			captured, err := captureCursorPane(ctx, sessionName)
			if err != nil {
				return "", err
			}
			delta := cursorCapturedAfterBaseline(captured, baseline)
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamCursorTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if autoApproveWebSearch && hasCursorWebSearchApprovalPrompt(captured) {
				if lastWebSearchApprovalAt.IsZero() || time.Since(lastWebSearchApprovalAt) >= 2*time.Second {
					if err := runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "y"); err == nil {
						lastWebSearchApprovalAt = time.Now()
					}
				}
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if hasCursorActivity(captured) {
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if strings.TrimSpace(delta) != "" {
				sawActivity = true
			}
			if !sawActivity || !hasCursorReadyPrompt(captured) {
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if captured != lastCaptured {
				lastCaptured = captured
				idleSince = time.Now()
				continue
			}
			if idleSince.IsZero() {
				idleSince = time.Now()
				continue
			}
			if time.Since(idleSince) >= cursorInteractiveStableWindow {
				content := parseCursorInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
				if strings.TrimSpace(content) == "" {
					if readyWithoutContentSince.IsZero() {
						readyWithoutContentSince = time.Now()
					}
					if cursorPaneShowsPromptDraft(captured, prompt) && submitRetryCount < 3 && time.Since(lastSubmitRetryAt) >= time.Second {
						_ = runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m")
						submitRetryCount++
						lastSubmitRetryAt = time.Now()
						idleSince = time.Time{}
						continue
					}
					if time.Since(readyWithoutContentSince) >= 15*time.Second {
						return captured, fmt.Errorf("Cursor Agent CLI returned to the prompt without visible assistant output; latest pane:\n%s", captured)
					}
					continue
				}
				return captured, nil
			}
		}
	}
}

func parseCursorInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
	delta := cursorCapturedAfterBaseline(captured, baseline)
	text := extractCursorVisibleAssistantText(delta)
	text = stripCursorEchoedUserPrompt(text, echoedUserPrompt)
	text = stripCursorHistoricalAssistantText(text, historicalAssistantTexts)
	return strings.TrimSpace(text)
}

func extractCursorVisibleAssistantText(delta string) string {
	lines := strings.Split(stripCursorANSI(delta), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := normalizeCursorPaneLine(line)
		if trimmed == "" {
			continue
		}
		if isCursorPromptBoundaryLine(trimmed) {
			break
		}
		if isCursorTUILine(trimmed) || isCursorToolStatusLine(trimmed) || isCursorBoxDrawingLine(trimmed) {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func normalizeCursorPaneLine(line string) string {
	line = strings.TrimSpace(stripCursorANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSuffix(line, "│")
	line = strings.TrimSpace(line)
	return strings.TrimSpace(strings.TrimPrefix(line, "• "))
}

func isCursorPromptBoundaryLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return trimmed == ">" ||
		trimmed == "›" ||
		trimmed == "❯" ||
		strings.Contains(lower, "ask (shift+tab") ||
		strings.HasPrefix(lower, "type your message") ||
		strings.Contains(lower, "what can i help") ||
		strings.Contains(lower, "add a follow-up") ||
		strings.Contains(lower, "cursor agent") && strings.Contains(lower, "workspace")
}

func isCursorTUILine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if trimmed == "" {
		return true
	}
	return strings.Contains(lower, "ctrl+") ||
		strings.Contains(lower, "esc to") ||
		strings.Contains(lower, "press enter") ||
		strings.Contains(lower, "run everything") ||
		strings.Contains(lower, "ask (shift+tab") ||
		strings.HasPrefix(lower, "v20") ||
		strings.Contains(lower, "try composer") ||
		strings.Contains(lower, "composer") && strings.Contains(lower, "fast") ||
		strings.Contains(trimmed, " · ") ||
		strings.HasPrefix(trimmed, "→ ") ||
		strings.Contains(lower, "cursor agent") ||
		strings.Contains(lower, "cursor") && strings.Contains(lower, "model") ||
		strings.Contains(lower, "workspace:") ||
		strings.Contains(lower, "mode:") ||
		strings.Contains(lower, "approval") ||
		strings.Contains(lower, "permission") ||
		strings.Contains(lower, "pasted text") ||
		strings.HasPrefix(lower, "use /") ||
		strings.HasPrefix(lower, "add a follow-up")
}

func isCursorToolStatusLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "thinking") ||
		strings.HasPrefix(lower, "working") ||
		strings.HasPrefix(lower, "running") ||
		strings.HasPrefix(lower, "reading") ||
		strings.HasPrefix(lower, "editing") ||
		strings.HasPrefix(lower, "writing") ||
		strings.HasPrefix(lower, "searching") ||
		strings.HasPrefix(lower, "applying") ||
		strings.HasPrefix(lower, "calling ") ||
		strings.HasPrefix(lower, "called ") ||
		strings.HasPrefix(lower, "executing") ||
		strings.Contains(lower, "mcp") && strings.Contains(lower, "tool") ||
		strings.Contains(lower, "shell(") ||
		strings.Contains(lower, `"stdout"`) ||
		strings.Contains(lower, `"stderr"`) ||
		strings.Contains(lower, `"exit_code"`)
}

func isCursorBoxDrawingLine(line string) bool {
	if line == "" {
		return true
	}
	for _, r := range line {
		if strings.ContainsRune("─━▀▄▁▂▃▅▆▇█▌▐▝▜▗▟▘▛▙▚▞▖╭╮╰╯│┌┐└┘├┤┬┴┼╞╪╡╘╧╛╔╗╚╝═║╠╣╦╩╬╌╍╎╏┄┅┆┇┈┉┊┋ ", r) {
			continue
		}
		return false
	}
	return true
}

func stripCursorEchoedUserPrompt(text, prompt string) string {
	text = strings.TrimSpace(text)
	prompt = strings.TrimSpace(prompt)
	if text == "" || prompt == "" {
		return text
	}
	textLines := nonEmptyCursorLines(text)
	promptLines := nonEmptyCursorLines(prompt)
	if len(textLines) == 0 || len(promptLines) == 0 {
		return text
	}
	bestStart := -1
	bestLen := 0
	for start := 0; start < len(textLines) && start < 64; start++ {
		for promptStart := 0; promptStart < len(promptLines); promptStart++ {
			matchLen := 0
			for start+matchLen < len(textLines) &&
				promptStart+matchLen < len(promptLines) &&
				cursorPromptLinesEqual(textLines[start+matchLen], promptLines[promptStart+matchLen]) {
				matchLen++
			}
			if matchLen > bestLen {
				bestStart = start
				bestLen = matchLen
			}
		}
	}
	if bestLen < 2 && !(len(promptLines) == 1 && bestLen == 1) {
		return text
	}
	out := make([]string, 0, len(textLines)-bestLen)
	out = append(out, textLines[:bestStart]...)
	out = append(out, textLines[bestStart+bestLen:]...)
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripCursorHistoricalAssistantText(text string, historicalAssistantTexts []string) string {
	text = strings.TrimSpace(text)
	if text == "" || len(historicalAssistantTexts) == 0 {
		return text
	}
	for i := len(historicalAssistantTexts) - 1; i >= 0; i-- {
		historical := strings.TrimSpace(historicalAssistantTexts[i])
		if historical == "" {
			continue
		}
		if stripped, ok := stripCursorHistoricalPrefix(text, historical); ok {
			text = strings.TrimSpace(stripped)
			i = len(historicalAssistantTexts)
		}
	}
	return text
}

func stripCursorHistoricalPrefix(text, historical string) (string, bool) {
	if text == historical {
		return "", true
	}
	if strings.HasPrefix(text, historical) {
		return text[len(historical):], true
	}
	historicalLines := nonEmptyCursorLines(historical)
	for start := 0; start < len(historicalLines); start++ {
		suffix := strings.Join(historicalLines[start:], "\n")
		if suffix == "" {
			continue
		}
		if text == suffix {
			return "", true
		}
		if strings.HasPrefix(text, suffix) {
			return text[len(suffix):], true
		}
	}
	return text, false
}

func cursorPromptLinesEqual(a, b string) bool {
	a = normalizeCursorPromptLine(a)
	b = normalizeCursorPromptLine(b)
	return a != "" && a == b
}

func normalizeCursorPromptLine(line string) string {
	line = strings.TrimSpace(stripCursorANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, ">")
	line = strings.TrimPrefix(line, "›")
	return strings.TrimSpace(line)
}

func nonEmptyCursorLines(text string) []string {
	rawLines := strings.Split(strings.TrimSpace(text), "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func cursorPaneShowsPromptDraft(captured, prompt string) bool {
	captured = strings.ToLower(stripCursorANSI(captured))
	for _, line := range nonEmptyCursorLines(prompt) {
		line = strings.TrimSpace(stripCursorANSI(line))
		if len([]rune(line)) < 8 {
			continue
		}
		if len([]rune(line)) > 120 {
			line = string([]rune(line)[:120])
		}
		if strings.Contains(captured, strings.ToLower(line)) {
			return true
		}
		return false
	}
	return false
}

func hasCursorReadyPrompt(captured string) bool {
	if hasCursorTrustPrompt(captured) {
		return false
	}
	if hasCursorWebSearchApprovalPrompt(captured) {
		return false
	}
	cleaned := strings.ToLower(stripCursorANSI(captured))
	if !hasCursorReadyMarker(cleaned) {
		return false
	}
	// Cursor leaves historical status lines in the pane after a tool run. Once the
	// follow-up prompt is visible, stale "Running..." text should not keep the turn
	// open forever.
	if hasCursorActivity(captured) && !strings.Contains(cleaned, "add a follow-up") {
		return false
	}
	return true
}

func hasCursorReadyMarker(cleaned string) bool {
	return strings.Contains(cleaned, "type your message") ||
		strings.Contains(cleaned, "ask (shift+tab") ||
		strings.Contains(cleaned, "plan, search, build anything") ||
		strings.Contains(cleaned, "what can i help") ||
		strings.Contains(cleaned, "ask me anything") ||
		strings.Contains(cleaned, "message cursor") ||
		strings.Contains(cleaned, "add a follow-up")
}

func hasCursorTrustPrompt(captured string) bool {
	cleaned := strings.ToLower(stripCursorANSI(captured))
	if strings.Contains(cleaned, "trusting workspace") {
		return false
	}
	return strings.Contains(cleaned, "workspace trust required") ||
		strings.Contains(cleaned, "do you trust the contents of this directory") ||
		strings.Contains(cleaned, "trust") && strings.Contains(cleaned, "workspace") &&
			(strings.Contains(cleaned, "y/n") || strings.Contains(cleaned, "yes") ||
				strings.Contains(cleaned, "[a]") || strings.Contains(cleaned, "[w]"))
}

func hasCursorWebSearchApprovalPrompt(captured string) bool {
	cleaned := strings.ToLower(stripCursorANSI(captured))
	return strings.Contains(cleaned, "allow this web search") ||
		strings.Contains(cleaned, "allow search (y)") ||
		strings.Contains(cleaned, "web search:") && strings.Contains(cleaned, "allow")
}

func cursorTrustPromptResponse(captured string) string {
	cleaned := strings.ToLower(stripCursorANSI(captured))
	if strings.Contains(cleaned, "[a]") || strings.Contains(cleaned, "trust this workspace, but don't enable all mcp servers") {
		return "a"
	}
	return "y"
}

func hasCursorActivity(captured string) bool {
	for _, line := range strings.Split(stripCursorANSI(captured), "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "esc to interrupt") ||
			strings.Contains(lower, "ctrl+c to cancel") ||
			strings.HasPrefix(lower, "thinking") ||
			strings.HasPrefix(lower, "working") ||
			strings.HasPrefix(lower, "running") ||
			strings.HasPrefix(lower, "editing") ||
			strings.HasPrefix(lower, "applying") ||
			strings.HasPrefix(lower, "calling ") {
			return true
		}
	}
	return false
}

func streamCursorTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	snapshot, err := captureCursorPane(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripCursorANSI(snapshot), "\n")
	if strings.TrimSpace(snapshot) == "" || snapshot == *lastTerminalSnapshot {
		return false
	}
	*lastTerminalSnapshot = snapshot
	select {
	case streamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeTerminal, Content: snapshot}:
		return true
	default:
		return false
	}
}

func interruptCursorInteractiveSession(sessionName string, logger interfaces.Logger) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runCursorCommand(interruptCtx, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil && logger != nil {
		logger.Debugf("Failed to send Escape to Cursor interactive session %s: %v", sessionName, err)
	}
}

func resetCursorPaneForTurn(ctx context.Context, sessionName string) {
	_ = runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-l")
	_ = runCursorCommand(ctx, nil, "tmux", "clear-history", "-t", sessionName)
}

func captureCursorPane(ctx context.Context, sessionName string) (string, error) {
	return runCursorCommandOutput(ctx, nil, "tmux", "capture-pane", "-p", "-J", "-S", "-3000", "-t", sessionName)
}

func cursorCapturedAfterBaseline(captured, baseline string) string {
	if baseline != "" {
		if idx := strings.LastIndex(captured, baseline); idx >= 0 {
			return captured[idx+len(baseline):]
		}
	}
	return captured
}

func killCursorTmuxSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return nil
	}
	if err := runCursorCommand(ctx, nil, "tmux", "kill-session", "-t", sessionName); err != nil {
		if isCursorTmuxSessionLostError(err) {
			return nil
		}
		return err
	}
	return nil
}

func isCursorTmuxSessionLostError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no server running") ||
		strings.Contains(lower, "can't find pane") ||
		strings.Contains(lower, "can't find session") ||
		strings.Contains(lower, "no current target")
}

func cursorInteractiveSessionPrefix() string {
	prefix := strings.TrimSpace(os.Getenv(EnvCursorInteractiveSessionPrefix))
	if prefix == "" {
		prefix = "mlp-cursor-cli-int"
	}
	return sanitizeCursorTmuxSessionName(prefix)
}

func newCursorTmuxSessionName() string {
	return sanitizeCursorTmuxSessionName(fmt.Sprintf("%s-%d-%s", cursorInteractiveSessionPrefix(), time.Now().UnixNano(), cursorRandomHex(4)))
}

func cursorInteractiveTimeout() time.Duration {
	return cursorDurationFromEnv(EnvCursorInteractiveTimeoutSeconds, defaultCursorInteractiveTimeout)
}

func cursorInteractiveIdleTimeout() time.Duration {
	return cursorDurationFromEnv(EnvCursorInteractiveIdleTimeoutSeconds, defaultCursorInteractiveIdleTimeout)
}

func cursorInteractivePromptWait() time.Duration {
	return cursorDurationFromEnv(EnvCursorInteractivePromptWaitSeconds, defaultCursorInteractivePromptWait)
}

func cursorInteractiveStreamTmuxScreenEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvCursorInteractiveStreamTmuxScreen))) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func cursorDurationFromEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func runCursorCommand(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	_, err := runCursorCommandOutput(ctx, stdin, name, args...)
	return err
}

func runCursorCommandOutput(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func cursorShellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = cursorShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func cursorShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func cursorMustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func cursorRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sanitizeCursorTmuxSessionName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "cursor"
	}
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func stripCursorANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inEscape {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				inEscape = false
			}
			continue
		}
		if ch == 0x1b {
			inEscape = true
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
