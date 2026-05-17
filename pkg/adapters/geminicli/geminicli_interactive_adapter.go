package geminicli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/manishiitg/multi-llm-provider-go/internal/shelllaunch"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	defaultGeminiInteractiveTimeout     = 30 * time.Minute
	defaultGeminiInteractiveIdleTimeout = 20 * time.Minute
	defaultGeminiInteractivePromptWait  = 20 * time.Second
	geminiInteractiveStableWindow       = 1200 * time.Millisecond

	EnvGeminiInteractiveSessionPrefix      = "GEMINI_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvGeminiInteractiveTimeoutSeconds     = "GEMINI_CLI_INTERACTIVE_TIMEOUT_SECONDS"
	EnvGeminiInteractiveIdleTimeoutSeconds = "GEMINI_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvGeminiInteractivePromptWaitSeconds  = "GEMINI_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvGeminiInteractiveStreamTmuxScreen   = "GEMINI_CLI_STREAM_TMUX_SCREEN"
)

type geminiInteractiveSession struct {
	ownerSessionID       string
	tmuxSessionName      string
	projectDir           string
	projectDirID         string
	systemPromptTempFile string
	launchFingerprint    string
	idleTimer            *time.Timer
	initErr              error
	createdAt            time.Time
	lastUsed             time.Time
	mu                   sync.Mutex
	liveMu               sync.Mutex
	pendingLiveInputs    []string
}

var geminiInteractiveRegistry = struct {
	sync.RWMutex
	sessions map[string]string
}{
	sessions: map[string]string{},
}

var geminiPersistentRegistry = struct {
	sync.Mutex
	sessions map[string]*geminiInteractiveSession
}{
	sessions: map[string]*geminiInteractiveSession{},
}

func (g *GeminiCLIAdapter) generateContentInteractive(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; gemini-cli interactive mode requires tmux")
	}
	if _, err := exec.LookPath("gemini"); err != nil {
		return nil, fmt.Errorf("gemini cli not found in PATH. Please install it first (npm install -g @google/gemini-cli or see https://github.com/google-gemini/gemini-cli)")
	}

	ownerSessionID := geminiInteractiveSessionIDFromOptions(opts)
	if ownerSessionID == "" {
		return nil, fmt.Errorf("gemini-cli interactive mode requires an owner session ID")
	}

	callCtx, cancel := context.WithTimeout(ctx, geminiInteractiveTimeout())
	defer cancel()

	systemPrompt, conversationMessages := splitGeminiSystemPrompt(messages)
	historicalAssistantTexts := geminiAssistantHistory(conversationMessages)
	session, err := g.acquireGeminiInteractiveSession(callCtx, ownerSessionID, opts, systemPrompt)
	if err != nil {
		return nil, err
	}
	releaseSession := true
	defer func() {
		if releaseSession && session != nil {
			releaseGeminiInteractiveSession(session, g.logger)
		}
	}()

	if err := waitForGeminiPrompt(callCtx, session.tmuxSessionName); err != nil {
		markGeminiInteractiveSessionFailedLocked(session, err, g.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedGeminiInteractiveSession(failedSession)
		return nil, err
	}
	resetGeminiPaneForTurn(callCtx, session.tmuxSessionName)
	if err := waitForGeminiPrompt(callCtx, session.tmuxSessionName); err != nil {
		markGeminiInteractiveSessionFailedLocked(session, err, g.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedGeminiInteractiveSession(failedSession)
		return nil, err
	}

	prompt := buildGeminiInteractivePrompt(conversationMessages)
	baseline, _ := captureGeminiPane(callCtx, session.tmuxSessionName)
	g.logger.Infof("Executing Gemini CLI interactive tmux session: %s", session.tmuxSessionName)
	if err := sendGeminiPromptToTmux(callCtx, session.tmuxSessionName, prompt); err != nil {
		return nil, err
	}

	captured, err := waitForGeminiInteractiveResponse(callCtx, session, baseline, opts.StreamChan)
	if err != nil {
		if ctx.Err() != nil {
			interruptGeminiInteractiveSession(session.tmuxSessionName, g.logger)
		}
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	content := parseGeminiInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content: content,
				GenerationInfo: &llmtypes.GenerationInfo{
					Additional: map[string]interface{}{
						"provider":                      "gemini-cli",
						"gemini_mode":                   "interactive",
						"gemini_interactive_session":    session.tmuxSessionName,
						"gemini_persistent_interactive": true,
						"gemini_uses_stream_json":       false,
						"gemini_project_dir_id":         session.projectDirID,
					},
				},
			},
		},
		Usage: &llmtypes.Usage{},
	}, nil
}

