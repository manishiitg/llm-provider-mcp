package opencodecli

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
	defaultOpenCodeInteractiveTimeout     = 30 * time.Minute
	defaultOpenCodeInteractiveIdleTimeout = 20 * time.Minute
	defaultOpenCodeInteractivePromptWait  = 25 * time.Second
	opencodeInteractiveStableWindow       = 1200 * time.Millisecond

	EnvOpenCodeInteractiveSessionPrefix      = "OPENCODE_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvOpenCodeInteractiveTimeoutSeconds     = "OPENCODE_CLI_INTERACTIVE_TIMEOUT_SECONDS"
	EnvOpenCodeInteractiveIdleTimeoutSeconds = "OPENCODE_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvOpenCodeInteractivePromptWaitSeconds  = "OPENCODE_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvOpenCodeInteractiveStreamTmuxScreen   = "OPENCODE_CLI_STREAM_TMUX_SCREEN"
)

type opencodeInteractiveSession struct {
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

var opencodeInteractiveRegistry = struct {
	sync.RWMutex
	sessions map[string]string
}{
	sessions: map[string]string{},
}

var opencodePersistentRegistry = struct {
	sync.Mutex
	sessions map[string]*opencodeInteractiveSession
}{
	sessions: map[string]*opencodeInteractiveSession{},
}

func (c *OpenCodeCLIAdapter) generateContentTmux(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; opencode-cli tmux mode requires tmux")
	}
	if _, err := opencodeBinaryPath(); err != nil {
		return nil, err
	}

	persistent := opencodePersistentInteractiveFromOptions(opts)
	ownerSessionID := opencodeInteractiveSessionIDFromOptions(opts)
	if ownerSessionID == "" {
		ownerSessionID = "opencode-bounded-" + opencodeRandomHex(8)
	}

	callCtx, cancel := context.WithTimeout(ctx, opencodeInteractiveTimeout())
	defer cancel()

	systemPrompt, conversationMessages := splitOpenCodeSystemPrompt(messages)
	historicalAssistantTexts := opencodeAssistantHistory(conversationMessages)
	resume := opencodeResumeSessionIDFromOptions(opts) != ""
	prompt := buildOpenCodePrompt(conversationMessages, resume)
	if strings.TrimSpace(prompt) == "" {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("opencode-cli prompt is empty")
	}

	session, err := c.acquireOpenCodeInteractiveSession(callCtx, ownerSessionID, persistent, opts, systemPrompt)
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
			releaseOpenCodeInteractiveSession(session, c.logger)
		} else {
			closeOpenCodeSessionLocked(session, "bounded turn complete", c.logger)
		}
	}()

	if err := waitForOpenCodePrompt(callCtx, session.tmuxSessionName); err != nil {
		markOpenCodeInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedOpenCodeInteractiveSession(failedSession)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	resetOpenCodePaneForTurn(callCtx, session.tmuxSessionName)
	if err := waitForOpenCodePrompt(callCtx, session.tmuxSessionName); err != nil {
		markOpenCodeInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedOpenCodeInteractiveSession(failedSession)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	baseline, _ := captureOpenCodePane(callCtx, session.tmuxSessionName)
	c.logInfof("Executing OpenCode CLI tmux session: %s", session.tmuxSessionName)
	if err := sendOpenCodeInputToTmux(callCtx, session.tmuxSessionName, prompt); err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	captured, err := waitForOpenCodeInteractiveResponse(callCtx, session.tmuxSessionName, baseline, prompt, historicalAssistantTexts, opts.StreamChan, opencodeAutoApproveWebSearchFromOptions(opts))
	if err != nil {
		if ctx.Err() != nil {
			interruptOpenCodeInteractiveSession(session.tmuxSessionName, c.logger)
		}
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	content := parseOpenCodeInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content: content,
				GenerationInfo: &llmtypes.GenerationInfo{
					Additional: map[string]interface{}{
						"provider":                        "opencode-cli",
						"opencode_mode":                   "tmux",
						"opencode_interactive_session":    session.tmuxSessionName,
						"opencode_persistent_interactive": persistent,
						"opencode_uses_print_json":        false,
						"opencode_working_dir":            session.workingDir,
					},
				},
			},
		},
		Usage: &llmtypes.Usage{},
	}, nil
}

