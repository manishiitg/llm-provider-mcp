package cursorcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

const (
	// Default to no provider-level turn timeout. Workflow/background callers own
	// their execution deadline; the adapter should not cancel a still-running tmux
	// coding agent before the outer workflow timeout.
	defaultCursorInteractiveTimeout     = 0
	defaultCursorInteractiveIdleTimeout = 20 * time.Minute
	defaultCursorInteractiveRetention   = 30 * time.Minute
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
	// Capture turn start before doing any I/O so the sidecar parser
	// can scope its store.db pick to a freshly-modified session.
	turnStart := time.Now()

	callCtx, cancel := cursorInteractiveCallContext(ctx)
	defer cancel()

	// On user-initiated cancellation, tear down the persistent tmux
	// session so the live pane closes alongside the workflow step.
	defer func() {
		if ctx.Err() != context.Canceled {
			return
		}
		closeCursorPersistentSession(ownerSessionID, "workflow context canceled", c.logger)
	}()

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
			releaseCursorBoundedInteractiveSession(session, c.logger)
		}
	}()

	if err := waitForCursorPrompt(callCtx, session.tmuxSessionName, opts.StreamChan); err != nil {
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
	if err := waitForCursorPrompt(callCtx, session.tmuxSessionName, opts.StreamChan); err != nil {
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
	forcedComplete := errors.Is(err, tmuxcontrol.ErrForceComplete)
	if err != nil && !forcedComplete {
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
	if forcedComplete && strings.TrimSpace(content) == "" {
		content = forcedCursorInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	}
	// Trailing-capture grace window — see llmtypes.RunTrailingPaneCapture.
	llmtypes.RunTrailingPaneCapture(callCtx, opts.StreamChan,
		func(ctx context.Context) (string, error) {
			snap, err := captureCursorPane(ctx, session.tmuxSessionName)
			if err != nil {
				return "", err
			}
			return strings.TrimRight(stripCursorANSI(snap), "\n"), nil
		},
		map[string]interface{}{
			"tmux_session":               session.tmuxSessionName,
			"cursor_interactive_session": session.tmuxSessionName,
		},
	)
	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	additional := map[string]interface{}{
		"provider":                      "cursor-cli",
		"cursor_mode":                   "tmux",
		"cursor_interactive_session":    session.tmuxSessionName,
		"cursor_persistent_interactive": persistent,
		"cursor_uses_print_json":        false,
		"cursor_working_dir":            session.workingDir,
	}
	if !persistent {
		// terminal_retention_seconds intentionally not set: the rail
		// snapshot stays read-only until the user dismisses it via the
		// X button. Tmux itself is killed quickly after the turn via
		// the bounded-session cleanup using llmtypes.TmuxKillDelay.
		// The cursor_interactive_retention_seconds value is kept for
		// any backend code that still tracks it for diagnostics.
		additional["cursor_interactive_retention_seconds"] = int(cursorInteractiveRetention().Seconds())
	}

	// Cursor's tmux TUI does not expose exact token counts in a format the
	// adapter can read (the running counter on the "⠰⠰ Composing 1.87k
	// tokens" line is cleared once the turn settles). We approximate so the
	// cost ledger receives a non-zero row rather than a bare timestamp.
	// The approximation is char-based (≈ 4 chars/token for English prose,
	// the standard fallback used elsewhere in this codebase); cost is then
	// computed via the same ComputeUSDCostFromMetadata path the structured
	// adapter uses. Numbers may be ±20-30% off the true tokenizer counts —
	// good enough for cost-tracking trends, not for fine-grained per-call
	// billing reconciliation.
	inputTokens, outputTokens := estimateCursorTmuxTokens(prompt, content)
	totalTokens := inputTokens + outputTokens
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtrFromInt(inputTokens),
		OutputTokens: intPtrFromInt(outputTokens),
		TotalTokens:  intPtrFromInt(totalTokens),
		Additional:   additional,
	}
	costLookupModel := c.modelID
	if costLookupModel != "" {
		if meta, _ := c.GetModelMetadata(costLookupModel); meta != nil {
			if cost := llmtypes.ComputeUSDCostFromMetadata(meta, genInfo); cost > 0 {
				additional["cost_usd_estimated"] = cost
				additional["cost_model_id"] = costLookupModel
			}
		}
	}

	// Reconstruct the CLI's internal tool-use trail from cursor's
	// local sqlite store at ~/.cursor/chats/<md5(cwd)>/<agentId>/store.db.
	// Cursor doesn't expose tokens (subscription-priced) but stores
	// the full conversation including tool-call / tool-result blocks,
	// so workflow conversation logs gain the same richness as the
	// claude-code / codex tmux flows.
	if sidecarMsgs := readCursorTranscriptMessages(turnStart, session.workingDir, ownerSessionID); len(sidecarMsgs) > 0 {
		llmtypes.AttachCodingProviderIntermediateMessages(genInfo, llmtypes.CodingProviderIntermediateMessages{
			Provider:  "cursor-cli",
			Transport: llmtypes.CodingProviderTransportTmux,
			Messages:  sidecarMsgs,
		})
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        content,
				GenerationInfo: genInfo,
			},
		},
		Usage: &llmtypes.Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  totalTokens,
		},
	}, nil
}