// acquireGeminiInteractiveSession returns with session.mu held. The caller must
// either releaseGeminiInteractiveSession on normal completion or mark, unlock,
// and clean up the session on a startup/ready-prompt failure.
func (g *GeminiCLIAdapter) acquireGeminiInteractiveSession(ctx context.Context, ownerSessionID string, opts *llmtypes.CallOptions, systemPrompt string) (*geminiInteractiveSession, error) {
	launchFingerprint := g.geminiInteractiveLaunchFingerprint(opts, systemPrompt)

	geminiPersistentRegistry.Lock()
	if existing := geminiPersistentRegistry.sessions[ownerSessionID]; existing != nil {
		existing.mu.Lock()
		if existing.initErr != nil {
			err := existing.initErr
			existing.mu.Unlock()
			geminiPersistentRegistry.Unlock()
			return nil, err
		}
		if existing.launchFingerprint != launchFingerprint {
			existing.mu.Unlock()
			geminiPersistentRegistry.Unlock()
			closeGeminiPersistentSession(ownerSessionID, "launch configuration changed", g.logger)
			return g.acquireGeminiInteractiveSession(ctx, ownerSessionID, opts, systemPrompt)
		}
		if existing.idleTimer != nil {
			existing.idleTimer.Stop()
			existing.idleTimer = nil
		}
		existing.lastUsed = time.Now()
		geminiPersistentRegistry.Unlock()
		return existing, nil
	}

	now := time.Now()
	session := &geminiInteractiveSession{
		ownerSessionID:    ownerSessionID,
		tmuxSessionName:   newGeminiTmuxSessionName(),
		launchFingerprint: launchFingerprint,
		createdAt:         now,
		lastUsed:          now,
	}
	session.mu.Lock()
	geminiPersistentRegistry.sessions[ownerSessionID] = session
	geminiPersistentRegistry.Unlock()

	args, env, projectDir, projectDirID, systemPromptTempFile, err := g.buildGeminiInteractiveLaunch(ownerSessionID, opts, systemPrompt)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		removeGeminiPersistentSession(ownerSessionID, session)
		return nil, err
	}
	session.projectDir = projectDir
	session.projectDirID = projectDirID
	session.systemPromptTempFile = systemPromptTempFile

	if err := startGeminiTmuxSession(ctx, session.tmuxSessionName, args, env, projectDir); err != nil {
		session.initErr = err
		if systemPromptTempFile != "" {
			_ = os.Remove(systemPromptTempFile)
		}
		session.mu.Unlock()
		removeGeminiPersistentSession(ownerSessionID, session)
		return nil, err
	}
	registerGeminiInteractiveSession(ownerSessionID, session.tmuxSessionName)
	return session, nil
}

func (g *GeminiCLIAdapter) buildGeminiInteractiveLaunch(ownerSessionID string, opts *llmtypes.CallOptions, systemPrompt string) ([]string, []string, string, string, string, error) {
	modelToUse := resolveGeminiCLIModelID(g.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyGeminiModel].(string); ok && model != "" {
			modelToUse = resolveGeminiCLIModelID(model)
		}
	}
	if modelToUse == "" || modelToUse == "gemini-cli" {
		modelToUse = "auto"
	}

	args := []string{"gemini", "--model", modelToUse}
	appendGeminiPolicyArgs(&args, opts)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mode, ok := opts.Metadata.Custom[MetadataKeyApprovalMode].(string); ok && strings.TrimSpace(mode) != "" {
			args = append(args, "--approval-mode", mode)
		}
		if allowedTools, ok := opts.Metadata.Custom[MetadataKeyAllowedTools].(string); ok && strings.TrimSpace(allowedTools) != "" {
			for _, tool := range strings.Split(allowedTools, ",") {
				if tool = strings.TrimSpace(tool); tool != "" {
					args = append(args, "--allowed-tools", tool)
				}
			}
		}
		if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && strings.TrimSpace(resumeID) != "" {
			args = append(args, "--resume", strings.TrimSpace(resumeID))
		}
	}

	projectDir, projectDirID, err := prepareGeminiInteractiveProjectDir(ownerSessionID, opts)
	if err != nil {
		return nil, nil, "", "", "", err
	}

	systemPromptFile := ""
	systemPromptTempFile := ""
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if spf, ok := opts.Metadata.Custom[MetadataKeySystemPromptFile].(string); ok && strings.TrimSpace(spf) != "" {
			systemPromptFile = strings.TrimSpace(spf)
		}
	}
	if systemPromptFile == "" && strings.TrimSpace(systemPrompt) != "" {
		tmpFile, err := os.CreateTemp("", "gemini-interactive-system-*.md")
		if err != nil {
			return nil, nil, "", "", "", fmt.Errorf("failed to create temp file for Gemini interactive system prompt: %w", err)
		}
		systemPromptTempFile = tmpFile.Name()
		if _, err := tmpFile.WriteString(systemPrompt); err != nil {
			tmpFile.Close()
			_ = os.Remove(systemPromptTempFile)
			return nil, nil, "", "", "", fmt.Errorf("failed to write Gemini interactive system prompt: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			_ = os.Remove(systemPromptTempFile)
			return nil, nil, "", "", "", fmt.Errorf("failed to close Gemini interactive system prompt: %w", err)
		}
		systemPromptFile = systemPromptTempFile
	}

	env := []string{"GEMINI_CLI_TRUST_WORKSPACE=true", "GEMINI_PROJECT_DIR=" + projectDir}
	if g.apiKey != "" {
		env = append(env, "GEMINI_API_KEY="+g.apiKey)
	}
	if systemPromptFile != "" {
		env = append(env, "GEMINI_SYSTEM_MD="+systemPromptFile)
	}
	return args, env, projectDir, projectDirID, systemPromptTempFile, nil
}