// acquireOpenCodeInteractiveSession returns with session.mu held.
func (c *OpenCodeCLIAdapter) acquireOpenCodeInteractiveSession(ctx context.Context, ownerSessionID string, persistent bool, opts *llmtypes.CallOptions, systemPrompt string) (*opencodeInteractiveSession, error) {
	launchFingerprint := c.opencodeInteractiveLaunchFingerprint(opts, systemPrompt)

	if persistent {
		opencodePersistentRegistry.Lock()
		if existing := opencodePersistentRegistry.sessions[ownerSessionID]; existing != nil {
			existing.mu.Lock()
			if existing.initErr != nil {
				err := existing.initErr
				existing.mu.Unlock()
				opencodePersistentRegistry.Unlock()
				return nil, err
			}
			if existing.launchFingerprint != launchFingerprint {
				existing.mu.Unlock()
				opencodePersistentRegistry.Unlock()
				closeOpenCodePersistentSession(ownerSessionID, "launch configuration changed", c.logger)
				return c.acquireOpenCodeInteractiveSession(ctx, ownerSessionID, persistent, opts, systemPrompt)
			}
			if existing.idleTimer != nil {
				existing.idleTimer.Stop()
				existing.idleTimer = nil
			}
			existing.lastUsed = time.Now()
			opencodePersistentRegistry.Unlock()
			return existing, nil
		}
	}

	now := time.Now()
	session := &opencodeInteractiveSession{
		ownerSessionID:    ownerSessionID,
		tmuxSessionName:   newOpenCodeTmuxSessionName(),
		launchFingerprint: launchFingerprint,
		persistent:        persistent,
		createdAt:         now,
		lastUsed:          now,
	}
	session.mu.Lock()
	if persistent {
		opencodePersistentRegistry.sessions[ownerSessionID] = session
		opencodePersistentRegistry.Unlock()
	}

	args, env, workingDir, cleanupFiles, err := c.buildOpenCodeInteractiveLaunch(opts, systemPrompt)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		if persistent {
			removeOpenCodePersistentSession(ownerSessionID, session)
		}
		return nil, err
	}
	session.workingDir = workingDir
	session.cleanupFiles = cleanupFiles

	if err := startOpenCodeTmuxSession(ctx, session.tmuxSessionName, args, env, workingDir); err != nil {
		session.initErr = err
		if cleanupFiles != nil {
			cleanupFiles()
		}
		session.mu.Unlock()
		if persistent {
			removeOpenCodePersistentSession(ownerSessionID, session)
		}
		return nil, err
	}
	registerOpenCodeInteractiveSession(ownerSessionID, session.tmuxSessionName)
	return session, nil
}

func (c *OpenCodeCLIAdapter) buildOpenCodeInteractiveLaunch(opts *llmtypes.CallOptions, systemPrompt string) ([]string, []string, string, func(), error) {
	workingDir := opencodeWorkingDirFromOptions(opts)
	if workingDir == "" {
		workingDir = opencodeMustGetwd()
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, nil, "", nil, fmt.Errorf("failed to create OpenCode CLI working directory: %w", err)
	}

	cleanupFiles, err := prepareOpenCodeProjectFiles(workingDir, systemPrompt, opts)
	if err != nil {
		return nil, nil, "", nil, err
	}

	modelToUse := resolveOpenCodeCLIModelID(c.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyOpenCodeModel].(string); ok {
			modelToUse = resolveOpenCodeCLIModelID(model)
		}
	}

	binaryPath, err := opencodeBinaryPath()
	if err != nil {
		return nil, nil, "", nil, err
	}

	args := []string{binaryPath}
	if modelToUse != "" {
		args = append(args, "--model", modelToUse)
	}
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && strings.TrimSpace(resumeID) != "" {
			args = append(args, "--session", strings.TrimSpace(resumeID))
		}
		if agent, ok := opts.Metadata.Custom[MetadataKeyAgent].(string); ok && strings.TrimSpace(agent) != "" {
			args = append(args, "--agent", strings.TrimSpace(agent))
		}
	}
	args = append(args, workingDir)

	env := []string{}
	if strings.TrimSpace(c.apiKey) != "" {
		env = append(env, "OPENCODE_API_KEY="+strings.TrimSpace(c.apiKey))
	}
	return args, env, workingDir, cleanupFiles, nil
}