// estimateCursorTmuxTokens returns (input, output) token counts estimated
// from prompt/response character lengths. Cursor's tmux TUI does not surface
// exact token counts in a parseable form, so this is the best the adapter
// can do without re-implementing the model's tokenizer. The 4-chars-per-
// token heuristic matches what other tmux adapters fall back to when their
// CLI's JSON side-stream is unavailable. Both halves round up so a tiny
// turn still records >0 tokens, otherwise ComputeUSDCostFromMetadata would
// return 0 and the ledger row stays bare.
func estimateCursorTmuxTokens(prompt, content string) (int, int) {
	estimate := func(s string) int {
		n := len(s)
		if n == 0 {
			return 0
		}
		// (n + 3) / 4 = ceil(n / 4)
		return (n + 3) / 4
	}
	return estimate(prompt), estimate(content)
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
	// Persistent interactive sessions pin the system prompt at session startup
	// via .cursor/rules/mlp-system-*.mdc. Do not include the full prompt text
	// in the reuse fingerprint: app-level prompts can contain per-turn dynamic
	// context (e.g. background-agent role labels vs chat-agent labels), and
	// restarting the TUI would tear down the live Cursor pane mid-conversation.
	write("system_prompt_present", strconv.FormatBool(strings.TrimSpace(systemPrompt) != ""))
	writeString(MetadataKeyWorkingDir)
	writeString(MetadataKeyResumeSessionID)
	writeString(MetadataKeySandbox)
	writeString(MetadataKeyMode)
	// Project config (.cursor/cli.json) and MCP config (.cursor/mcp.json) are
	// re-written into the workspace on every call by prepareCursorProjectFiles.
	// The running TUI does not reload them — Cursor reads both at startup only.
	// Hashing the JSON contents here would force a TUI restart whenever the
	// caller's MCP config embeds per-turn data (e.g. a Langfuse TraceID inside
	// MCP_VIRTUAL_SCOPE_ID), which it always does for the bridge config built
	// by mcpagent. Hash only "is config provided" so a session that started
	// without an MCP config still gets restarted when one is added later.
	hasStringValue := func(key string) bool {
		v, ok := custom[key].(string)
		return ok && strings.TrimSpace(v) != ""
	}
	write("project_config_present", strconv.FormatBool(hasStringValue(MetadataKeyProjectConfig)))
	write("mcp_config_present", strconv.FormatBool(hasStringValue(MetadataKeyMCPConfig)))
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

func releaseCursorBoundedInteractiveSession(session *cursorInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	// Keep the real tmux pane alive for the shared bounded retention window so
	// the UI terminal remains inspectable/debuggable while it is visible.
	retention := llmtypes.TmuxKillDelay
	session.lastUsed = time.Now()
	if retention <= 0 {
		closeCursorSessionLocked(session, "bounded turn complete", logger)
		return
	}
	if logger != nil {
		logger.Debugf("Retaining completed Cursor interactive session %s for owner %s for %s (then kill)", session.tmuxSessionName, session.ownerSessionID, retention)
	}
	session.idleTimer = time.AfterFunc(retention, func() {
		closeCursorPersistentSession(session.ownerSessionID, "bounded retention elapsed", logger)
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
		cleanupFiles := stopCursorIdleTimerAndSnapshotCleanupIfAvailable(session)
		unregisterCursorInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		if cleanupFiles != nil {
			cleanupFiles()
		}
		if err := killCursorTmuxSession(ctx, session.tmuxSessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up Cursor interactive sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func stopCursorIdleTimerAndSnapshotCleanupIfAvailable(session *cursorInteractiveSession) func() {
	if session == nil || !session.mu.TryLock() {
		return nil
	}
	defer session.mu.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	cleanupFiles := session.cleanupFiles
	session.cleanupFiles = nil
	return cleanupFiles
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

func waitForCursorPrompt(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) error {
	deadline, cancel := context.WithTimeout(ctx, cursorInteractivePromptWait())
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var trustSubmitted bool
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	streamTerminalScreen := cursorInteractiveStreamTmuxScreenEnabled()
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
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamCursorTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
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

// cursorTypedInputMaxLen is the upper bound under which a single-line message
// is typed via `tmux send-keys -l` (keystroke injection) instead of
// paste-buffer + bracketed paste. Keeping short, single-line input out of the
// bracketed-paste path stops Cursor's TUI from rendering normal chat turns as
// "[Pasted text #N]". Multi-line or longer payloads still go through
// paste-buffer to preserve newlines and avoid premature submission.
const cursorTypedInputMaxLen = 240

func sendCursorInputToTmux(ctx context.Context, sessionName, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Cursor interactive input is empty")
	}
	if !strings.ContainsAny(message, "\n\r") && len(message) <= cursorTypedInputMaxLen {
		return typeCursorInputToTmux(ctx, sessionName, message)
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
	// Cursor consumes the first Enter when the follow-ups suggestion box is
	// showing (it dismisses the menu but does NOT submit the text — the text
	// stays in the input draft). One extra Enter is needed to actually send.
	// We don't know up front whether the menu was shown, so probe: if after
	// the first Enter the draft is still in the input field, send another.
	ensureCursorInputSubmitted(ctx, sessionName, message)
	return nil
}

// typeCursorInputToTmux delivers a short single-line message to Cursor's TUI
// as keystrokes via `tmux send-keys -l` instead of paste-buffer. The TUI then
// treats it as normal typed input and does not show the "[Pasted text]"
// marker. Used only for messages that have no embedded newlines and fit under
// cursorTypedInputMaxLen — multi-line or longer payloads stay on the
// paste-buffer/bracketed-paste path so Cursor doesn't submit on every \n.
func typeCursorInputToTmux(ctx context.Context, sessionName, message string) error {
	if err := runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "-l", message); err != nil {
		return fmt.Errorf("failed to type input into Cursor interactive session: %w", err)
	}
	waitForCursorInputDraftVisible(ctx, sessionName, message, 2*time.Second)
	if err := runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return fmt.Errorf("failed to submit typed input to Cursor interactive session: %w", err)
	}
	ensureCursorInputSubmitted(ctx, sessionName, message)
	return nil
}

// ensureCursorInputSubmitted polls briefly after the initial C-m and sends a
// second C-m if the pasted text is still sitting in the input draft (which
// happens when the follow-ups menu, or any other modal overlay, swallows the
// first Enter). Best-effort: errors are ignored because the first submit may
// have succeeded and the pane just hasn't repainted yet.
func ensureCursorInputSubmitted(ctx context.Context, sessionName, message string) {
	deadline, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			return
		case <-ticker.C:
			captured, err := captureCursorPane(deadline, sessionName)
			if err != nil {
				continue
			}
			if !cursorPaneShowsPromptDraft(captured, message) {
				return
			}
			_ = runCursorCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "C-m")
			return
		}
	}
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
			if tmuxcontrol.ConsumeForceComplete(sessionName) {
				return captured, tmuxcontrol.ErrForceComplete
			}
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
			// Reset idle only when we have activity AND we're not yet
			// at the ready prompt. Cursor's TUI leaves stale status
			// lines ("Running...", "Thinking...") visible for several
			// seconds after the → prompt reappears; those used to
			// keep restarting the idle timer and added 5-10s of
			// avoidable wait to every turn. hasCursorReadyPrompt
			// already handles the "→ visible + stale status" case
			// correctly (returns true), so once we're ready we let
			// the stable-window check drive completion.
			if !hasCursorReadyPrompt(captured) && hasCursorActivity(captured) {
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

func forcedCursorInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
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
		if isCursorPromptBoundaryLine(trimmed) {
			break
		}
		// "User: …" marks the start of a user turn. Everything previously
		// collected is from an older assistant turn and must be discarded —
		// otherwise a multi-turn pane (where baseline-diff falls back to
		// line-prefix mode and leaves prior turns in the delta) leaks the
		// stale reply into the new turn's extracted text.
		if isCursorUserTurnHeader(trimmed) {
			out = out[:0]
			continue
		}
		if trimmed == "" {
			// Preserve blank lines as paragraph-break markers (collapse runs),
			// but never start the response with a blank.
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if isCursorTUILine(trimmed) || isCursorToolStatusLine(trimmed) || isCursorBoxDrawingLine(trimmed) {
			continue
		}
		out = append(out, trimmed)
	}
	// Drop trailing blank markers introduced by the input-box gap.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	// Blank lines between non-blank lines are preserved as "" entries, so the
	// joined output uses "\n\n" between paragraphs — CommonMark renders that as
	// a real paragraph break, while a single "\n" inside a paragraph (a tmux
	// hard-wrap) is treated as a soft break and rendered as a space.
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// isCursorUserTurnHeader matches Cursor's per-turn user header ("User: <prompt>").
// Anchored on the colon-space pair to avoid matching prose like "User input is".
func isCursorUserTurnHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "User:") && !strings.HasPrefix(trimmed, "user:") {
		return false
	}
	// Require the colon to be followed by whitespace OR end-of-line — distinguishes
	// Cursor's "User: hi" header from prose like "User:enter your username".
	if len(trimmed) == len("User:") {
		return true
	}
	next := trimmed[len("User:")]
	return next == ' ' || next == '\t'
}

func normalizeCursorPaneLine(line string) string {
	line = strings.TrimSpace(stripCursorANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSuffix(line, "│")
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.TrimPrefix(line, "• "))
	// Cursor labels each assistant turn with a literal "Assistant:" header. Strip
	// it so the kept response reads as plain prose (and matches what the user
	// sees in the chat panel for Claude/Gemini, which have no such label).
	line = strings.TrimSpace(strings.TrimPrefix(line, "Assistant:"))
	return line
}

// cursorShellEchoSuffix matches the duration suffix Cursor appends to shell-tool
// echoes (e.g. "$ ls -1 /tmp 407ms", "$ sleep 1 1.0s"). Used to distinguish a
// shell-tool transcript line from a code block that legitimately starts with "$".
var cursorShellEchoSuffix = regexp.MustCompile(`\s\d+(?:\.\d+)?(?:ms|s)\s*$`)

// cursorFoundCountLine matches the tool-result summary Cursor prints after
// grep/glob/list operations: "Found 33 files", "Found 1,024 matches", etc.
var cursorFoundCountLine = regexp.MustCompile(`^found\s+[\d,]+\s+(files?|matches?|results?|symbols?)\b`)

// cursorMultiToolSummaryLine matches Cursor's per-turn tool-activity summary:
//
//	"Read, grepped, globbed 7 files, 4 greps, 2 globs"
//
// The line lists past-tense verbs followed by counts. Always tool narration,
// never response prose.
var cursorMultiToolSummaryLine = regexp.MustCompile(`^(?:read|grepped|globbed|listed|searched)[,\s].*\b\d+\s+(?:files?|greps?|globs?|matches?|results?|symbols?|reads?|lists?|searches?)\b`)

// cursorEarlierHiddenLine matches Cursor's truncation header on long tool
// transcripts, e.g. "… 10 earlier items hidden" or "... 3 earlier tools hidden".
var cursorEarlierHiddenLine = regexp.MustCompile(`^(?:…|\.\.\.)\s*\d+\s+earlier\s+(?:items?|tools?|results?)\b`)

// cursorReadFileLine matches Cursor's per-file read narration. Cursor truncates
// long paths with `...`, so a real prose line "Read the docs" is unaffected — the
// regex requires a path token and either "lines N-M" or a file extension.
var cursorReadFileLine = regexp.MustCompile(`^read\s+(?:\.\.\.|/|~)\S*\s+(?:lines?\s+\d+(?:-\d+)?|.*\.\w{1,8}\b)`)

func isCursorPromptBoundaryLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	// The → arrow is Cursor's input cursor — the most reliable structural
	// boundary. In the delta (after baseline stripping), it only appears at
	// the bottom after the response completes.
	return strings.HasPrefix(trimmed, "→") ||
		trimmed == ">" ||
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
		strings.HasPrefix(lower, "add a follow-up") ||
		strings.HasPrefix(lower, "auto-run") ||
		// Cursor labels each user turn with a literal "User:" header. It is a
		// structural marker, not response prose, so drop it from extraction.
		strings.HasPrefix(lower, "user:")
}

func isCursorToolStatusLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "thinking") ||
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
		strings.HasPrefix(lower, "globbed ") ||
		strings.HasPrefix(lower, "listed ") ||
		strings.HasPrefix(lower, "grepped ") ||
		strings.Contains(lower, "mcp") && strings.Contains(lower, "tool") ||
		strings.Contains(lower, "shell(") ||
		strings.Contains(lower, `"stdout"`) ||
		strings.Contains(lower, `"stderr"`) ||
		strings.Contains(lower, `"exit_code"`) {
		return true
	}
	// Tool-result summary lines: "Found N files", "Found N matches", …
	if cursorFoundCountLine.MatchString(lower) {
		return true
	}
	// Combined per-turn tool-activity summary, truncation header, per-file read
	// narration — all are tool transcript, never response prose.
	if cursorMultiToolSummaryLine.MatchString(lower) ||
		cursorEarlierHiddenLine.MatchString(lower) ||
		cursorReadFileLine.MatchString(lower) {
		return true
	}
	// Shell-tool command echo: starts with "$ " and ends with a duration
	// suffix like "407ms" or "1.2s" — distinguishes the tool transcript from a
	// markdown code block that happens to begin with "$".
	if strings.HasPrefix(trimmed, "$ ") && cursorShellEchoSuffix.MatchString(lower) {
		return true
	}
	// Truncation marker that closes a tool-output block:
	//   "… truncated (36 more lines) · ctrl+o to expand"
	// (Already filtered by isCursorTUILine's " · " rule in the common case;
	// handle the no-middot variant defensively.)
	if strings.Contains(lower, "truncated") &&
		(strings.Contains(lower, "more lines") || strings.Contains(lower, "more line")) {
		return true
	}
	return false
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
	// Preserve raw lines (including blank-line paragraph markers) for the
	// returned text, and compute a parallel non-empty view for prompt matching.
	rawLines := strings.Split(text, "\n")
	textNonEmpty := make([]string, 0, len(rawLines))
	rawIndexFor := make([]int, 0, len(rawLines))
	for i, line := range rawLines {
		if strings.TrimSpace(line) != "" {
			textNonEmpty = append(textNonEmpty, line)
			rawIndexFor = append(rawIndexFor, i)
		}
	}
	promptLines := nonEmptyCursorLines(prompt)
	if len(textNonEmpty) == 0 || len(promptLines) == 0 {
		return text
	}
	bestStart := -1
	bestLen := 0
	for start := 0; start < len(textNonEmpty) && start < 64; start++ {
		for promptStart := 0; promptStart < len(promptLines); promptStart++ {
			matchLen := 0
			for start+matchLen < len(textNonEmpty) &&
				promptStart+matchLen < len(promptLines) &&
				cursorPromptLinesEqual(textNonEmpty[start+matchLen], promptLines[promptStart+matchLen]) {
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
	// Translate the non-empty match span back to raw-line indices and drop it
	// (keeps paragraph-break blank lines outside the prompt block intact).
	startRaw := rawIndexFor[bestStart]
	endRaw := rawIndexFor[bestStart+bestLen-1] + 1
	out := make([]string, 0, len(rawLines))
	out = append(out, rawLines[:startRaw]...)
	out = append(out, rawLines[endRaw:]...)
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
	// Live generation signals (composing spinner, ctrl+c to stop) mean the
	// turn is still in progress — never treat as ready.
	if hasCursorLiveGenerationActivity(cleaned) {
		return false
	}
	// Cursor leaves stale status lines (Running..., Thinking...) in the pane
	// after a tool finishes. Once the → prompt is visible, stale activity
	// text should not keep the turn open forever.
	if hasCursorActivity(captured) && !strings.Contains(cleaned, "→") {
		return false
	}
	return true
}

func hasCursorLiveGenerationActivity(cleaned string) bool {
	return strings.Contains(cleaned, "ctrl+c to stop") ||
		strings.Contains(cleaned, "composing")
}

func hasCursorReadyMarker(cleaned string) bool {
	// The → arrow is Cursor's structural input cursor — the most reliable
	// signal that the prompt area is visible, regardless of placeholder text.
	for _, line := range strings.Split(cleaned, "\n") {
		if strings.Contains(strings.TrimSpace(line), "→") {
			return true
		}
	}
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
			strings.Contains(lower, "ctrl+c to stop") ||
			strings.Contains(lower, "composing") ||
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
	snapshot, err := captureCursorPaneForDisplay(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripCursorANSI(snapshot), "\n")
	if strings.TrimSpace(snapshot) == "" || snapshot == *lastTerminalSnapshot {
		return false
	}
	*lastTerminalSnapshot = snapshot
	select {
	case streamChan <- llmtypes.StreamChunk{
		Type:    llmtypes.StreamChunkTypeTerminal,
		Content: snapshot,
		Metadata: map[string]interface{}{
			"tmux_session":               sessionName,
			"cursor_interactive_session": sessionName,
		},
	}:
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
	// Only trim tmux's external scrollback to bound memory growth. We
	// intentionally do NOT send C-l (0x0C) anymore: Cursor's raw-mode TUI
	// catches that keystroke as "clear display", which wipes the visible
	// chat history the operator is watching in the browser terminal pane.
	// Baseline-diff logic in cursorCapturedAfterBaseline tolerates an
	// already-populated pane via LastIndex(captured, baseline).
	_ = runCursorCommand(ctx, nil, "tmux", "clear-history", "-t", sessionName)
}

func captureCursorPane(ctx context.Context, sessionName string) (string, error) {
	return runCursorCommandOutput(ctx, nil, "tmux", "capture-pane", "-p", "-J", "-S", "-3000", "-t", sessionName)
}

func captureCursorPaneForDisplay(ctx context.Context, sessionName string) (string, error) {
	return runCursorCommandOutput(ctx, nil, "tmux", "capture-pane", "-p", "-S", "-3000", "-t", sessionName)
}

func cursorCapturedAfterBaseline(captured, baseline string) string {
	if baseline == "" {
		return captured
	}
	// Fast path: exact substring match.
	if idx := strings.LastIndex(captured, baseline); idx >= 0 {
		return captured[idx+len(baseline):]
	}
	// Non-breaking space normalization (matches Claude Code adapter).
	normalizedCaptured := strings.ReplaceAll(captured, " ", " ")
	normalizedBaseline := strings.ReplaceAll(baseline, " ", " ")
	if idx := strings.LastIndex(normalizedCaptured, normalizedBaseline); idx >= 0 {
		return normalizedCaptured[idx+len(normalizedBaseline):]
	}
	// Line-based prefix divergence: on multi-turn panes the baseline and
	// captured share the same top lines (header + earlier turns) but diverge
	// where the new turn starts. Find the first line that differs and
	// return everything from that point onward.
	return cursorLinePrefixDelta(normalizedCaptured, normalizedBaseline)
}

func cursorLinePrefixDelta(captured, baseline string) string {
	capturedLines := strings.Split(captured, "\n")
	baselineLines := strings.Split(baseline, "\n")
	maxCompare := len(capturedLines)
	if len(baselineLines) < maxCompare {
		maxCompare = len(baselineLines)
	}
	divergeAt := 0
	for i := 0; i < maxCompare; i++ {
		if strings.TrimSpace(capturedLines[i]) != strings.TrimSpace(baselineLines[i]) {
			break
		}
		divergeAt = i + 1
	}
	// Require at least a few matching lines to trust the prefix.
	if divergeAt < 3 {
		return captured
	}
	return strings.Join(capturedLines[divergeAt:], "\n")
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
	return cursorDurationFromEnvAllowZero(EnvCursorInteractiveTimeoutSeconds, defaultCursorInteractiveTimeout)
}

func cursorInteractiveCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := cursorInteractiveTimeout()
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func cursorInteractiveIdleTimeout() time.Duration {
	return cursorDurationFromEnv(EnvCursorInteractiveIdleTimeoutSeconds, defaultCursorInteractiveIdleTimeout)
}

func cursorInteractiveRetention() time.Duration {
	return tmuxlaunch.Retention(defaultCursorInteractiveRetention)
}

func cursorInteractivePromptWait() time.Duration {
	return tmuxlaunch.PromptWait(EnvCursorInteractivePromptWaitSeconds)
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

func cursorDurationFromEnvAllowZero(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
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