func (g *GeminiCLIAdapter) geminiInteractiveLaunchFingerprint(opts *llmtypes.CallOptions, systemPrompt string) string {
	modelToUse := resolveGeminiCLIModelID(g.modelID)
	custom := map[string]interface{}{}
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		custom = opts.Metadata.Custom
		if model, ok := custom[MetadataKeyGeminiModel].(string); ok && strings.TrimSpace(model) != "" {
			modelToUse = resolveGeminiCLIModelID(model)
		}
	}
	if modelToUse == "" || modelToUse == "gemini-cli" {
		modelToUse = "auto"
	}

	hash := sha256.New()
	write := func(key, value string) {
		_, _ = io.WriteString(hash, key)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, value)
		_, _ = io.WriteString(hash, "\x00")
	}
	writeFile := func(key, path string) {
		path = strings.TrimSpace(path)
		write(key+".path", path)
		if path == "" {
			return
		}
		content, err := os.ReadFile(path)
		if err != nil {
			write(key+".read_error", err.Error())
			return
		}
		_, _ = io.WriteString(hash, key+".content")
		_, _ = io.WriteString(hash, "\x00")
		_, _ = hash.Write(content)
		_, _ = io.WriteString(hash, "\x00")
	}
	writeStringOption := func(key string) {
		if value, ok := custom[key].(string); ok {
			write(key, strings.TrimSpace(value))
		}
	}

	write("model", modelToUse)
	// Persistent interactive sessions pin the system prompt at session startup.
	// Do not include the full prompt text in the reuse fingerprint: app-level
	// prompts can contain per-turn dynamic context such as secret ordering or
	// timestamps, and restarting the TUI would break native multi-turn memory.
	write("system_prompt_present", strconv.FormatBool(strings.TrimSpace(systemPrompt) != ""))
	writeStringOption(MetadataKeyApprovalMode)
	writeStringOption(MetadataKeyAllowedTools)
	writeStringOption(MetadataKeyProjectSettings)
	writeStringOption(MetadataKeyWorkingDir)
	writeStringOption(MetadataKeyProjectDirID)
	writeStringOption(MetadataKeyResumeSessionID)
	writeFile(MetadataKeySystemPromptFile, stringOptionValue(custom, MetadataKeySystemPromptFile))
	writeFile(MetadataKeyPolicyPath, stringOptionValue(custom, MetadataKeyPolicyPath))
	writeFile(MetadataKeyAdminPolicyPath, stringOptionValue(custom, MetadataKeyAdminPolicyPath))

	return hex.EncodeToString(hash.Sum(nil))
}

func stringOptionValue(custom map[string]interface{}, key string) string {
	if value, ok := custom[key].(string); ok {
		return value
	}
	return ""
}

func prepareGeminiInteractiveProjectDir(ownerSessionID string, opts *llmtypes.CallOptions) (string, string, error) {
	projectDirID := ""
	settingsJSON := ""
	workingDir := geminiWorkingDirFromOptions(opts)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if id, ok := opts.Metadata.Custom[MetadataKeyProjectDirID].(string); ok && strings.TrimSpace(id) != "" {
			projectDirID = strings.TrimSpace(id)
		}
		if settings, ok := opts.Metadata.Custom[MetadataKeyProjectSettings].(string); ok && strings.TrimSpace(settings) != "" {
			settingsJSON = settings
		}
	}
	if projectDirID == "" {
		projectDirID = "interactive-" + sanitizeGeminiTmuxSessionName(ownerSessionID)
	}
	projectDir := filepath.Join(os.TempDir(), "gemini-cli-project-"+projectDirID)
	if workingDir != "" {
		projectDir = workingDir
		projectDirID = ""
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return "", "", fmt.Errorf("failed to create Gemini interactive project dir: %w", err)
	}
	if settingsJSON != "" {
		geminiDir := filepath.Join(projectDir, ".gemini")
		if err := os.MkdirAll(geminiDir, 0o755); err != nil {
			return "", "", fmt.Errorf("failed to create Gemini interactive settings dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
			return "", "", fmt.Errorf("failed to write Gemini interactive settings: %w", err)
		}
	}
	return projectDir, projectDirID, nil
}

func releaseGeminiInteractiveSession(session *geminiInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	session.lastUsed = time.Now()
	session.idleTimer = time.AfterFunc(geminiInteractiveIdleTimeout(), func() {
		closeGeminiPersistentSession(session.ownerSessionID, "idle timeout", logger)
	})
	session.mu.Unlock()
}

func closeGeminiPersistentSession(ownerSessionID, reason string, logger interfaces.Logger) {
	geminiPersistentRegistry.Lock()
	session := geminiPersistentRegistry.sessions[ownerSessionID]
	if session == nil {
		geminiPersistentRegistry.Unlock()
		return
	}
	delete(geminiPersistentRegistry.sessions, ownerSessionID)
	geminiPersistentRegistry.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Closing Gemini interactive session %s for owner %s: %s", session.tmuxSessionName, ownerSessionID, reason)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = runGeminiCommand(closeCtx, nil, "tmux", "send-keys", "-t", session.tmuxSessionName, "C-c")
	_ = killGeminiTmuxSession(closeCtx, session.tmuxSessionName)
	if session.systemPromptTempFile != "" {
		_ = os.Remove(session.systemPromptTempFile)
	}
	unregisterGeminiInteractiveSession(ownerSessionID, session.tmuxSessionName)
}

func markGeminiInteractiveSessionFailedLocked(session *geminiInteractiveSession, err error, logger interfaces.Logger) {
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
		logger.Debugf("Discarding Gemini interactive session %s for owner %s: %v", session.tmuxSessionName, session.ownerSessionID, err)
	}
}

func cleanupFailedGeminiInteractiveSession(session *geminiInteractiveSession) {
	if session == nil {
		return
	}
	removeGeminiPersistentSession(session.ownerSessionID, session)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killGeminiTmuxSession(cleanupCtx, session.tmuxSessionName)
	unregisterGeminiInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
	if session.systemPromptTempFile != "" {
		_ = os.Remove(session.systemPromptTempFile)
	}
}

func removeGeminiPersistentSession(ownerSessionID string, session *geminiInteractiveSession) {
	geminiPersistentRegistry.Lock()
	defer geminiPersistentRegistry.Unlock()
	if current := geminiPersistentRegistry.sessions[ownerSessionID]; current == session {
		delete(geminiPersistentRegistry.sessions, ownerSessionID)
	}
}

func CleanupGeminiCLIInteractiveSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	geminiPersistentRegistry.Lock()
	sessions := make([]*geminiInteractiveSession, 0, len(geminiPersistentRegistry.sessions))
	for _, session := range geminiPersistentRegistry.sessions {
		sessions = append(sessions, session)
	}
	geminiPersistentRegistry.sessions = map[string]*geminiInteractiveSession{}
	geminiPersistentRegistry.Unlock()

	var failures []string
	for _, session := range sessions {
		session.mu.Lock()
		if session.idleTimer != nil {
			session.idleTimer.Stop()
			session.idleTimer = nil
		}
		session.mu.Unlock()
		unregisterGeminiInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		if session.systemPromptTempFile != "" {
			_ = os.Remove(session.systemPromptTempFile)
		}
		if err := killGeminiTmuxSession(ctx, session.tmuxSessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up Gemini interactive sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func registerGeminiInteractiveSession(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if ownerSessionID == "" || tmuxSessionName == "" {
		return
	}
	geminiInteractiveRegistry.Lock()
	defer geminiInteractiveRegistry.Unlock()
	geminiInteractiveRegistry.sessions[ownerSessionID] = tmuxSessionName
}

func unregisterGeminiInteractiveSession(ownerSessionID, tmuxSessionName string) {
	geminiInteractiveRegistry.Lock()
	defer geminiInteractiveRegistry.Unlock()
	if current := geminiInteractiveRegistry.sessions[ownerSessionID]; current == tmuxSessionName {
		delete(geminiInteractiveRegistry.sessions, ownerSessionID)
	}
}

func activeGeminiInteractiveSession(ownerSessionID string) (string, bool) {
	geminiInteractiveRegistry.RLock()
	defer geminiInteractiveRegistry.RUnlock()
	sessionName, ok := geminiInteractiveRegistry.sessions[strings.TrimSpace(ownerSessionID)]
	return sessionName, ok && strings.TrimSpace(sessionName) != ""
}

func SendGeminiInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	if session, ok := geminiPersistentSession(ownerSessionID); ok {
		if session.mu.TryLock() {
			session.mu.Unlock()
			return fmt.Errorf("Gemini interactive session %s is idle; live input should start a normal turn", session.tmuxSessionName)
		}
		return enqueueGeminiLiveInput(session, message)
	}
	sessionName, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Gemini interactive session registered for owner session %s", ownerSessionID)
	}
	return sendGeminiInputToTmux(ctx, sessionName, message)
}

func geminiPersistentSession(ownerSessionID string) (*geminiInteractiveSession, bool) {
	geminiPersistentRegistry.Lock()
	defer geminiPersistentRegistry.Unlock()
	session := geminiPersistentRegistry.sessions[strings.TrimSpace(ownerSessionID)]
	return session, session != nil
}

func enqueueGeminiLiveInput(session *geminiInteractiveSession, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Gemini interactive input is empty")
	}
	session.liveMu.Lock()
	defer session.liveMu.Unlock()
	session.pendingLiveInputs = append(session.pendingLiveInputs, message)
	return nil
}

func popGeminiLiveInput(session *geminiInteractiveSession) (string, bool) {
	session.liveMu.Lock()
	defer session.liveMu.Unlock()
	if len(session.pendingLiveInputs) == 0 {
		return "", false
	}
	message := session.pendingLiveInputs[0]
	copy(session.pendingLiveInputs, session.pendingLiveInputs[1:])
	session.pendingLiveInputs[len(session.pendingLiveInputs)-1] = ""
	session.pendingLiveInputs = session.pendingLiveInputs[:len(session.pendingLiveInputs)-1]
	return message, true
}

func geminiInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyInteractiveSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func geminiPersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyPersistentInteractive].(bool)
	return ok && enabled
}

func splitGeminiSystemPrompt(messages []llmtypes.MessageContent) (string, []llmtypes.MessageContent) {
	var systems []string
	conversation := make([]llmtypes.MessageContent, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			for _, part := range msg.Parts {
				if textPart, ok := part.(llmtypes.TextContent); ok {
					systems = append(systems, textPart.Text)
				}
			}
			continue
		}
		conversation = append(conversation, msg)
	}
	return strings.Join(systems, "\n\n"), conversation
}

func buildGeminiInteractivePrompt(messages []llmtypes.MessageContent) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llmtypes.ChatMessageTypeHuman {
			current := extractTextFromMessage(messages[i])
			if prior := buildGeminiPriorConversationContext(messages[:i]); prior != "" {
				return prior + "\n\nCurrent user message:\n" + current
			}
			return current
		}
	}
	return ""
}

func buildGeminiPriorConversationContext(messages []llmtypes.MessageContent) string {
	const maxChars = 16000
	var lines []string
	for _, msg := range messages {
		text := strings.TrimSpace(extractTextFromMessage(msg))
		if text == "" {
			continue
		}
		switch msg.Role {
		case llmtypes.ChatMessageTypeHuman:
			lines = append(lines, "User: "+text)
		case llmtypes.ChatMessageTypeAI:
			lines = append(lines, "Assistant: "+text)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	context := strings.Join(lines, "\n\n")
	if len(context) > maxChars {
		context = context[len(context)-maxChars:]
		if idx := strings.Index(context, "\n\n"); idx >= 0 {
			context = context[idx+2:]
		}
	}
	return "Previous conversation context for this same chat:\n" + context
}

func geminiAssistantHistory(messages []llmtypes.MessageContent) []string {
	history := make([]string, 0)
	for _, msg := range messages {
		if msg.Role != llmtypes.ChatMessageTypeAI {
			continue
		}
		text := strings.TrimSpace(extractTextFromMessage(msg))
		if text != "" {
			history = append(history, text)
		}
	}
	return history
}

func startGeminiTmuxSession(ctx context.Context, sessionName string, args []string, env []string, projectDir string) error {
	if projectDir == "" {
		projectDir = geminiMustGetwd()
	}
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	for _, entry := range env {
		if strings.TrimSpace(entry) != "" {
			tmuxArgs = append(tmuxArgs, "-e", entry)
		}
	}
	shellCommand := shelllaunch.Command(args, projectDir)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runGeminiCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start Gemini interactive session %q: %w", sessionName, err)
	}
	_ = runGeminiCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	return nil
}

func waitForGeminiPrompt(ctx context.Context, sessionName string) error {
	deadline, cancel := context.WithTimeout(ctx, geminiInteractivePromptWait())
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			captured, _ := captureGeminiPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out waiting for Gemini CLI prompt; latest pane:\n%s", captured)
			}
			return fmt.Errorf("timed out waiting for Gemini CLI prompt")
		case <-ticker.C:
			captured, err := captureGeminiPane(deadline, sessionName)
			if err == nil && hasGeminiReadyPrompt(captured) {
				return nil
			}
			if err == nil && hasGeminiTrustPrompt(captured) {
				_ = submitGeminiInputInTmux(deadline, sessionName)
				continue
			}
		}
	}
}