func (c *OpenCodeCLIAdapter) opencodeInteractiveLaunchFingerprint(opts *llmtypes.CallOptions, systemPrompt string) string {
	custom := map[string]interface{}{}
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		custom = opts.Metadata.Custom
	}
	modelToUse := resolveOpenCodeCLIModelID(c.modelID)
	if model, ok := custom[MetadataKeyOpenCodeModel].(string); ok {
		modelToUse = resolveOpenCodeCLIModelID(model)
	}

	hash := sha256.New()
	write := func(key, value string) {
		_, _ = io.WriteString(hash, key)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writeString := func(key string) {
		if value, ok := custom[key].(string); ok {
			write(key, strings.TrimSpace(value))
		}
	}

	write("model", modelToUse)
	write("system_prompt", systemPrompt)
	writeString(MetadataKeyWorkingDir)
	writeString(MetadataKeyResumeSessionID)
	writeString(MetadataKeyAgent)
	writeString(MetadataKeyProjectConfig)
	writeString(MetadataKeyMCPConfig)

	return hex.EncodeToString(hash.Sum(nil))
}

func prepareOpenCodeProjectFiles(workingDir, systemPrompt string, opts *llmtypes.CallOptions) (func(), error) {
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

	if strings.TrimSpace(systemPrompt) != "" {
		agentsPath := filepath.Join(workingDir, "AGENTS.md")
		content := strings.TrimSpace(systemPrompt) + "\n"
		if previous, err := os.ReadFile(agentsPath); err == nil && strings.TrimSpace(string(previous)) != "" {
			content = string(previous) + "\n\n<!-- mcp-agent-builder runtime instructions -->\n\n" + content
		} else if err != nil && !os.IsNotExist(err) {
			cleanupAll()
			return nil, fmt.Errorf("failed to read existing OpenCode AGENTS.md: %w", err)
		}
		cleanup, err := writeOpenCodeRestoredFile(agentsPath, []byte(content))
		if err != nil {
			cleanupAll()
			return nil, fmt.Errorf("failed to write OpenCode AGENTS.md: %w", err)
		}
		addCleanup(cleanup)
	}

	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if configJSON, ok := opts.Metadata.Custom[MetadataKeyProjectConfig].(string); ok && strings.TrimSpace(configJSON) != "" {
			if !json.Valid([]byte(configJSON)) {
				cleanupAll()
				return nil, fmt.Errorf("opencode project config is not valid JSON")
			}
			cleanup, err := writeOpenCodeRestoredFile(filepath.Join(workingDir, "opencode.jsonc"), []byte(configJSON))
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
		}
		if mcpJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpJSON) != "" {
			configJSON, err := buildOpenCodeMCPConfigJSON(mcpJSON)
			if err != nil {
				cleanupAll()
				return nil, err
			}
			cleanup, err := writeOpenCodeRestoredFile(filepath.Join(workingDir, "opencode.jsonc"), configJSON)
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
		}
	}

	return cleanupAll, nil
}

func buildOpenCodeMCPConfigJSON(mcpJSON string) ([]byte, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(mcpJSON), &raw); err != nil {
		return nil, fmt.Errorf("opencode MCP config is not valid JSON: %w", err)
	}
	mcp := raw
	if nested, ok := raw["mcpServers"].(map[string]interface{}); ok {
		mcp = nested
	}
	config := map[string]interface{}{"mcp": mcp}
	return json.MarshalIndent(config, "", "  ")
}

func writeOpenCodeRestoredFile(path string, content []byte) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create OpenCode config dir: %w", err)
	}
	previous, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("failed to read existing OpenCode config %s: %w", path, readErr)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write OpenCode config %s: %w", path, err)
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

func releaseOpenCodeInteractiveSession(session *opencodeInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	session.lastUsed = time.Now()
	session.idleTimer = time.AfterFunc(opencodeInteractiveIdleTimeout(), func() {
		closeOpenCodePersistentSession(session.ownerSessionID, "idle timeout", logger)
	})
	session.mu.Unlock()
}

func closeOpenCodePersistentSession(ownerSessionID, reason string, logger interfaces.Logger) {
	opencodePersistentRegistry.Lock()
	session := opencodePersistentRegistry.sessions[ownerSessionID]
	if session == nil {
		opencodePersistentRegistry.Unlock()
		return
	}
	delete(opencodePersistentRegistry.sessions, ownerSessionID)
	opencodePersistentRegistry.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()
	closeOpenCodeSessionLocked(session, reason, logger)
}

func closeOpenCodeSessionLocked(session *opencodeInteractiveSession, reason string, logger interfaces.Logger) {
	if session == nil {
		return
	}
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Closing OpenCode interactive session %s for owner %s: %s", session.tmuxSessionName, session.ownerSessionID, reason)
	}
	removeOpenCodePersistentSession(session.ownerSessionID, session)
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = runOpenCodeCommand(closeCtx, nil, "tmux", "send-keys", "-t", session.tmuxSessionName, "C-c")
	_ = killOpenCodeTmuxSession(closeCtx, session.tmuxSessionName)
	if session.cleanupFiles != nil {
		session.cleanupFiles()
		session.cleanupFiles = nil
	}
	unregisterOpenCodeInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
}

func markOpenCodeInteractiveSessionFailedLocked(session *opencodeInteractiveSession, err error, logger interfaces.Logger) {
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
		logger.Debugf("Discarding OpenCode interactive session %s for owner %s: %v", session.tmuxSessionName, session.ownerSessionID, err)
	}
}

func cleanupFailedOpenCodeInteractiveSession(session *opencodeInteractiveSession) {
	if session == nil {
		return
	}
	removeOpenCodePersistentSession(session.ownerSessionID, session)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killOpenCodeTmuxSession(cleanupCtx, session.tmuxSessionName)
	unregisterOpenCodeInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
	if session.cleanupFiles != nil {
		session.cleanupFiles()
	}
}

func removeOpenCodePersistentSession(ownerSessionID string, session *opencodeInteractiveSession) {
	opencodePersistentRegistry.Lock()
	defer opencodePersistentRegistry.Unlock()
	if current := opencodePersistentRegistry.sessions[ownerSessionID]; current == session {
		delete(opencodePersistentRegistry.sessions, ownerSessionID)
	}
}

func CleanupOpenCodeCLIInteractiveSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	opencodePersistentRegistry.Lock()
	sessions := make([]*opencodeInteractiveSession, 0, len(opencodePersistentRegistry.sessions))
	for _, session := range opencodePersistentRegistry.sessions {
		sessions = append(sessions, session)
	}
	opencodePersistentRegistry.sessions = map[string]*opencodeInteractiveSession{}
	opencodePersistentRegistry.Unlock()

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
		unregisterOpenCodeInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		if err := killOpenCodeTmuxSession(ctx, session.tmuxSessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if listed, err := listOpenCodeTmuxSessions(ctx); err == nil {
		for _, sessionName := range listed {
			if err := killOpenCodeTmuxSession(ctx, sessionName); err != nil {
				failures = append(failures, err.Error())
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up OpenCode interactive sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func registerOpenCodeInteractiveSession(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if ownerSessionID == "" || tmuxSessionName == "" {
		return
	}
	opencodeInteractiveRegistry.Lock()
	defer opencodeInteractiveRegistry.Unlock()
	opencodeInteractiveRegistry.sessions[ownerSessionID] = tmuxSessionName
}

func unregisterOpenCodeInteractiveSession(ownerSessionID, tmuxSessionName string) {
	opencodeInteractiveRegistry.Lock()
	defer opencodeInteractiveRegistry.Unlock()
	if current := opencodeInteractiveRegistry.sessions[ownerSessionID]; current == tmuxSessionName {
		delete(opencodeInteractiveRegistry.sessions, ownerSessionID)
	}
}

func activeOpenCodeInteractiveSession(ownerSessionID string) (string, bool) {
	opencodeInteractiveRegistry.RLock()
	defer opencodeInteractiveRegistry.RUnlock()
	sessionName, ok := opencodeInteractiveRegistry.sessions[strings.TrimSpace(ownerSessionID)]
	return sessionName, ok && strings.TrimSpace(sessionName) != ""
}

func SendOpenCodeInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	sessionName, ok := activeOpenCodeInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active OpenCode interactive session registered for owner session %s", ownerSessionID)
	}
	return sendOpenCodeInputToTmux(ctx, sessionName, message)
}

func opencodeInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyInteractiveSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func opencodePersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyPersistentInteractive].(bool)
	return ok && enabled
}

func opencodeResumeSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func opencodeWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
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

func opencodeAutoApproveWebSearchFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyAutoApproveWebSearch].(bool)
	return enabled
}

func startOpenCodeTmuxSession(ctx context.Context, sessionName string, args []string, env []string, workingDir string) error {
	if workingDir == "" {
		workingDir = opencodeMustGetwd()
	}
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	for _, entry := range env {
		if strings.TrimSpace(entry) != "" {
			tmuxArgs = append(tmuxArgs, "-e", entry)
		}
	}
	shellCommand := "cd " + opencodeShellQuote(workingDir) + " && exec " + opencodeShellJoin(args)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runOpenCodeCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start OpenCode interactive session %q: %w", sessionName, err)
	}
	_ = runOpenCodeCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	return nil
}

func waitForOpenCodePrompt(ctx context.Context, sessionName string) error {
	deadline, cancel := context.WithTimeout(ctx, opencodeInteractivePromptWait())
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			captured, _ := captureOpenCodePane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out waiting for OpenCode CLI prompt; latest pane:\n%s", captured)
			}
			return fmt.Errorf("timed out waiting for OpenCode CLI prompt")
		case <-ticker.C:
			captured, err := captureOpenCodePane(deadline, sessionName)
			if err != nil {
				continue
			}
			if hasOpenCodeReadyPrompt(captured) {
				return nil
			}
		}
	}
}

func sendOpenCodeInputToTmux(ctx context.Context, sessionName, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("OpenCode interactive input is empty")
	}
	bufferName := "mlp-opencode-input-" + opencodeRandomHex(6)
	tmp, err := os.CreateTemp("", "opencode-tmux-input-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create OpenCode tmux input temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(message); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write OpenCode tmux input temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close OpenCode tmux input temp file: %w", err)
	}
	if err := runOpenCodeCommand(ctx, nil, "tmux", "load-buffer", "-b", bufferName, tmpPath); err != nil {
		return fmt.Errorf("failed to load OpenCode input into tmux buffer: %w", err)
	}
	if err := runOpenCodeCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-p", "-r", "-b", bufferName, "-t", sessionName); err != nil {
		return fmt.Errorf("failed to paste input into OpenCode interactive session: %w", err)
	}
	waitForOpenCodeInputDraftVisible(ctx, sessionName, message, 2*time.Second)
	if err := runOpenCodeCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return fmt.Errorf("failed to submit input to OpenCode interactive session: %w", err)
	}
	return nil
}

func waitForOpenCodeInputDraftVisible(ctx context.Context, sessionName, message string, timeout time.Duration) {
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
			captured, err := captureOpenCodePane(deadline, sessionName)
			if err == nil && opencodePaneShowsPromptDraft(captured, message) {
				return
			}
		}
	}
}