func sendGeminiPromptToTmux(ctx context.Context, sessionName, prompt string) error {
	return sendGeminiInputToTmux(ctx, sessionName, prompt)
}

func sendGeminiInputToTmux(ctx context.Context, sessionName, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Gemini interactive input is empty")
	}
	bufferName := "mlp-gemini-input-" + geminiRandomHex(6)
	tmp, err := os.CreateTemp("", "gemini-tmux-input-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create Gemini tmux input temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(message); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write Gemini tmux input temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close Gemini tmux input temp file: %w", err)
	}
	if err := runGeminiCommand(ctx, nil, "tmux", "load-buffer", "-b", bufferName, tmpPath); err != nil {
		return fmt.Errorf("failed to load Gemini input into tmux buffer: %w", err)
	}
	if err := runGeminiCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-p", "-r", "-b", bufferName, "-t", sessionName); err != nil {
		return fmt.Errorf("failed to paste input into Gemini interactive session: %w", err)
	}
	if err := submitGeminiInputInTmux(ctx, sessionName); err != nil {
		return fmt.Errorf("failed to submit input to Gemini interactive session: %w", err)
	}
	return nil
}

func submitGeminiInputInTmux(ctx context.Context, sessionName string) error {
	// Gemini CLI 0.42 binds prompt submission to tmux's Enter key name. Sending
	// C-m can leave text as an unsubmitted multiline draft in the prompt editor.
	return runGeminiCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Enter")
}

func waitForGeminiInteractiveResponse(ctx context.Context, session *geminiInteractiveSession, baseline string, streamChan chan<- llmtypes.StreamChunk) (string, error) {
	sessionName := session.tmuxSessionName
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var sawActivity bool
	var idleSince time.Time
	var lastCaptured string
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	var lastDraftSubmit time.Time
	var draftSubmitAttempts int
	streamTerminalScreen := geminiInteractiveStreamTmuxScreenEnabled()
	for {
		select {
		case <-ctx.Done():
			captured, _ := captureGeminiPane(context.Background(), sessionName)
			return captured, ctx.Err()
		case <-ticker.C:
			captured, err := captureGeminiPane(ctx, sessionName)
			if err != nil {
				return "", err
			}
			delta := geminiCapturedAfterBaseline(captured, baseline)
			if apiErr := geminiInteractiveAPIError(delta); apiErr != "" {
				return captured, fmt.Errorf("Gemini CLI interactive API error: %s", apiErr)
			}
			if hasGeminiReadyPrompt(captured) {
				if liveMessage, ok := popGeminiLiveInput(session); ok {
					if err := sendGeminiInputToTmux(ctx, sessionName, liveMessage); err != nil {
						return captured, err
					}
					sawActivity = false
					idleSince = time.Time{}
					lastCaptured = captured
					continue
				}
			}
			if hasGeminiUnsubmittedDraft(captured) && draftSubmitAttempts < 5 &&
				(lastDraftSubmit.IsZero() || time.Since(lastDraftSubmit) >= time.Second) {
				if !hasGeminiActivity(captured) {
					_ = submitGeminiInputInTmux(ctx, sessionName)
					lastDraftSubmit = time.Now()
					draftSubmitAttempts++
				}
				lastCaptured = captured
				continue
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamGeminiTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if hasGeminiActivity(captured) {
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if strings.TrimSpace(delta) != "" {
				sawActivity = true
			}
			if !sawActivity || !hasGeminiReadyPrompt(captured) {
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
			if time.Since(idleSince) >= geminiInteractiveStableWindow {
				return captured, nil
			}
		}
	}
}

func geminiInteractiveAPIError(delta string) string {
	cleaned := strings.TrimSpace(stripGeminiANSI(delta))
	lower := strings.ToLower(cleaned)
	if !strings.Contains(lower, "api error") && !strings.Contains(lower, "api_key_invalid") {
		return ""
	}
	lines := strings.Split(cleaned, "\n")
	out := make([]string, 0, 8)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isGeminiTUILine(trimmed) {
			continue
		}
		out = append(out, trimmed)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return truncate(cleaned, 500)
	}
	return truncate(strings.Join(out, " "), 500)
}

func parseGeminiInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
	delta := geminiCapturedAfterBaseline(captured, baseline)
	text := extractLatestGeminiMarkedAssistantText(delta)
	if strings.TrimSpace(text) == "" {
		text = extractGeminiVisibleAssistantText(delta)
	}
	text = stripGeminiEchoedUserPrompt(text, echoedUserPrompt)
	text = stripGeminiHistoricalAssistantText(text, historicalAssistantTexts)
	text = stripGeminiLeadingPromptFragments(text, echoedUserPrompt)
	return strings.TrimSpace(text)
}

func extractLatestGeminiMarkedAssistantText(delta string) string {
	lines := strings.Split(delta, "\n")
	blocks := make([]string, 0, 2)
	current := make([]string, 0)
	inAssistantBlock := false
	skipStartupContinuation := false

	flush := func() {
		if len(current) == 0 {
			inAssistantBlock = false
			return
		}
		content := strings.TrimSpace(strings.Join(current, "\n"))
		current = current[:0]
		inAssistantBlock = false
		if content != "" {
			blocks = append(blocks, content)
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(stripGeminiANSI(line))
		if trimmed == "" {
			if inAssistantBlock {
				current = append(current, "")
			}
			skipStartupContinuation = false
			continue
		}
		if skipStartupContinuation && isGeminiStartupContinuationLine(trimmed) {
			skipStartupContinuation = false
			continue
		}
		skipStartupContinuation = false
		if isGeminiStartupNoticeLine(trimmed) {
			flush()
			skipStartupContinuation = true
			continue
		}
		if isGeminiTUILine(trimmed) || isGeminiToolPanelLine(trimmed) {
			flush()
			continue
		}
		if content, ok := trimGeminiAssistantMarker(trimmed); ok {
			flush()
			if content != "" {
				current = append(current, content)
			}
			inAssistantBlock = true
			continue
		}
		if inAssistantBlock {
			current = append(current, trimGeminiBulletPrefix(trimmed))
		}
	}
	flush()

	if len(blocks) == 0 {
		return ""
	}
	return strings.TrimSpace(blocks[len(blocks)-1])
}

func extractGeminiVisibleAssistantText(delta string) string {
	lines := strings.Split(delta, "\n")
	out := make([]string, 0, len(lines))
	skipStartupContinuation := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripGeminiANSI(line))
		if trimmed == "" {
			skipStartupContinuation = false
			continue
		}
		if skipStartupContinuation && isGeminiStartupContinuationLine(trimmed) {
			skipStartupContinuation = false
			continue
		}
		skipStartupContinuation = false
		if isGeminiStartupNoticeLine(trimmed) {
			skipStartupContinuation = true
			continue
		}
		if isGeminiTUILine(trimmed) || isGeminiToolPanelLine(trimmed) {
			continue
		}
		if markerContent, ok := trimGeminiAssistantMarker(trimmed); ok {
			trimmed = markerContent
		} else {
			trimmed = trimGeminiBulletPrefix(trimmed)
		}
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func trimGeminiAssistantMarker(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "✦"):
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "✦")), true
	case strings.HasPrefix(trimmed, "->"):
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "->")), true
	case strings.HasPrefix(trimmed, "→"):
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "→")), true
	default:
		return trimmed, false
	}
}

func trimGeminiBulletPrefix(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "•"))
}

func sanitizeGeminiStreamJSONContent(content string) string {
	if content == "" || !containsGeminiStreamNoise(content) {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	var out strings.Builder
	skipContinuation := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripGeminiANSI(line))
		if trimmed == "" {
			skipContinuation = false
			out.WriteString(line)
			continue
		}
		if skipContinuation && isGeminiStartupContinuationLine(trimmed) {
			skipContinuation = false
			continue
		}
		skipContinuation = false
		if isGeminiStartupNoticeLine(trimmed) || isGeminiToolPanelLine(trimmed) {
			skipContinuation = true
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

func containsGeminiStreamNoise(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "policy file warning") ||
		strings.Contains(lower, "waiting for mcp servers") ||
		strings.Contains(lower, "slash commands are still available") ||
		strings.Contains(lower, "api-bridge mcp server") ||
		strings.Contains(lower, "execute_shell_command (") ||
		strings.Contains(lower, "mcp_server_tool") ||
		strings.Contains(lower, "mcpname =") ||
		strings.Contains(lower, "tools.exclude")
}

func isGeminiStartupNoticeLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "waiting for mcp servers to initialize") ||
		strings.Contains(lower, "slash commands are still available") ||
		strings.Contains(lower, "prompts will be queued") ||
		strings.Contains(lower, "policy file warning") ||
		strings.Contains(lower, "unrecognized tool name") ||
		strings.Contains(lower, "syntax for mcp tools is strictly deprecated") ||
		strings.Contains(lower, "mcpname =") ||
		strings.Contains(lower, "mcp_server_tool") ||
		strings.Contains(lower, "warning: tools.exclude in settings.json is deprecated") ||
		strings.Contains(lower, "project-level hooks have been detected") ||
		strings.Contains(lower, "these hooks will be executed") ||
		strings.Contains(lower, "review the project settings")
}

func isGeminiStartupContinuationLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "prompts will be queued") ||
		strings.Contains(lower, "unrecognized tool name") ||
		strings.Contains(lower, "syntax for mcp tools is strictly deprecated") ||
		strings.Contains(lower, "mcpname =") ||
		strings.Contains(lower, "mcp_server_tool") ||
		strings.Contains(lower, "these hooks will be executed") ||
		strings.Contains(lower, "review the project settings") ||
		strings.HasPrefix(lower, "and prompts will be queued")
}

func isGeminiToolPanelLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	if strings.HasPrefix(trimmed, "│") ||
		strings.HasPrefix(trimmed, "╭") ||
		strings.HasPrefix(trimmed, "╰") ||
		strings.HasPrefix(trimmed, "┌") ||
		strings.HasPrefix(trimmed, "└") ||
		strings.HasPrefix(trimmed, "├") {
		return true
	}
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "api-bridge mcp server") ||
		strings.Contains(lower, "execute_shell_command (") ||
		strings.Contains(lower, "mcp server)") ||
		strings.HasPrefix(lower, "✓ execute_shell_command")
}