func waitForOpenCodeInteractiveResponse(ctx context.Context, sessionName, baseline, prompt string, historicalAssistantTexts []string, streamChan chan<- llmtypes.StreamChunk, autoApproveWebSearch bool) (string, error) {
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
	streamTerminalScreen := opencodeInteractiveStreamTmuxScreenEnabled()
	for {
		select {
		case <-ctx.Done():
			captured, _ := captureOpenCodePane(context.Background(), sessionName)
			return captured, ctx.Err()
		case <-ticker.C:
			captured, err := captureOpenCodePane(ctx, sessionName)
			if err != nil {
				return "", err
			}
			delta := opencodeCapturedAfterBaseline(captured, baseline)
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamOpenCodeTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if autoApproveWebSearch && hasOpenCodeWebSearchApprovalPrompt(captured) {
				if lastWebSearchApprovalAt.IsZero() || time.Since(lastWebSearchApprovalAt) >= 2*time.Second {
					if err := runOpenCodeCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "y"); err == nil {
						lastWebSearchApprovalAt = time.Now()
					}
				}
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if hasOpenCodeActivity(captured) {
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if strings.TrimSpace(delta) != "" {
				sawActivity = true
			}
			if !sawActivity || !hasOpenCodeReadyPrompt(captured) {
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
			if time.Since(idleSince) >= opencodeInteractiveStableWindow {
				content := parseOpenCodeInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
				if strings.TrimSpace(content) == "" {
					if readyWithoutContentSince.IsZero() {
						readyWithoutContentSince = time.Now()
					}
					if opencodePaneShowsPromptDraft(captured, prompt) && submitRetryCount < 3 && time.Since(lastSubmitRetryAt) >= time.Second {
						_ = runOpenCodeCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m")
						submitRetryCount++
						lastSubmitRetryAt = time.Now()
						idleSince = time.Time{}
						continue
					}
					if time.Since(readyWithoutContentSince) >= 15*time.Second {
						return captured, fmt.Errorf("OpenCode CLI returned to the prompt without visible assistant output; latest pane:\n%s", captured)
					}
					continue
				}
				return captured, nil
			}
		}
	}
}

func parseOpenCodeInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
	delta := opencodeCapturedAfterBaseline(captured, baseline)
	text := extractOpenCodeVisibleAssistantText(delta)
	text = stripOpenCodeEchoedUserPrompt(text, echoedUserPrompt)
	text = stripOpenCodeHistoricalAssistantText(text, historicalAssistantTexts)
	if isOpenCodeLikelyQueuedUserEcho(text) {
		return ""
	}
	return strings.TrimSpace(text)
}

func isOpenCodeLikelyQueuedUserEcho(text string) bool {
	lines := nonEmptyOpenCodeLines(text)
	if len(lines) == 0 {
		return false
	}
	lower := strings.ToLower(strings.Join(lines, "\n"))
	return strings.Contains(lower, "pre-validation failed") &&
		strings.Contains(lower, "checks:") &&
		(strings.Contains(lower, "fix the specific issues") ||
			strings.Contains(lower, "validation failed") ||
			strings.Contains(lower, "must exist"))
}

func extractOpenCodeVisibleAssistantText(delta string) string {
	lines := strings.Split(stripOpenCodeANSI(delta), "\n")
	out := make([]string, 0, len(lines))
	capturingAssistant := false
	capturedAssistantBlock := false
	for _, line := range lines {
		trimmed := normalizeOpenCodePaneLine(line)
		if trimmed == "" {
			continue
		}
		if isOpenCodeAssistantStartLine(trimmed) {
			capturingAssistant = true
			capturedAssistantBlock = true
			continue
		}
		if capturingAssistant && isOpenCodeAssistantEndLine(trimmed) {
			capturingAssistant = false
			continue
		}
		if isOpenCodePromptBoundaryLine(trimmed) {
			break
		}
		if isOpenCodeTUILine(trimmed) || isOpenCodeToolStatusLine(trimmed) || isOpenCodeBoxDrawingLine(trimmed) {
			continue
		}
		if capturedAssistantBlock && !capturingAssistant {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func normalizeOpenCodePaneLine(line string) string {
	line = strings.TrimSpace(stripOpenCodeANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSuffix(line, "│")
	line = strings.TrimSpace(line)
	return strings.TrimSpace(strings.TrimPrefix(line, "• "))
}

func isOpenCodePromptBoundaryLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return trimmed == ">" ||
		trimmed == "›" ||
		trimmed == "❯" ||
		strings.Contains(lower, "ask anything") ||
		strings.Contains(lower, "ctrl+p commands") && strings.Contains(lower, "opencode")
}

func isOpenCodeAssistantStartLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.Contains(lower, "thought for") ||
		strings.HasPrefix(lower, "thinking") ||
		strings.HasPrefix(lower, "working")
}

func isOpenCodeAssistantEndLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "▣") ||
		strings.Contains(lower, "context") ||
		strings.Contains(lower, "tokens") ||
		strings.Contains(lower, "used") ||
		strings.Contains(lower, "spent") ||
		strings.Contains(lower, "lsp") ||
		strings.Contains(lower, "ctrl+p commands")
}

func isOpenCodeTUILine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if trimmed == "" {
		return true
	}
	return strings.Contains(lower, "ctrl+") ||
		strings.Contains(lower, "esc to") ||
		strings.Contains(lower, "press enter") ||
		strings.Contains(lower, "ask anything") ||
		strings.Contains(trimmed, " · ") ||
		strings.HasPrefix(trimmed, "→ ") ||
		strings.HasPrefix(trimmed, "▣") ||
		strings.Contains(lower, "new session -") ||
		strings.Contains(lower, "opencode") && strings.Contains(lower, "zen") ||
		strings.Contains(lower, "opencode includes free models") ||
		strings.Contains(lower, "connect from 75+ providers") ||
		strings.Contains(lower, "connect provider") ||
		strings.Contains(lower, "getting started") ||
		strings.Contains(lower, "context") ||
		strings.Contains(lower, "tokens") ||
		strings.Contains(lower, "used") ||
		strings.Contains(lower, "spent") ||
		strings.Contains(lower, "lsp") ||
		strings.Contains(lower, "lsps are disabled") ||
		strings.Contains(lower, "approval") ||
		strings.Contains(lower, "permission") ||
		strings.Contains(lower, "pasted text")
}