func isGeminiTUILine(line string) bool {
	lower := strings.ToLower(line)
	if line == ">" || strings.EqualFold(strings.TrimSpace(line), "Gemini") || strings.HasPrefix(line, "> ") || strings.Contains(lower, "type your message") {
		return true
	}
	if strings.Contains(lower, "gemini cli") ||
		strings.Contains(lower, "authenticated with") ||
		strings.Contains(lower, "tips for getting started") ||
		strings.Contains(lower, "create gemini.md") ||
		strings.Contains(lower, "/help for more information") ||
		strings.Contains(lower, "ask coding questions") ||
		strings.Contains(lower, "be specific for the best results") ||
		strings.Contains(lower, "gemini cli update available") ||
		strings.Contains(lower, "installed via") ||
		strings.Contains(lower, "esc to cancel") ||
		strings.Contains(lower, "esc to interrupt") ||
		strings.Contains(lower, "ctrl+y") ||
		strings.Contains(lower, "ctrl+o") ||
		strings.Contains(lower, "pasted text") ||
		isGeminiActiveStatusLine(line) ||
		strings.Contains(lower, "? for shortcuts") ||
		strings.Contains(lower, "shift+tab") ||
		strings.Contains(lower, "workspace (/directory)") ||
		strings.Contains(lower, "no sandbox") ||
		strings.Contains(lower, "/model") {
		return true
	}
	return isGeminiBoxDrawingLine(line)
}

func isGeminiBoxDrawingLine(line string) bool {
	if line == "" {
		return true
	}
	for _, r := range line {
		if strings.ContainsRune("─━▀▄▁▂▃▅▆▇█▌▐▝▜▗▟▘▛▙▚▞▖▌╭╮╰╯│┌┐└┘├┤┬┴┼ ", r) {
			continue
		}
		return false
	}
	return true
}

func hasGeminiActivity(captured string) bool {
	lines := strings.Split(stripGeminiANSI(captured), "\n")
	seenNonEmpty := 0
	for i := len(lines) - 1; i >= 0 && seenNonEmpty < 24; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seenNonEmpty++
		lower := strings.ToLower(line)
		if isGeminiReadyPromptLine(line) {
			return false
		}
		if strings.Contains(lower, "esc to cancel") ||
			strings.Contains(lower, "esc to interrupt") ||
			isGeminiActiveStatusLine(line) {
			return true
		}
	}
	return false
}

func isGeminiActiveStatusLine(line string) bool {
	trimmed := strings.TrimSpace(stripGeminiANSI(line))
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "generating") ||
		strings.HasPrefix(lower, "thinking") ||
		strings.HasPrefix(lower, "processing") ||
		strings.HasPrefix(lower, "running") ||
		strings.HasPrefix(lower, "executing")
}

func hasGeminiTrustPrompt(captured string) bool {
	lower := strings.ToLower(stripGeminiANSI(captured))
	return strings.Contains(lower, "do you trust the files in this folder") &&
		strings.Contains(lower, "trust folder")
}

func hasGeminiReadyPrompt(captured string) bool {
	lines := strings.Split(stripGeminiANSI(captured), "\n")
	seenNonEmpty := 0
	for i := len(lines) - 1; i >= 0 && seenNonEmpty < 16; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seenNonEmpty++
		if isGeminiReadyPromptLine(line) {
			return true
		}
	}
	return false
}

func hasGeminiUnsubmittedDraft(captured string) bool {
	lines := strings.Split(stripGeminiANSI(captured), "\n")
	seenNonEmpty := 0
	for i := len(lines) - 1; i >= 0 && seenNonEmpty < 16; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seenNonEmpty++
		if isGeminiReadyPromptLine(line) {
			continue
		}
		if strings.HasPrefix(line, ">") || strings.HasPrefix(line, "*") {
			draft := normalizeGeminiPromptLine(line)
			return draft != ""
		}
	}
	return false
}

func isGeminiReadyPromptLine(line string) bool {
	trimmed := strings.TrimSpace(stripGeminiANSI(line))
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, "type your message") {
		return false
	}
	return strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "*")
}

func streamGeminiTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	snapshot, err := captureGeminiPane(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripGeminiANSI(snapshot), "\n")
	if strings.TrimSpace(snapshot) == "" || snapshot == *lastTerminalSnapshot {
		return false
	}
	select {
	case streamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeTerminal, Content: snapshot}:
		*lastTerminalSnapshot = snapshot
		return true
	default:
		return false
	}
}

func stripGeminiHistoricalAssistantText(text string, historicalAssistantTexts []string) string {
	text = strings.TrimSpace(text)
	if text == "" || len(historicalAssistantTexts) == 0 {
		return text
	}
	for i := len(historicalAssistantTexts) - 1; i >= 0; i-- {
		historical := strings.TrimSpace(historicalAssistantTexts[i])
		if historical == "" {
			continue
		}
		if stripped, ok := stripGeminiHistoricalPrefix(text, historical); ok {
			text = strings.TrimSpace(stripped)
			i = len(historicalAssistantTexts)
			continue
		}
		if idx := strings.LastIndex(text, historical); idx >= 0 {
			text = strings.TrimSpace(text[idx+len(historical):])
			i = len(historicalAssistantTexts)
		}
	}
	return text
}

func stripGeminiEchoedUserPrompt(text, prompt string) string {
	text = strings.TrimSpace(text)
	prompt = strings.TrimSpace(prompt)
	if text == "" || prompt == "" {
		return text
	}

	lines := nonEmptyGeminiLines(text)
	promptLines := nonEmptyGeminiLines(prompt)
	if len(lines) == 0 || len(promptLines) == 0 {
		return text
	}

	bestStart := -1
	bestLen := 0
	for start := 0; start < len(lines) && start < 32; start++ {
		for promptStart := 0; promptStart < len(promptLines); promptStart++ {
			matchLen := 0
			for start+matchLen < len(lines) &&
				promptStart+matchLen < len(promptLines) &&
				geminiPromptLinesEqual(lines[start+matchLen], promptLines[promptStart+matchLen]) {
				matchLen++
			}
			if matchLen > bestLen {
				bestStart = start
				bestLen = matchLen
			}
		}
	}

	if bestLen < 2 {
		return text
	}
	out := make([]string, 0, len(lines)-bestLen)
	out = append(out, lines[:bestStart]...)
	out = append(out, lines[bestStart+bestLen:]...)
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func geminiPromptLinesEqual(a, b string) bool {
	a = normalizeGeminiPromptLine(a)
	b = normalizeGeminiPromptLine(b)
	return a != "" && a == b
}

func normalizeGeminiPromptLine(line string) string {
	line = strings.TrimSpace(stripGeminiANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, ">")
	line = strings.TrimPrefix(line, "*")
	line = strings.TrimSpace(line)
	return line
}

func stripGeminiLeadingPromptFragments(text, prompt string) string {
	lines := nonEmptyGeminiLines(text)
	if len(lines) == 0 || strings.TrimSpace(prompt) == "" {
		return strings.TrimSpace(text)
	}
	normalizedPrompt := strings.ToLower(strings.Join(nonEmptyGeminiLines(prompt), "\n"))
	drop := 0
	for drop < len(lines) && drop < 3 {
		line := normalizeGeminiPromptLine(lines[drop])
		if line == "" || len([]rune(line)) > 24 || strings.Contains(line, "_") {
			break
		}
		if !strings.Contains(normalizedPrompt, strings.ToLower(line)) {
			break
		}
		drop++
	}
	if drop == 0 {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(strings.Join(lines[drop:], "\n"))
}

func stripGeminiHistoricalPrefix(text, historical string) (string, bool) {
	if text == historical {
		return "", true
	}
	if strings.HasPrefix(text, historical) {
		return text[len(historical):], true
	}

	historicalLines := nonEmptyGeminiLines(historical)
	if len(historicalLines) == 0 {
		return text, false
	}
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

func nonEmptyGeminiLines(text string) []string {
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

func interruptGeminiInteractiveSession(sessionName string, logger interfaces.Logger) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runGeminiCommand(interruptCtx, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil && logger != nil {
		logger.Debugf("Failed to send Escape to Gemini interactive session %s: %v", sessionName, err)
	}
}

func resetGeminiPaneForTurn(ctx context.Context, sessionName string) {
	// Do not send Ctrl-L to Gemini CLI here. Recent Gemini CLI builds treat the
	// shortcut as more than a visual repaint and can drop native conversation
	// context, breaking persistent multi-turn chat. Clearing tmux scrollback is
	// enough; per-turn parsing is anchored to the captured baseline.
	_ = runGeminiCommand(ctx, nil, "tmux", "clear-history", "-t", sessionName)
}

func captureGeminiPane(ctx context.Context, sessionName string) (string, error) {
	return runGeminiCommandOutput(ctx, nil, "tmux", "capture-pane", "-p", "-J", "-S", "-3000", "-t", sessionName)
}

func geminiCapturedAfterBaseline(captured, baseline string) string {
	if baseline != "" {
		if idx := strings.LastIndex(captured, baseline); idx >= 0 {
			return captured[idx+len(baseline):]
		}
	}
	return captured
}

func listGeminiTmuxSessions(ctx context.Context) ([]string, error) {
	out, err := runGeminiCommandOutput(ctx, nil, "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") {
			return nil, nil
		}
		return nil, err
	}
	prefix := geminiInteractiveSessionPrefix()
	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

func killGeminiTmuxSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return nil
	}
	if err := runGeminiCommand(ctx, nil, "tmux", "kill-session", "-t", sessionName); err != nil {
		if strings.Contains(err.Error(), "can't find session") || strings.Contains(err.Error(), "no server running") {
			return nil
		}
		return err
	}
	return nil
}

func geminiInteractiveSessionPrefix() string {
	prefix := strings.TrimSpace(os.Getenv(EnvGeminiInteractiveSessionPrefix))
	if prefix == "" {
		prefix = "mlp-gemini-cli-int"
	}
	return sanitizeGeminiTmuxSessionName(prefix)
}

func newGeminiTmuxSessionName() string {
	return sanitizeGeminiTmuxSessionName(fmt.Sprintf("%s-%d-%s", geminiInteractiveSessionPrefix(), time.Now().UnixNano(), geminiRandomHex(4)))
}

func geminiInteractiveTimeout() time.Duration {
	return geminiDurationFromEnv(EnvGeminiInteractiveTimeoutSeconds, defaultGeminiInteractiveTimeout)
}

func geminiInteractiveIdleTimeout() time.Duration {
	return geminiDurationFromEnv(EnvGeminiInteractiveIdleTimeoutSeconds, defaultGeminiInteractiveIdleTimeout)
}

func geminiInteractivePromptWait() time.Duration {
	return geminiDurationFromEnv(EnvGeminiInteractivePromptWaitSeconds, defaultGeminiInteractivePromptWait)
}

func geminiInteractiveStreamTmuxScreenEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvGeminiInteractiveStreamTmuxScreen))) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func geminiDurationFromEnv(key string, fallback time.Duration) time.Duration {
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

func runGeminiCommand(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	_, err := runGeminiCommandOutput(ctx, stdin, name, args...)
	return err
}

func runGeminiCommandOutput(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s failed: %w: %s", name, geminiCommandString(args), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func geminiCommandString(args []string) string {
	redacted := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "GEMINI_API_KEY=") {
			redacted[i] = "GEMINI_API_KEY=<redacted>"
			continue
		}
		redacted[i] = arg
	}
	return strings.Join(redacted, " ")
}

func geminiShellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = geminiShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func geminiShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func geminiMustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func geminiRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sanitizeGeminiTmuxSessionName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "gemini"
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

func stripGeminiANSI(s string) string {
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