func isOpenCodeToolStatusLine(line string) bool {
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

func isOpenCodeBoxDrawingLine(line string) bool {
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

func stripOpenCodeEchoedUserPrompt(text, prompt string) string {
	text = strings.TrimSpace(text)
	prompt = strings.TrimSpace(prompt)
	if text == "" || prompt == "" {
		return text
	}
	textLines := nonEmptyOpenCodeLines(text)
	promptLines := nonEmptyOpenCodeLines(prompt)
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
				opencodePromptLinesEqual(textLines[start+matchLen], promptLines[promptStart+matchLen]) {
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

func stripOpenCodeHistoricalAssistantText(text string, historicalAssistantTexts []string) string {
	text = strings.TrimSpace(text)
	if text == "" || len(historicalAssistantTexts) == 0 {
		return text
	}
	for i := len(historicalAssistantTexts) - 1; i >= 0; i-- {
		historical := strings.TrimSpace(historicalAssistantTexts[i])
		if historical == "" {
			continue
		}
		if stripped, ok := stripOpenCodeHistoricalPrefix(text, historical); ok {
			text = strings.TrimSpace(stripped)
			i = len(historicalAssistantTexts)
		}
	}
	return text
}

func stripOpenCodeHistoricalPrefix(text, historical string) (string, bool) {
	if text == historical {
		return "", true
	}
	if strings.HasPrefix(text, historical) {
		return text[len(historical):], true
	}
	historicalLines := nonEmptyOpenCodeLines(historical)
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

func opencodePromptLinesEqual(a, b string) bool {
	a = normalizeOpenCodePromptLine(a)
	b = normalizeOpenCodePromptLine(b)
	return a != "" && a == b
}

func normalizeOpenCodePromptLine(line string) string {
	line = strings.TrimSpace(stripOpenCodeANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, ">")
	line = strings.TrimPrefix(line, "›")
	return strings.TrimSpace(line)
}

func nonEmptyOpenCodeLines(text string) []string {
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

func opencodePaneShowsPromptDraft(captured, prompt string) bool {
	captured = strings.ToLower(stripOpenCodeANSI(captured))
	for _, line := range nonEmptyOpenCodeLines(prompt) {
		line = strings.TrimSpace(stripOpenCodeANSI(line))
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

func hasOpenCodeReadyPrompt(captured string) bool {
	if hasOpenCodeWebSearchApprovalPrompt(captured) {
		return false
	}
	cleaned := strings.ToLower(stripOpenCodeANSI(captured))
	if !hasOpenCodeReadyMarker(cleaned) {
		return false
	}
	// OpenCode leaves historical status lines in the pane after a tool run. Once the
	// follow-up prompt is visible, stale "Running..." text should not keep the turn
	// open forever.
	if hasOpenCodeActivity(captured) && !strings.Contains(cleaned, "add a follow-up") {
		return false
	}
	return true
}

func hasOpenCodeReadyMarker(cleaned string) bool {
	return strings.Contains(cleaned, "ask anything") ||
		strings.Contains(cleaned, "ctrl+p commands") && strings.Contains(cleaned, "opencode")
}

func hasOpenCodeWebSearchApprovalPrompt(captured string) bool {
	cleaned := strings.ToLower(stripOpenCodeANSI(captured))
	return strings.Contains(cleaned, "allow this web search") ||
		strings.Contains(cleaned, "allow search (y)") ||
		strings.Contains(cleaned, "web search:") && strings.Contains(cleaned, "allow")
}

func hasOpenCodeActivity(captured string) bool {
	for _, line := range strings.Split(stripOpenCodeANSI(captured), "\n") {
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

func streamOpenCodeTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	snapshot, err := captureOpenCodePane(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripOpenCodeANSI(snapshot), "\n")
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

func interruptOpenCodeInteractiveSession(sessionName string, logger interfaces.Logger) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runOpenCodeCommand(interruptCtx, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil && logger != nil {
		logger.Debugf("Failed to send Escape to OpenCode interactive session %s: %v", sessionName, err)
	}
}

func resetOpenCodePaneForTurn(ctx context.Context, sessionName string) {
	_ = runOpenCodeCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-l")
	_ = runOpenCodeCommand(ctx, nil, "tmux", "clear-history", "-t", sessionName)
}

func captureOpenCodePane(ctx context.Context, sessionName string) (string, error) {
	return runOpenCodeCommandOutput(ctx, nil, "tmux", "capture-pane", "-p", "-J", "-S", "-3000", "-t", sessionName)
}

func opencodeCapturedAfterBaseline(captured, baseline string) string {
	if baseline != "" {
		if idx := strings.LastIndex(captured, baseline); idx >= 0 {
			return captured[idx+len(baseline):]
		}
	}
	return captured
}

func listOpenCodeTmuxSessions(ctx context.Context) ([]string, error) {
	out, err := runOpenCodeCommandOutput(ctx, nil, "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") {
			return nil, nil
		}
		return nil, err
	}
	prefix := opencodeInteractiveSessionPrefix()
	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

func killOpenCodeTmuxSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return nil
	}
	if err := runOpenCodeCommand(ctx, nil, "tmux", "kill-session", "-t", sessionName); err != nil {
		if strings.Contains(err.Error(), "can't find session") || strings.Contains(err.Error(), "no server running") {
			return nil
		}
		return err
	}
	return nil
}

func opencodeInteractiveSessionPrefix() string {
	prefix := strings.TrimSpace(os.Getenv(EnvOpenCodeInteractiveSessionPrefix))
	if prefix == "" {
		prefix = "mlp-opencode-cli-int"
	}
	return sanitizeOpenCodeTmuxSessionName(prefix)
}

func newOpenCodeTmuxSessionName() string {
	return sanitizeOpenCodeTmuxSessionName(fmt.Sprintf("%s-%d-%s", opencodeInteractiveSessionPrefix(), time.Now().UnixNano(), opencodeRandomHex(4)))
}

func opencodeInteractiveTimeout() time.Duration {
	return opencodeDurationFromEnv(EnvOpenCodeInteractiveTimeoutSeconds, defaultOpenCodeInteractiveTimeout)
}

func opencodeInteractiveIdleTimeout() time.Duration {
	return opencodeDurationFromEnv(EnvOpenCodeInteractiveIdleTimeoutSeconds, defaultOpenCodeInteractiveIdleTimeout)
}

func opencodeInteractivePromptWait() time.Duration {
	return opencodeDurationFromEnv(EnvOpenCodeInteractivePromptWaitSeconds, defaultOpenCodeInteractivePromptWait)
}

func opencodeInteractiveStreamTmuxScreenEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvOpenCodeInteractiveStreamTmuxScreen))) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func opencodeDurationFromEnv(key string, fallback time.Duration) time.Duration {
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

func runOpenCodeCommand(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	_, err := runOpenCodeCommandOutput(ctx, stdin, name, args...)
	return err
}

func runOpenCodeCommandOutput(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
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

func opencodeBinaryPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("OPENCODE_BIN")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("OPENCODE_BIN points to a missing or invalid executable: %s", configured)
	}
	if path, err := exec.LookPath("opencode"); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		for _, candidate := range []string{
			filepath.Join(home, ".opencode", "bin", "opencode"),
			filepath.Join(home, ".cache", "opencode", "bin", "opencode"),
		} {
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("opencode not found in PATH. Install OpenCode CLI or set OPENCODE_BIN to the opencode executable")
}

func opencodeShellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = opencodeShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func opencodeShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func opencodeMustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func opencodeRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sanitizeOpenCodeTmuxSessionName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "opencode"
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

func stripOpenCodeANSI(s string) string {
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
