package agycli

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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/paneview"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxexec"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

const (
	// Default to no provider-level turn timeout. Workflow/background callers own
	// their execution deadline; the adapter should not cancel a still-running tmux
	// coding agent before the outer workflow timeout.
	defaultAgyInteractiveTimeout     = 0
	defaultAgyInteractiveIdleTimeout = 20 * time.Minute
	defaultAgyInteractiveRetention   = 30 * time.Minute
	agyInteractiveStableWindow       = 1200 * time.Millisecond
	// Number of paste+verify attempts before declaring the prompt input lost.
	// A not-yet-ready pane swallows the first paste; a retry typically lands it.
	agyPasteMaxAttempts = 3
	// Hard cap on how long we wait for the CLI to show ANY activity after the
	// prompt is submitted. A live agy turn shows "Thinking…"/streaming within
	// seconds; if nothing appears within this window the input never reached the
	// pane (e.g. paste/Enter was swallowed during launch), and without this cap
	// the response loop would spin forever because every completion/failsafe
	// branch is gated behind sawActivity. Generous so it never trips a real turn.
	defaultAgyInteractiveFirstActivityTimeout = 90 * time.Second
	// Backstop for a turn that produced activity and then went completely
	// silent — the pane is byte-for-byte unchanged for this long — but
	// hasAgyReadyPrompt never reported ready (e.g. a prompt-detection bug like a
	// stale "○ " tool card or a leftover spinner frame holding the turn "not
	// ready" forever). Without this, the loop spins indefinitely because the
	// default turn timeout is 0. When it trips we extract whatever response text
	// is on the pane and return it (or fail if empty) instead of hanging. Set
	// generously so a genuinely slow-but-alive turn (whose pane keeps changing)
	// never trips it — only a frozen pane does.
	defaultAgyInteractiveStalePaneBackstop = 120 * time.Second

	EnvAgyInteractiveSessionPrefix               = "AGY_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvAgyInteractiveTimeoutSeconds              = "AGY_CLI_INTERACTIVE_TIMEOUT_SECONDS"
	EnvAgyInteractiveIdleTimeoutSeconds          = "AGY_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvAgyInteractivePromptWaitSeconds           = "AGY_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvAgyInteractiveStreamTmuxScreen            = "AGY_CLI_STREAM_TMUX_SCREEN"
	EnvAgyInteractiveFirstActivityTimeoutSeconds = "AGY_CLI_INTERACTIVE_FIRST_ACTIVITY_TIMEOUT_SECONDS"
	EnvAgyInteractiveStalePaneBackstopSeconds    = "AGY_CLI_INTERACTIVE_STALE_PANE_BACKSTOP_SECONDS"
)

type agyInteractiveSession struct {
	ownerSessionID       string
	tmuxSessionName      string
	workingDir           string
	persistent           bool
	cleanupFiles         func()
	releaseMCPLease      func()
	idleTimer            *time.Timer
	initErr              error
	createdAt            time.Time
	lastUsed             time.Time
	modelID              string
	statuslineConfigured bool
	mu                   sync.Mutex
}

var agyInteractiveRegistry = struct {
	sync.RWMutex
	sessions map[string]string
}{
	sessions: map[string]string{},
}

var agyActiveSessionsRegistry = struct {
	sync.RWMutex
	workingDirs map[string]string
}{
	workingDirs: map[string]string{},
}

func registerAgyActiveSessionWorkingDir(tmuxSessionName, workingDir string) {
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	workingDir = strings.TrimSpace(workingDir)
	if tmuxSessionName == "" || workingDir == "" {
		return
	}
	agyActiveSessionsRegistry.Lock()
	defer agyActiveSessionsRegistry.Unlock()
	agyActiveSessionsRegistry.workingDirs[tmuxSessionName] = workingDir
}

func unregisterAgyActiveSessionWorkingDir(tmuxSessionName string) {
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if tmuxSessionName == "" {
		return
	}
	agyActiveSessionsRegistry.Lock()
	defer agyActiveSessionsRegistry.Unlock()
	delete(agyActiveSessionsRegistry.workingDirs, tmuxSessionName)
}

func getAgyActiveSessionWorkingDir(tmuxSessionName string) string {
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if tmuxSessionName == "" {
		return ""
	}
	agyActiveSessionsRegistry.RLock()
	defer agyActiveSessionsRegistry.RUnlock()
	return agyActiveSessionsRegistry.workingDirs[tmuxSessionName]
}

var agyPersistentRegistry = struct {
	sync.Mutex
	sessions map[string]*agyInteractiveSession
}{
	sessions: map[string]*agyInteractiveSession{},
}

var agyWorkspaceMCPConfigRegistry = struct {
	sync.Mutex
	leases map[string]map[*agyInteractiveSession]string
}{
	leases: map[string]map[*agyInteractiveSession]string{},
}

var agyStatuslineCaptureMu sync.Mutex

var errAgyAuthRequired = errors.New("Antigravity CLI authentication required")

func (c *AgyCLIAdapter) generateContentTmux(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; agy-cli tmux mode requires tmux")
	}
	if _, err := exec.LookPath("agy"); err != nil {
		return nil, fmt.Errorf("agy not found in PATH. Install Antigravity CLI with `curl https://agy.com/install -fsS | bash`")
	}

	persistent := agyPersistentInteractiveFromOptions(opts)
	ownerSessionID := agyInteractiveSessionIDFromOptions(opts)
	if ownerSessionID == "" {
		ownerSessionID = "agy-bounded-" + agyRandomHex(8)
	}
	callCtx, cancel := agyInteractiveCallContext(ctx)
	defer cancel()

	// On user-initiated cancellation, tear down the persistent tmux
	// session so the live pane closes alongside the workflow step.
	defer func() {
		if ctx.Err() != context.Canceled {
			return
		}
		closeAgyPersistentSession(ownerSessionID, "workflow context canceled", c.logger)
	}()

	systemPrompt, conversationMessages := splitAgySystemPrompt(messages)
	historicalAssistantTexts := agyAssistantHistory(conversationMessages)
	launchOnly := llmtypes.CodingProviderLaunchOnlyFromOptions(opts)
	prompt := buildAgyPrompt(conversationMessages)
	// JSON Schema structured output: agy-cli has no flag equivalent to
	// claude-code's --json-schema, so we append the schema to the prompt
	// with explicit instructions. Same prompt-appended fallback used by
	// claude-code's interactive adapter and the gemini / codex / cursor /
	// other coding-agent adapters.
	if opts != nil && opts.JSONSchema != nil && opts.JSONSchema.Schema != nil {
		schemaBytes, err := json.Marshal(opts.JSONSchema.Schema)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON schema: %w", err)
		}
		var b strings.Builder
		b.WriteString(prompt)
		if prompt != "" && !strings.HasSuffix(prompt, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\nReturn a response that conforms to this JSON schema:\n")
		b.Write(schemaBytes)
		b.WriteString("\n")
		prompt = b.String()
	}
	if !launchOnly && strings.TrimSpace(prompt) == "" {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("agy-cli prompt is empty")
	}

	session, err := c.acquireAgyInteractiveSession(callCtx, ownerSessionID, persistent, opts, systemPrompt)
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
			releaseAgyInteractiveSession(session, c.logger)
		} else {
			releaseAgyBoundedInteractiveSession(session, c.logger)
		}
	}()

	if err := waitForAgyPrompt(callCtx, session.tmuxSessionName, opts.StreamChan); err != nil {
		markAgyInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedAgyInteractiveSession(failedSession)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	resetAgyPaneForTurn(callCtx, session.tmuxSessionName)
	if err := waitForAgyPrompt(callCtx, session.tmuxSessionName, opts.StreamChan); err != nil {
		markAgyInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedAgyInteractiveSession(failedSession)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	if err := clearAgyPromptDraftBeforePaste(callCtx, session.tmuxSessionName); err != nil {
		markAgyInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedAgyInteractiveSession(failedSession)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	if !session.statuslineConfigured {
		if err := configureAgyStatusline(callCtx, session, c.logger); err == nil {
			session.statuslineConfigured = true
		} else if c.logger != nil {
			c.logger.Debugf("Failed to configure Antigravity CLI statusline: %v", err)
		}
	}

	if launchOnly {
		var lastSnapshot string
		streamAgyTerminalSnapshot(callCtx, session.tmuxSessionName, opts.StreamChan, &lastSnapshot)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		nativeSessionID := firstNonEmptyAgy(agyResumeSessionIDFromOptions(opts), waitForAgyConversationID(callCtx, session.workingDir, ""))
		additional := map[string]interface{}{
			"provider":                   "agy-cli",
			"agy_mode":                   "tmux",
			"agy_interactive_session":    session.tmuxSessionName,
			"agy_persistent_interactive": persistent,
			"agy_uses_print_json":        false,
			"agy_working_dir":            session.workingDir,
		}
		if nativeSessionID != "" {
			additional["agy_session_id"] = nativeSessionID
		}
		gi := &llmtypes.GenerationInfo{Additional: additional}
		llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
			Provider:        "agy-cli",
			Transport:       llmtypes.CodingProviderTransportTmux,
			NativeSessionID: nativeSessionID,
			TmuxSession:     session.tmuxSessionName,
			WorkingDir:      session.workingDir,
			Model:           c.modelID,
			Status:          llmtypes.CodingProviderSessionStatusIdle,
		})
		return &llmtypes.ContentResponse{
			Choices: []*llmtypes.ContentChoice{{
				Content:        "",
				GenerationInfo: gi,
			}},
		}, nil
	}

	baseline, _ := captureAgyPane(callCtx, session.tmuxSessionName)
	c.logInfof("Executing Antigravity CLI tmux session: %s", session.tmuxSessionName)
	if err := sendAgyInputToTmux(callCtx, session.tmuxSessionName, prompt); err != nil {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	captured, err := waitForAgyInteractiveResponse(callCtx, session.tmuxSessionName, baseline, prompt, historicalAssistantTexts, opts.StreamChan, agyAutoApproveWebSearchFromOptions(opts))
	forcedComplete := errors.Is(err, tmuxcontrol.ErrForceComplete)
	if err != nil && !forcedComplete {
		if isAgyTmuxSessionLostError(err) {
			markAgyInteractiveSessionFailedLocked(session, err, c.logger)
			releaseSession = false
			failedSession := session
			session.mu.Unlock()
			session = nil
			cleanupFailedAgyInteractiveSession(failedSession)
		} else if ctx.Err() != nil {
			interruptAgyInteractiveSession(session.tmuxSessionName, c.logger)
		}
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}

	content := parseAgyInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	if forcedComplete && strings.TrimSpace(content) == "" {
		content = forcedAgyInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	}
	// Trailing-capture grace window — see llmtypes.RunTrailingPaneCapture.
	llmtypes.RunTrailingPaneCapture(callCtx, opts.StreamChan,
		func(ctx context.Context) (string, error) {
			snap, err := captureAgyPane(ctx, session.tmuxSessionName)
			if err != nil {
				return "", err
			}
			return strings.TrimRight(stripAgyANSI(snap), "\n"), nil
		},
		map[string]interface{}{
			"tmux_session":            session.tmuxSessionName,
			"agy_interactive_session": session.tmuxSessionName,
		},
	)
	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	additional := map[string]interface{}{
		"provider":                   "agy-cli",
		"agy_mode":                   "tmux",
		"agy_interactive_session":    session.tmuxSessionName,
		"agy_persistent_interactive": persistent,
		"agy_uses_print_json":        false,
		"agy_working_dir":            session.workingDir,
	}
	nativeSessionID := agyResumeSessionIDFromOptions(opts)
	if nativeSessionID == "" {
		nativeSessionID = waitForAgyConversationID(callCtx, session.workingDir, prompt)
	}
	if nativeSessionID != "" {
		additional["agy_session_id"] = nativeSessionID
	}
	if !persistent {
		// terminal_retention_seconds intentionally not set: the rail
		// snapshot stays read-only until the user dismisses it via the
		// X button. Tmux itself is killed quickly after the turn via
		// the bounded-session cleanup using llmtypes.TmuxKillDelay.
		// The agy_interactive_retention_seconds value is kept for
		// any backend code that still tracks it for diagnostics.
		additional["agy_interactive_retention_seconds"] = int(agyInteractiveRetention().Seconds())
	}

	inputTokens, outputTokens, cacheReadTokens := agyTmuxTokenCounts(prompt, content, nil, additional)
	if !persistent {
		if statuslineUsage, ok := captureAgyStatuslineUsageAtIdle(callCtx, session, c.logger); ok {
			inputTokens, outputTokens, cacheReadTokens = agyTmuxTokenCounts(prompt, content, &statuslineUsage, additional)
		}
	}
	totalTokens := inputTokens + outputTokens
	if cacheReadTokens > 0 {
		totalTokens += cacheReadTokens
	}
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:  intPtrFromInt(inputTokens),
		OutputTokens: intPtrFromInt(outputTokens),
		TotalTokens:  intPtrFromInt(totalTokens),
		Additional:   additional,
	}
	if cacheReadTokens > 0 {
		genInfo.CachedContentTokens = intPtrFromInt(cacheReadTokens)
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

	llmtypes.AttachCodingProviderIntermediateMessages(genInfo, llmtypes.CodingProviderIntermediateMessages{
		Provider:  "agy-cli",
		Transport: llmtypes.CodingProviderTransportTmux,
		Messages: []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: content}}},
		},
	})
	llmtypes.AttachCodingProviderSessionHandle(genInfo, llmtypes.CodingProviderSessionHandle{
		Provider:        "agy-cli",
		Transport:       llmtypes.CodingProviderTransportTmux,
		NativeSessionID: nativeSessionID,
		TmuxSession:     session.tmuxSessionName,
		WorkingDir:      session.workingDir,
		Model:           c.modelID,
		Status:          llmtypes.CodingProviderSessionStatusIdle,
	})

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

type agyStatuslineUsage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

func agyTmuxTokenCounts(prompt, content string, statuslineUsage *agyStatuslineUsage, additional map[string]interface{}) (int, int, int) {
	if statuslineUsage != nil {
		estimatedInput, estimatedOutput := estimateAgyTmuxTokens(prompt, content)
		inputTokens := statuslineUsage.InputTokens + statuslineUsage.CacheCreationInputTokens
		outputTokens := statuslineUsage.OutputTokens
		if inputTokens <= 0 {
			inputTokens = estimatedInput
		}
		if outputTokens <= 0 {
			outputTokens = estimatedOutput
		}
		additional["agy_token_usage_source"] = "statusline"
		additional["agy_statusline_input_tokens"] = statuslineUsage.InputTokens
		additional["agy_statusline_output_tokens"] = statuslineUsage.OutputTokens
		if statuslineUsage.CacheCreationInputTokens > 0 {
			additional["cache_creation_input_tokens"] = statuslineUsage.CacheCreationInputTokens
		}
		if statuslineUsage.CacheReadInputTokens > 0 {
			additional["cache_read_input_tokens"] = statuslineUsage.CacheReadInputTokens
		}
		return inputTokens, outputTokens, statuslineUsage.CacheReadInputTokens
	}
	inputTokens, outputTokens := estimateAgyTmuxTokens(prompt, content)
	additional["agy_token_usage_source"] = "estimated"
	return inputTokens, outputTokens, 0
}

// estimateAgyTmuxTokens returns (input, output) token counts estimated
// from prompt/response character lengths. Agy falls back to this when the
// bounded-session statusline snapshot is unavailable, and for persistent
// sessions where mutating the user's live statusline would be surprising.
// The 4-chars-per-token heuristic matches what other tmux adapters fall
// back to when their CLI's JSON side-stream is unavailable.
func estimateAgyTmuxTokens(prompt, content string) (int, int) {
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

func intPtrFromInt(v int) *int {
	return &v
}

type agySettingsSnapshot struct {
	path    string
	exists  bool
	body    []byte
	mode    os.FileMode
	restore bool
}

func captureAgyStatuslineUsageAtIdle(ctx context.Context, session *agyInteractiveSession, logger interfaces.Logger) (agyStatuslineUsage, bool) {
	if session == nil || strings.TrimSpace(session.tmuxSessionName) == "" {
		return agyStatuslineUsage{}, false
	}

	agyStatuslineCaptureMu.Lock()
	defer agyStatuslineCaptureMu.Unlock()

	captureCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	snapshot, err := snapshotAgySettingsFile()
	if err != nil {
		logAgyStatuslineUsageCaptureError(logger, "snapshot settings", err)
		return agyStatuslineUsage{}, false
	}
	tempDir := ""
	defer func() {
		if snapshot.restore {
			if err := restoreAgySettingsFile(snapshot); err != nil {
				logAgyStatuslineUsageCaptureError(logger, "restore settings", err)
			}
		}
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
	}()

	if err := waitForAgyPrompt(captureCtx, session.tmuxSessionName, nil); err != nil {
		logAgyStatuslineUsageCaptureError(logger, "wait for prompt", err)
		return agyStatuslineUsage{}, false
	}

	if session.persistent {
		tempDir, err = os.MkdirTemp("/tmp", "agy-statusline-usage-*")
	} else {
		tempDir, err = os.MkdirTemp(session.workingDir, "agy-statusline-usage-*")
	}
	if err != nil {
		logAgyStatuslineUsageCaptureError(logger, "create temp dir", err)
		return agyStatuslineUsage{}, false
	}
	outputPath := filepath.Join(tempDir, "usage.json")
	scriptPath := filepath.Join(tempDir, "capture.sh")
	if err := os.WriteFile(scriptPath, []byte(buildAgyStatuslineCaptureScript(outputPath)), 0o700); err != nil {
		logAgyStatuslineUsageCaptureError(logger, "write capture script", err)
		return agyStatuslineUsage{}, false
	}

	command := "/statusline sh " + agyShellQuote(scriptPath)
	if err := typeAgyInputToTmux(captureCtx, session.tmuxSessionName, command); err != nil {
		logAgyStatuslineUsageCaptureError(logger, "submit statusline command", err)
		return agyStatuslineUsage{}, false
	}
	raw, err := waitForAgyStatuslineUsageFile(captureCtx, outputPath)
	if err != nil {
		logAgyStatuslineUsageCaptureError(logger, "read statusline usage", err)
		return agyStatuslineUsage{}, false
	}
	usage, ok := parseAgyStatuslineUsageJSON(raw)
	if !ok {
		logAgyStatuslineUsageCaptureError(logger, "parse statusline usage", fmt.Errorf("no token counts in statusline payload"))
		return agyStatuslineUsage{}, false
	}
	return usage, true
}

func snapshotAgySettingsFile() (agySettingsSnapshot, error) {
	path := filepath.Join(agyAppDataDir(), "settings.json")
	snapshot := agySettingsSnapshot{path: path, restore: true, mode: 0o600}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return snapshot, nil
		}
		return snapshot, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return snapshot, err
	}
	snapshot.exists = true
	snapshot.body = body
	snapshot.mode = info.Mode().Perm()
	if snapshot.mode == 0 {
		snapshot.mode = 0o600
	}
	return snapshot, nil
}

func restoreAgySettingsFile(snapshot agySettingsSnapshot) error {
	if snapshot.path == "" {
		return nil
	}
	if snapshot.exists {
		if err := os.MkdirAll(filepath.Dir(snapshot.path), 0o755); err != nil {
			return err
		}
		mode := snapshot.mode
		if mode == 0 {
			mode = 0o600
		}
		return os.WriteFile(snapshot.path, snapshot.body, mode)
	}
	if err := os.Remove(snapshot.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func buildAgyStatuslineCaptureScript(outputPath string) string {
	quotedOutputPath := agyShellQuote(outputPath)
	return `#!/bin/sh
out=` + quotedOutputPath + `
payload=$(cat)
input_tokens=$(printf '%s\n' "$payload" | sed -n 's/.*"current_usage"[[:space:]]*:[[:space:]]*{[^}]*"input_tokens"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | sed -n '1p')
output_tokens=$(printf '%s\n' "$payload" | sed -n 's/.*"current_usage"[[:space:]]*:[[:space:]]*{[^}]*"output_tokens"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | sed -n '1p')
cache_creation_input_tokens=$(printf '%s\n' "$payload" | sed -n 's/.*"current_usage"[[:space:]]*:[[:space:]]*{[^}]*"cache_creation_input_tokens"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | sed -n '1p')
cache_read_input_tokens=$(printf '%s\n' "$payload" | sed -n 's/.*"current_usage"[[:space:]]*:[[:space:]]*{[^}]*"cache_read_input_tokens"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | sed -n '1p')
total_input_tokens=$(printf '%s\n' "$payload" | sed -n 's/.*"total_input_tokens"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | sed -n '1p')
total_output_tokens=$(printf '%s\n' "$payload" | sed -n 's/.*"total_output_tokens"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | sed -n '1p')
: "${input_tokens:=0}"
: "${output_tokens:=0}"
: "${cache_creation_input_tokens:=0}"
: "${cache_read_input_tokens:=0}"
: "${total_input_tokens:=0}"
: "${total_output_tokens:=0}"
tmp="${out}.$$"
printf '{"input_tokens":%s,"output_tokens":%s,"cache_creation_input_tokens":%s,"cache_read_input_tokens":%s,"total_input_tokens":%s,"total_output_tokens":%s}\n' "$input_tokens" "$output_tokens" "$cache_creation_input_tokens" "$cache_read_input_tokens" "$total_input_tokens" "$total_output_tokens" > "$tmp"
mv "$tmp" "$out"
`
}

func waitForAgyStatuslineUsageFile(ctx context.Context, path string) ([]byte, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		raw, err := os.ReadFile(path)
		if err == nil && len(bytes.TrimSpace(raw)) > 0 {
			return raw, nil
		}
		if err != nil && !os.IsNotExist(err) {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, errors.Join(ctx.Err(), fmt.Errorf("last read error: %w", lastErr))
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func parseAgyStatuslineUsageJSON(raw []byte) (agyStatuslineUsage, bool) {
	type currentUsage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	}
	var payload struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		TotalInputTokens         int `json:"total_input_tokens"`
		TotalOutputTokens        int `json:"total_output_tokens"`
		ContextWindow            struct {
			TotalInputTokens  int          `json:"total_input_tokens"`
			TotalOutputTokens int          `json:"total_output_tokens"`
			CurrentUsage      currentUsage `json:"current_usage"`
		} `json:"context_window"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		return agyStatuslineUsage{}, false
	}

	usage := agyStatuslineUsage{
		InputTokens:              payload.InputTokens,
		OutputTokens:             payload.OutputTokens,
		CacheCreationInputTokens: payload.CacheCreationInputTokens,
		CacheReadInputTokens:     payload.CacheReadInputTokens,
	}
	current := payload.ContextWindow.CurrentUsage
	if current.InputTokens > 0 || current.OutputTokens > 0 || current.CacheCreationInputTokens > 0 || current.CacheReadInputTokens > 0 {
		usage = agyStatuslineUsage{
			InputTokens:              current.InputTokens,
			OutputTokens:             current.OutputTokens,
			CacheCreationInputTokens: current.CacheCreationInputTokens,
			CacheReadInputTokens:     current.CacheReadInputTokens,
		}
	}
	if usage.InputTokens <= 0 {
		if payload.ContextWindow.TotalInputTokens > 0 {
			usage.InputTokens = payload.ContextWindow.TotalInputTokens
		} else if payload.TotalInputTokens > 0 {
			usage.InputTokens = payload.TotalInputTokens
		}
	}
	if usage.OutputTokens <= 0 {
		if payload.ContextWindow.TotalOutputTokens > 0 {
			usage.OutputTokens = payload.ContextWindow.TotalOutputTokens
		} else if payload.TotalOutputTokens > 0 {
			usage.OutputTokens = payload.TotalOutputTokens
		}
	}
	if usage.InputTokens <= 0 && usage.OutputTokens <= 0 && usage.CacheCreationInputTokens <= 0 && usage.CacheReadInputTokens <= 0 {
		return agyStatuslineUsage{}, false
	}
	return usage, true
}

func logAgyStatuslineUsageCaptureError(logger interfaces.Logger, stage string, err error) {
	if logger == nil || err == nil {
		return
	}
	logger.Debugf("Agy statusline usage capture skipped at %s: %v", stage, err)
}

// acquireAgyInteractiveSession returns with session.mu held.
func (c *AgyCLIAdapter) acquireAgyInteractiveSession(ctx context.Context, ownerSessionID string, persistent bool, opts *llmtypes.CallOptions, systemPrompt string) (*agyInteractiveSession, error) {
	if persistent {
		agyPersistentRegistry.Lock()
		existing := agyPersistentRegistry.sessions[ownerSessionID]
		if existing != nil {
			// Release the registry (map) lock BEFORE taking the per-session lock.
			// session.mu is held for a whole turn; holding the global map lock
			// across it stalls every other acquire behind a busy session
			// (lock-held-across-blocking-call deadlock).
			agyPersistentRegistry.Unlock()
			existing.mu.Lock()
			if existing.initErr != nil {
				err := existing.initErr
				existing.mu.Unlock()
				return nil, err
			}
			if existing.idleTimer != nil {
				existing.idleTimer.Stop()
				existing.idleTimer = nil
			}
			existing.lastUsed = time.Now()
			registerAgyActiveSessionWorkingDir(existing.tmuxSessionName, existing.workingDir)
			return existing, nil
		}
	}

	now := time.Now()
	session := &agyInteractiveSession{
		ownerSessionID:  ownerSessionID,
		tmuxSessionName: newAgyTmuxSessionName(),
		persistent:      persistent,
		createdAt:       now,
		lastUsed:        now,
		modelID:         c.modelID,
	}
	session.mu.Lock()
	if persistent {
		agyPersistentRegistry.sessions[ownerSessionID] = session
		agyPersistentRegistry.Unlock()
	}

	args, env, workingDir, cleanupFiles, err := c.buildAgyInteractiveLaunch(opts, systemPrompt, ownerSessionID)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		if persistent {
			removeAgyPersistentSession(ownerSessionID, session)
		}
		return nil, err
	}
	session.workingDir = workingDir
	session.cleanupFiles = cleanupFiles
	releaseMCPLease, err := acquireAgyWorkspaceMCPConfigLease(workingDir, opts, session)
	if err != nil {
		session.initErr = err
		if cleanupFiles != nil {
			cleanupFiles()
		}
		session.mu.Unlock()
		if persistent {
			removeAgyPersistentSession(ownerSessionID, session)
		}
		return nil, err
	}
	session.releaseMCPLease = releaseMCPLease

	if err := startAgyTmuxSession(ctx, session.tmuxSessionName, args, env, workingDir); err != nil {
		session.initErr = err
		if cleanupFiles != nil {
			cleanupFiles()
		}
		if releaseMCPLease != nil {
			releaseMCPLease()
			session.releaseMCPLease = nil
		}
		session.mu.Unlock()
		if persistent {
			removeAgyPersistentSession(ownerSessionID, session)
		}
		return nil, err
	}
	registerAgyActiveSessionWorkingDir(session.tmuxSessionName, session.workingDir)
	registerAgyInteractiveSession(ownerSessionID, session.tmuxSessionName)
	return session, nil
}

func (c *AgyCLIAdapter) buildAgyInteractiveLaunch(opts *llmtypes.CallOptions, systemPrompt string, ownerSessionID string) ([]string, []string, string, func(), error) {
	workingDir := agyWorkingDirFromOptions(opts)
	if workingDir == "" {
		workingDir = agyMustGetwd()
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, nil, "", nil, fmt.Errorf("failed to create Antigravity CLI working directory: %w", err)
	}

	// Project attached skills before launching the CLI so agy can
	// discover them in .agents/skills/ at startup. Non-fatal: skills
	// are useful but not load-bearing — the agent can still run
	// without disk projection, and the listing is also present in the
	// system prompt via mcpagent's ensureSystemPrompt.
	if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
		_ = c.ProjectSkills(workingDir, skills)
	}

	cleanupFiles, err := prepareAgyProjectFiles(workingDir, systemPrompt, opts, ownerSessionID)
	if err != nil {
		return nil, nil, "", nil, err
	}

	args := []string{"agy"}
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if enabled, ok := opts.Metadata.Custom["agy_dangerously_skip_permissions"].(bool); !ok || enabled {
			args = append(args, "--dangerously-skip-permissions")
		}
		if sandbox, ok := opts.Metadata.Custom["agy_sandbox"].(string); ok && strings.TrimSpace(sandbox) != "" {
			args = append(args, "--sandbox", strings.TrimSpace(sandbox))
		}
		if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok && strings.TrimSpace(resumeID) != "" {
			args = append(args, "--conversation", strings.TrimSpace(resumeID))
		}
	}
	args = append(args, "--add-dir", workingDir, "--prompt-interactive", "")

	env := []string{}
	if strings.TrimSpace(c.apiKey) != "" {
		apiKey := strings.TrimSpace(c.apiKey)
		env = append(env,
			"AGY_API_KEY="+apiKey,
			"GOOGLE_API_KEY="+apiKey,
			"GEMINI_API_KEY="+apiKey,
		)
	}
	return args, env, workingDir, cleanupFiles, nil
}

func prepareAgyProjectFiles(workingDir, systemPrompt string, opts *llmtypes.CallOptions, ownerSessionID string) (func(), error) {
	cleanups := make([]func(), 0, 2)
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

	agentsDir := filepath.Join(workingDir, ".agents")
	if strings.TrimSpace(systemPrompt) != "" {
		rulesDir := filepath.Join(agentsDir, "rules")
		if err := os.MkdirAll(rulesDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create Agy rules dir: %w", err)
		}
		// Fixed filename — only one agy chat owns a workflow folder at
		// a time, so no need to disambiguate via per-session hex. The
		// adapter's cleanup callback removes this file on session end;
		// if a session crashed and left it behind, the next session
		// overwrites it cleanly.
		rulePath := filepath.Join(rulesDir, "mlp-system.md")
		content := "# MCP Agent System Instructions\n\n" + strings.TrimSpace(systemPrompt) + "\n"
		if err := os.WriteFile(rulePath, []byte(content), 0o600); err != nil {
			cleanupAll()
			return nil, fmt.Errorf("failed to write Agy system rule: %w", err)
		}
		addCleanup(func() {
			_ = os.Remove(rulePath)
			_ = os.Remove(rulesDir)
			_ = os.Remove(agentsDir)
		})
	}

	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		restorePrior := agyRestoreProjectFilesFromOptions(opts)
		if mcpJSON, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpJSON) != "" {
			if !json.Valid([]byte(mcpJSON)) {
				cleanupAll()
				return nil, fmt.Errorf("agy MCP config is not valid JSON")
			}
			cleanup, err := writeAgyRestoredFile(filepath.Join(agentsDir, "mcp_config.json"), []byte(mcpJSON), restorePrior)
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
		}
		if enabled, ok := opts.Metadata.Custom[MetadataKeyBridgeOnlyTools].(bool); ok && enabled {
			cleanup, err := writeAgyBridgeOnlyHookFiles(agentsDir, restorePrior)
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
		}
	}

	// Final teardown: nuke the whole .agents/ tree. Registered LAST so it
	// fires FIRST in LIFO order, making the earlier per-file restore
	// callbacks no-ops on already-gone files. The intent is a clean wipe
	// between sessions — orphaned MCP config / hook files from a prior
	// session that didn't finish its cleanup callback (e.g. orchestrator
	// killed before closeAgyPersistentSession ran) would otherwise leak.
	// Trade-off: if the operator had their OWN content under .agents/
	// before our session, it is destroyed by this RemoveAll. That is
	// considered acceptable for now; the orchestrator manages this
	// directory as a session-scoped artifact.
	if strings.TrimSpace(workingDir) != "" {
		addCleanup(func() { _ = os.RemoveAll(agentsDir) })
	}

	return cleanupAll, nil
}

const agyBridgeOnlyDeniedToolMatcher = "Read|read|read_file|view_file|ListDir|list_dir|Search|search|grep_search|find_by_name|write_to_file|replace_file_content|multi_replace_file_content|run_command|manage_task|schedule|list_permissions|ask_permission|invoke_subagent|define_subagent|send_message|manage_subagents|ask_question|generate_image"

func writeAgyBridgeOnlyHookFiles(agentsDir string, restorePrior bool) (func(), error) {
	scriptPath := filepath.Join(agentsDir, "mlp-bridge-only-hook.sh")
	logPath := filepath.Join(agentsDir, "mlp-bridge-only-denials.jsonl")
	// Rich deny reason mirrors the cursor hook's agent_message: tell the
	// model EXACTLY which bridge tools are available so it doesn't give
	// up with "no MCP server configured" when its built-in is blocked.
	denyReason := "Antigravity built-in tools are DENIED in this session. You DO have full access via the api-bridge MCP server (your environment carries valid MCP_API_URL + MCP_API_TOKEN). Use these EXACT bridge tools — api-bridge.execute_shell_command(command, timeout?) for shell (cat, ls, jq, python3, curl); api-bridge.diff_patch_workspace_file(filepath, diff) for file edits (instead of write_to_file / replace_file_content); api-bridge.get_api_spec(server_name, tool_name) to discover other MCP servers (google_sheets, playwright). Custom tools are available through execute_shell_command + curl with $MCP_CUSTOM and $MCP_AUTH; for LLM/provider config use list_published_llms, list_provider_models, test_llm, save_published_llm, and set_provider_auth through $MCP_CUSTOM. Do not read or edit config/ files for LLM/provider configuration. Do NOT report 'no MCP server configuration' or 'no API tokens' — the bridge is configured. Always pick a bridge/custom-tool route over giving up."
	denyPayload, err := json.Marshal(map[string]string{
		"decision": "deny",
		"reason":   denyReason,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode Agy bridge-only deny payload: %w", err)
	}
	script := "#!/bin/sh\n" +
		"input=$(cat)\n" +
		"printf '%s\\n' \"$input\" >> " + agyShellQuote(logPath) + "\n" +
		"printf '%s\\n' " + agyShellQuote(string(denyPayload)) + "\n"
	scriptCleanup, err := writeAgyRestoredFile(scriptPath, []byte(script), restorePrior)
	if err != nil {
		return nil, err
	}

	hooksPath := filepath.Join(agentsDir, "hooks.json")
	hooksConfig := map[string]interface{}{}
	// Only merge with an operator's pre-existing hooks.json when restore is
	// opted in. With restore off (the default) we overwrite fresh: a clean
	// session-scoped hooks.json with just our bridge-only entry, deleted on
	// cleanup.
	if restorePrior {
		existingHooks, readErr := os.ReadFile(hooksPath)
		if readErr == nil && strings.TrimSpace(string(existingHooks)) != "" {
			if err := json.Unmarshal(existingHooks, &hooksConfig); err != nil {
				scriptCleanup()
				return nil, fmt.Errorf("failed to parse existing Agy hooks.json: %w", err)
			}
		} else if readErr != nil && !os.IsNotExist(readErr) {
			scriptCleanup()
			return nil, fmt.Errorf("failed to read existing Agy hooks.json: %w", readErr)
		}
	}
	hooksConfig["mlp-bridge-only-tools"] = map[string]interface{}{
		"PreToolUse": []map[string]interface{}{
			{
				"matcher": agyBridgeOnlyDeniedToolMatcher,
				"hooks": []map[string]interface{}{
					{
						"type":    "command",
						"command": "sh " + agyShellQuote(scriptPath),
						"timeout": 10,
					},
				},
			},
		},
	}
	hooksBody, err := json.MarshalIndent(hooksConfig, "", "  ")
	if err != nil {
		scriptCleanup()
		return nil, fmt.Errorf("failed to encode Agy bridge-only hooks: %w", err)
	}
	hooksBody = append(hooksBody, '\n')
	hooksCleanup, err := writeAgyRestoredFile(hooksPath, hooksBody, restorePrior)
	if err != nil {
		scriptCleanup()
		return nil, err
	}

	return func() {
		hooksCleanup()
		_ = os.Remove(logPath)
		scriptCleanup()
	}, nil
}

func writeAgyRestoredFile(path string, content []byte, restorePrior bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create Agy config dir: %w", err)
	}
	var previous []byte
	existed := false
	if restorePrior {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			previous, existed = data, true
		} else if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("failed to read existing Agy config %s: %w", path, readErr)
		}
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write Agy config %s: %w", path, err)
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

func readAgyConversationIDForTurn(workingDir, prompt string) string {
	workingDir = filepath.Clean(strings.TrimSpace(workingDir))
	if workingDir == "." || workingDir == "" {
		return ""
	}
	prompt = strings.TrimSpace(prompt)
	raw, err := os.ReadFile(agyHistoryPath())
	if err != nil {
		return ""
	}
	type historyEntry struct {
		Display        string `json:"display"`
		Workspace      string `json:"workspace"`
		ConversationID string `json:"conversationId"`
	}
	lines := bytes.Split(raw, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var entry historyEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if filepath.Clean(strings.TrimSpace(entry.Workspace)) != workingDir {
			continue
		}
		if prompt != "" && strings.TrimSpace(entry.Display) != prompt {
			continue
		}
		if id := strings.TrimSpace(entry.ConversationID); id != "" {
			return id
		}
	}
	return ""
}

func readAgyLatestConversationID(workingDir string) string {
	workingDir = filepath.Clean(strings.TrimSpace(workingDir))
	if workingDir == "." || workingDir == "" {
		return ""
	}
	raw, err := os.ReadFile(agyLastConversationsPath())
	if err != nil {
		return ""
	}
	var conversations map[string]string
	if err := json.Unmarshal(raw, &conversations); err != nil {
		return ""
	}
	for workspace, conversationID := range conversations {
		if filepath.Clean(strings.TrimSpace(workspace)) == workingDir {
			return strings.TrimSpace(conversationID)
		}
	}
	return ""
}

var agyConversationLogIDPattern = regexp.MustCompile(`(?:Created conversation|Streaming conversation|Forwarding user message to conversation|Sending user message to conversation) ([0-9a-fA-F-]{36})`)

func readAgyConversationIDFromLogs(workingDir string) string {
	workingDir = filepath.Clean(strings.TrimSpace(workingDir))
	if workingDir == "." || workingDir == "" {
		return ""
	}
	entries, err := os.ReadDir(agyLogDir())
	if err != nil {
		return ""
	}
	type logEntry struct {
		path    string
		modTime time.Time
	}
	logs := make([]logEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		logs = append(logs, logEntry{
			path:    filepath.Join(agyLogDir(), entry.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].modTime.After(logs[j].modTime)
	})
	for _, logFile := range logs {
		raw, err := os.ReadFile(logFile.path)
		if err != nil {
			continue
		}
		text := string(raw)
		if !strings.Contains(text, workingDir) {
			continue
		}
		matches := agyConversationLogIDPattern.FindAllStringSubmatch(text, -1)
		for i := len(matches) - 1; i >= 0; i-- {
			if len(matches[i]) > 1 {
				if id := strings.TrimSpace(matches[i][1]); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func agyLastConversationsPath() string {
	return filepath.Join(agyAppDataDir(), "cache", "last_conversations.json")
}

func agyHistoryPath() string {
	return filepath.Join(agyAppDataDir(), "history.jsonl")
}

func agyLogDir() string {
	return filepath.Join(agyAppDataDir(), "log")
}

func agyAppDataDir() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".gemini", "antigravity-cli")
	}
	return filepath.Join(".gemini", "antigravity-cli")
}

func firstNonEmptyAgy(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func waitForAgyConversationID(ctx context.Context, workingDir, prompt string) string {
	if id := firstNonEmptyAgy(readAgyConversationIDForTurn(workingDir, prompt), readAgyLatestConversationID(workingDir), readAgyConversationIDFromLogs(workingDir)); id != "" {
		return id
	}
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return ""
		case <-ticker.C:
			if id := firstNonEmptyAgy(readAgyConversationIDForTurn(workingDir, prompt), readAgyLatestConversationID(workingDir), readAgyConversationIDFromLogs(workingDir)); id != "" {
				return id
			}
		}
	}
}

func releaseAgyInteractiveSession(session *agyInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	session.lastUsed = time.Now()
	session.idleTimer = time.AfterFunc(agyInteractiveIdleTimeout(), func() {
		closeAgyPersistentSession(session.ownerSessionID, "idle timeout", logger)
	})
	session.mu.Unlock()
}

func releaseAgyBoundedInteractiveSession(session *agyInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	// Keep the real tmux pane alive for the shared bounded retention window so
	// the UI terminal remains inspectable/debuggable while it is visible.
	retention := llmtypes.TmuxKillDelay
	session.lastUsed = time.Now()
	if retention <= 0 {
		closeAgySessionLocked(session, "bounded turn complete", logger)
		return
	}
	if logger != nil {
		logger.Debugf("Retaining completed Agy interactive session %s for owner %s for %s (then kill)", session.tmuxSessionName, session.ownerSessionID, retention)
	}
	session.idleTimer = time.AfterFunc(retention, func() {
		closeAgyPersistentSession(session.ownerSessionID, "bounded retention elapsed", logger)
	})
	session.mu.Unlock()
}

func closeAgyPersistentSession(ownerSessionID, reason string, logger interfaces.Logger) {
	agyPersistentRegistry.Lock()
	session := agyPersistentRegistry.sessions[ownerSessionID]
	if session == nil {
		agyPersistentRegistry.Unlock()
		return
	}
	delete(agyPersistentRegistry.sessions, ownerSessionID)
	agyPersistentRegistry.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()
	closeAgySessionLocked(session, reason, logger)
}

// CloseAgyCLIInteractiveSessionForOwner closes the persistent agy
// interactive session registered for the given owner session ID. Use
// when the orchestrator needs to force a fresh relaunch — e.g. when the
// chat's workshop mode changed mid-session and the agent's system prompt
// has been updated, so the running agy CLI process (which loaded its
// prompt at launch time) must be torn down to pick up the new content.
// Returns silently when no session exists for the owner.
func CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	closeAgyPersistentSession(ownerSessionID, reason, nil)
}

// CloseAgyCLIInteractiveSessionByTmux closes the persistent agy interactive
// session whose backing tmux session matches tmuxSessionName, regardless of
// the owner key it was registered under. Use as a teardown backstop when the
// owning session ID is unknown or has drifted (e.g. a workflow sub-agent
// registered under a step-execution owner that the caller can't reconstruct).
// It resolves the tmux name to its owner and delegates to the owner-keyed
// close, so the exact same graceful exit + cleanup sequence runs. No-op when
// no live session matches the tmux name.
func CloseAgyCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	name := strings.TrimSpace(tmuxSessionName)
	if name == "" {
		return
	}
	agyPersistentRegistry.Lock()
	owner := ""
	for o, s := range agyPersistentRegistry.sessions {
		if s != nil && s.tmuxSessionName == name {
			owner = o
			break
		}
	}
	agyPersistentRegistry.Unlock()
	if owner == "" {
		return
	}
	closeAgyPersistentSession(owner, reason, nil)
}

func closeAgySessionLocked(session *agyInteractiveSession, reason string, logger interfaces.Logger) {
	if session == nil {
		return
	}
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Closing Agy interactive session %s for owner %s: %s", session.tmuxSessionName, session.ownerSessionID, reason)
	}
	removeAgyPersistentSession(session.ownerSessionID, session)
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requestAgyGracefulExit(closeCtx, session.tmuxSessionName)
	_ = killAgyTmuxSession(closeCtx, session.tmuxSessionName)
	if session.cleanupFiles != nil {
		session.cleanupFiles()
		session.cleanupFiles = nil
	}
	if session.releaseMCPLease != nil {
		session.releaseMCPLease()
		session.releaseMCPLease = nil
	}
	unregisterAgyInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
}

func markAgyInteractiveSessionFailedLocked(session *agyInteractiveSession, err error, logger interfaces.Logger) {
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
		logger.Debugf("Discarding Agy interactive session %s for owner %s: %v", session.tmuxSessionName, session.ownerSessionID, err)
	}
}

func cleanupFailedAgyInteractiveSession(session *agyInteractiveSession) {
	if session == nil {
		return
	}
	removeAgyPersistentSession(session.ownerSessionID, session)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requestAgyGracefulExit(cleanupCtx, session.tmuxSessionName)
	_ = killAgyTmuxSession(cleanupCtx, session.tmuxSessionName)
	unregisterAgyInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
	if session.cleanupFiles != nil {
		session.cleanupFiles()
	}
	if session.releaseMCPLease != nil {
		session.releaseMCPLease()
		session.releaseMCPLease = nil
	}
}

func removeAgyPersistentSession(ownerSessionID string, session *agyInteractiveSession) {
	agyPersistentRegistry.Lock()
	defer agyPersistentRegistry.Unlock()
	if current := agyPersistentRegistry.sessions[ownerSessionID]; current == session {
		delete(agyPersistentRegistry.sessions, ownerSessionID)
	}
}

func acquireAgyWorkspaceMCPConfigLease(workingDir string, opts *llmtypes.CallOptions, session *agyInteractiveSession) (func(), error) {
	mcpConfig := ""
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if raw, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok {
			mcpConfig = strings.TrimSpace(raw)
		}
	}
	if mcpConfig == "" || session == nil {
		return nil, nil
	}
	key := cleanAgyWorkingDirKey(workingDir)
	fingerprint := agyMCPConfigFingerprint(mcpConfig)

	agyWorkspaceMCPConfigRegistry.Lock()
	defer agyWorkspaceMCPConfigRegistry.Unlock()
	leases := agyWorkspaceMCPConfigRegistry.leases[key]
	for existing, existingFingerprint := range leases {
		if existing == nil || existing == session {
			continue
		}
		if existingFingerprint != fingerprint {
			return nil, fmt.Errorf("agy-cli does not support concurrent sessions in working directory %s with different MCP configs; use separate working directories or the same bridge config", workingDir)
		}
	}
	if leases == nil {
		leases = map[*agyInteractiveSession]string{}
		agyWorkspaceMCPConfigRegistry.leases[key] = leases
	}
	leases[session] = fingerprint
	released := false
	return func() {
		agyWorkspaceMCPConfigRegistry.Lock()
		defer agyWorkspaceMCPConfigRegistry.Unlock()
		if released {
			return
		}
		released = true
		current := agyWorkspaceMCPConfigRegistry.leases[key]
		delete(current, session)
		if len(current) == 0 {
			delete(agyWorkspaceMCPConfigRegistry.leases, key)
		}
	}, nil
}

func cleanAgyWorkingDirKey(workingDir string) string {
	if abs, err := filepath.Abs(strings.TrimSpace(workingDir)); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(strings.TrimSpace(workingDir))
}

func agyMCPConfigFingerprint(config string) string {
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(config), &decoded); err == nil {
		// Normalize session-specific and scope-specific fields from env maps
		if mcpServers, ok := decoded["mcpServers"].(map[string]interface{}); ok {
			for _, srv := range mcpServers {
				if srvMap, ok := srv.(map[string]interface{}); ok {
					if env, ok := srvMap["env"].(map[string]interface{}); ok {
						delete(env, "MCP_SESSION_ID")
						delete(env, "MCP_VIRTUAL_SCOPE_ID")
						if apiURL, ok := env["MCP_API_URL"].(string); ok {
							if idx := strings.Index(apiURL, "/s/"); idx != -1 {
								env["MCP_API_URL"] = apiURL[:idx]
							}
						}
					}
				}
			}
		}
		if canonical, err := json.Marshal(decoded); err == nil {
			config = string(canonical)
		}
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(config)))
	return hex.EncodeToString(sum[:])
}

func CleanupAgyCLIInteractiveSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	agyPersistentRegistry.Lock()
	sessions := make([]*agyInteractiveSession, 0, len(agyPersistentRegistry.sessions))
	for _, session := range agyPersistentRegistry.sessions {
		sessions = append(sessions, session)
	}
	agyPersistentRegistry.sessions = map[string]*agyInteractiveSession{}
	agyPersistentRegistry.Unlock()

	var failures []string
	for _, session := range sessions {
		cleanupFiles := stopAgyIdleTimerAndSnapshotCleanupIfAvailable(session)
		unregisterAgyInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		requestAgyGracefulExit(ctx, session.tmuxSessionName)
		if cleanupFiles != nil {
			cleanupFiles()
		}
		if session.releaseMCPLease != nil {
			session.releaseMCPLease()
			session.releaseMCPLease = nil
		}
		if err := killAgyTmuxSession(ctx, session.tmuxSessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up Agy interactive sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func stopAgyIdleTimerAndSnapshotCleanupIfAvailable(session *agyInteractiveSession) func() {
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
	if session.releaseMCPLease != nil {
		session.releaseMCPLease()
		session.releaseMCPLease = nil
	}
	return cleanupFiles
}

func registerAgyInteractiveSession(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if ownerSessionID == "" || tmuxSessionName == "" {
		return
	}
	agyInteractiveRegistry.Lock()
	defer agyInteractiveRegistry.Unlock()
	agyInteractiveRegistry.sessions[ownerSessionID] = tmuxSessionName
}

func unregisterAgyInteractiveSession(ownerSessionID, tmuxSessionName string) {
	agyInteractiveRegistry.Lock()
	if current := agyInteractiveRegistry.sessions[ownerSessionID]; current == tmuxSessionName {
		delete(agyInteractiveRegistry.sessions, ownerSessionID)
	}
	agyInteractiveRegistry.Unlock()
	unregisterAgyActiveSessionWorkingDir(tmuxSessionName)
}

func activeAgyInteractiveSession(ownerSessionID string) (string, bool) {
	agyInteractiveRegistry.RLock()
	defer agyInteractiveRegistry.RUnlock()
	sessionName, ok := agyInteractiveRegistry.sessions[strings.TrimSpace(ownerSessionID)]
	return sessionName, ok && strings.TrimSpace(sessionName) != ""
}

func SendAgyInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	sessionName, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Agy interactive session registered for owner session %s", ownerSessionID)
	}
	return sendAgyInputToTmux(ctx, sessionName, message)
}

func agyInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyInteractiveSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func agyPersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyPersistentInteractive].(bool)
	return ok && enabled
}

func agyWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
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

func agyResumeSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func agyAutoApproveWebSearchFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyAutoApproveWebSearch].(bool)
	return enabled
}

func startAgyTmuxSession(ctx context.Context, sessionName string, args []string, env []string, workingDir string) error {
	if workingDir == "" {
		workingDir = agyMustGetwd()
	}
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	for _, entry := range env {
		if strings.TrimSpace(entry) != "" {
			tmuxArgs = append(tmuxArgs, "-e", entry)
		}
	}
	shellCommand := "cd " + agyShellQuote(workingDir) + " && exec " + agyShellJoin(args)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runAgyCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start Agy interactive session %q: %w", sessionName, err)
	}
	_ = runAgyCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	if err := runAgyCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "history-limit", tmuxexec.DefaultHistoryLimit); err != nil {
		return fmt.Errorf("failed to configure Agy tmux history for session %q: %w", sessionName, err)
	}
	// Pin the window size to manual so the detached session keeps the size we
	// launched at instead of collapsing to default-size (80x24), which reflows
	// the TUI into half-width and makes the captured pane unreadable.
	_ = runAgyCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "window-size", "manual")
	_ = runAgyCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "focus-events", "on")
	return nil
}

func waitForAgyPrompt(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) error {
	_, err := waitForAgyPromptWithTrustSignal(ctx, sessionName, streamChan)
	return err
}

func waitForAgyPromptWithTrustSignal(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) (bool, error) {
	deadline, cancel := context.WithTimeout(ctx, agyInteractivePromptWait())
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var trustSeen bool
	var trustSubmitted bool
	var feedbackSkipped bool
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	var draftClearAttempts int
	var lastDraftClearAt time.Time
	streamTerminalScreen := agyInteractiveStreamTmuxScreenEnabled()
	for {
		select {
		case <-deadline.Done():
			captured, _ := captureAgyPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return trustSeen, fmt.Errorf("timed out waiting for Antigravity CLI prompt; latest pane:\n%s", captured)
			}
			return trustSeen, fmt.Errorf("timed out waiting for Antigravity CLI prompt")
		case <-ticker.C:
			captured, err := captureAgyPane(deadline, sessionName)
			if err != nil {
				if isAgyTmuxSessionLostError(err) {
					return trustSeen, fmt.Errorf("Antigravity CLI tmux session ended while waiting for prompt: %w", err)
				}
				continue
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamAgyTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			visible := agyVisiblePaneText(captured)
			if hasAgyTrustPrompt(visible) && !trustSubmitted {
				trustSeen = true
				_ = runAgyCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, agyTrustPromptResponse(visible))
				trustSubmitted = true
				continue
			}
			if hasAgyAuthPrompt(visible) {
				return trustSeen, agyAuthPromptError(captured)
			}
			if hasAgyFeedbackPrompt(visible) && !feedbackSkipped {
				_ = runAgyCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "0")
				feedbackSkipped = true
				continue
			}
			if hasAgyReadyPrompt(visible) {
				return trustSeen, nil
			}
			// A leftover draft in the input box (common when a persistent
			// session is reused across turns) makes the prompt line read
			// "> <text>", which is not a ready marker — so readiness never
			// trips and the turn would block until the deadline. Clear it here
			// so the prompt becomes ready. Bounded + rate-limited so a pane that
			// is genuinely not ready for other reasons still falls through to
			// the timeout rather than looping on key presses.
			if draftClearAttempts < 3 && (lastDraftClearAt.IsZero() || time.Since(lastDraftClearAt) >= 500*time.Millisecond) {
				if _, shouldClear := agyPromptDraftToClearBeforePaste(captured); shouldClear {
					_ = runAgyCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "C-e", "C-u")
					_ = runAgyCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "C-a", "C-k")
					draftClearAttempts++
					lastDraftClearAt = time.Now()
				}
			}
		}
	}
}

func sendAgyInputToTmux(ctx context.Context, sessionName, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Agy interactive input is empty")
	}
	bufferName := "mlp-agy-input-" + agyRandomHex(6)
	tmp, err := os.CreateTemp("", "agy-tmux-input-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create Agy tmux input temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(message); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write Agy tmux input temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close Agy tmux input temp file: %w", err)
	}
	// Paste, then verify the draft actually landed in the prompt before pressing
	// Enter. A swallowed paste (e.g. the CLI is still initializing) used to go
	// undetected here — Enter was sent into an empty prompt and the turn silently
	// stalled, only surfacing much later via the response-loop's no-activity
	// cutoff. Retrying the paste self-heals a not-yet-ready pane and, if the input
	// still never lands, fails fast at submit time with a clear error.
	drafted := false
	for attempt := 1; attempt <= agyPasteMaxAttempts; attempt++ {
		if err := runAgyCommand(ctx, nil, "tmux", "load-buffer", "-b", bufferName, tmpPath); err != nil {
			return fmt.Errorf("failed to load Agy input into tmux buffer: %w", err)
		}
		// Do not pass paste-buffer -p here. Agy treats bracketed paste as a
		// collapsed "[Pasted text #N]" block; raw tmux paste is still fast, preserves
		// embedded LFs with -r, and leaves the prompt text readable in the pane.
		if err := runAgyCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-r", "-b", bufferName, "-t", sessionName); err != nil {
			return fmt.Errorf("failed to paste input into Agy interactive session: %w", err)
		}
		if waitForAgyInputDraftVisible(ctx, sessionName, message, 2*time.Second) {
			drafted = true
			break
		}
		// The exact-message match can miss on unusual prompts; if any non-empty
		// draft is sitting on the prompt line, the paste did land — accept it.
		if captured, err := captureAgyPane(ctx, sessionName); err == nil {
			if _, ok := agyUnsubmittedPromptDraft(captured); ok {
				drafted = true
				break
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if !drafted {
		captured, _ := captureAgyPane(context.Background(), sessionName)
		return fmt.Errorf("Agy interactive prompt did not accept the pasted input after %d attempts — the CLI pane was not ready and the input would be lost; latest pane:\n%s", agyPasteMaxAttempts, captured)
	}
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return fmt.Errorf("failed to submit input to Agy interactive session: %w", err)
	}
	// Agy consumes the first Enter when the follow-ups suggestion box is
	// showing (it dismisses the menu but does NOT submit the text — the text
	// stays in the input draft). One extra Enter is needed to actually send.
	// We don't know up front whether the menu was shown, so probe: if after
	// the first Enter the draft is still in the input field, send another.
	ensureAgyInputSubmitted(ctx, sessionName, message)
	return nil
}

// typeAgyInputToTmux is retained for internal slash-command setup where Agy's
// command palette expects literal keystrokes. User messages go through
// sendAgyInputToTmux so they are pasted in one tmux operation instead of
// rendered key-by-key by the TUI.
func typeAgyInputToTmux(ctx context.Context, sessionName, message string) error {
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "-l", message); err != nil {
		return fmt.Errorf("failed to type input into Agy interactive session: %w", err)
	}
	waitForAgyInputDraftVisible(ctx, sessionName, message, 2*time.Second)
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return fmt.Errorf("failed to submit typed input to Agy interactive session: %w", err)
	}
	ensureAgyInputSubmitted(ctx, sessionName, message)
	return nil
}

// ensureAgyInputSubmitted polls briefly after the initial C-m and sends a
// second C-m if the pasted text is still sitting in the input draft (which
// happens when the follow-ups menu, or any other modal overlay, swallows the
// first Enter). Best-effort: errors are ignored because the first submit may
// have succeeded and the pane just hasn't repainted yet.
func ensureAgyInputSubmitted(ctx context.Context, sessionName, message string) {
	deadline, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			return
		case <-ticker.C:
			captured, err := captureAgyPane(deadline, sessionName)
			if err != nil {
				continue
			}
			if !agyPaneShowsPromptDraft(captured, message) {
				return
			}
			_ = runAgyCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "C-m")
			return
		}
	}
}

func clearAgyPromptDraftBeforePaste(ctx context.Context, sessionName string) error {
	captured, err := captureAgyPane(ctx, sessionName)
	if err != nil {
		if isAgyTmuxSessionLostError(err) {
			return err
		}
		return nil
	}
	draft, shouldClear := agyPromptDraftToClearBeforePaste(captured)
	if !shouldClear {
		return nil
	}
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-e", "C-u"); err != nil {
		return fmt.Errorf("failed to clear stale Agy prompt draft %q: %w", truncateAgyDraftForError(draft, 120), err)
	}
	if err := waitForAgyPromptDraftCleared(ctx, sessionName); err == nil {
		return nil
	}
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-a", "C-k"); err != nil {
		return fmt.Errorf("failed to clear stale Agy prompt draft %q: %w", truncateAgyDraftForError(draft, 120), err)
	}
	if err := waitForAgyPromptDraftCleared(ctx, sessionName); err != nil {
		return fmt.Errorf("failed to clear stale Agy prompt draft %q: %w", truncateAgyDraftForError(draft, 120), err)
	}
	return nil
}

func agyPromptDraftToClearBeforePaste(captured string) (string, bool) {
	if hasAgyTrustPrompt(captured) || hasAgyWebSearchApprovalPrompt(captured) || hasAgyActivity(captured) {
		return "", false
	}
	draft, placeholder, ok := latestAgyPromptDraftRaw(captured)
	if !ok {
		return "", false
	}
	draft = strings.TrimSpace(draft)
	return draft, draft != "" && !placeholder
}

func latestAgyPromptDraftRaw(captured string) (draft string, placeholder bool, ok bool) {
	lines := strings.Split(captured, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(strings.ReplaceAll(stripAgyANSI(lines[i]), "\u00a0", " "))
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "│"))
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "│"))
		if draft, ok := agyPromptLineDraft(trimmed); ok {
			return draft, isAgyPromptPlaceholder(draft), true
		}
	}
	return "", false, false
}

func agyPromptLineDraft(line string) (string, bool) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(stripAgyANSI(line), "\u00a0", " "))
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "│"))
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "│"))
	for _, marker := range []string{">", "→", "›", "❯"} {
		if trimmed == marker {
			return "", true
		}
		if strings.HasPrefix(trimmed, marker+" ") || strings.HasPrefix(trimmed, marker+"\t") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, marker)), true
		}
	}
	return "", false
}

func isAgyPromptPlaceholder(draft string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(draft, "\u00a0", " ")), " "))
	if normalized == "" {
		return false
	}
	return strings.HasPrefix(normalized, "type your message") ||
		strings.HasPrefix(normalized, "ask me anything") ||
		strings.HasPrefix(normalized, "plan, search, build anything") ||
		strings.HasPrefix(normalized, "message agy") ||
		strings.HasPrefix(normalized, "add a follow-up") ||
		(strings.HasPrefix(normalized, "try ") && strings.Contains(normalized, "\""))
}

func waitForAgyPromptDraftCleared(ctx context.Context, sessionName string) error {
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastCaptured string
	for {
		select {
		case <-deadline.Done():
			captured := lastCaptured
			if strings.TrimSpace(captured) == "" {
				captured, _ = captureAgyPane(context.Background(), sessionName)
			}
			draft, _, ok := latestAgyPromptDraftRaw(captured)
			if !ok {
				return fmt.Errorf("prompt draft still could not be inspected; latest pane:\n%s", captured)
			}
			return fmt.Errorf("prompt draft still present: %q; latest pane:\n%s", draft, captured)
		case <-ticker.C:
			captured, err := captureAgyPane(deadline, sessionName)
			if err != nil {
				if isAgyTmuxSessionLostError(err) {
					return err
				}
				continue
			}
			lastCaptured = captured
			draft, placeholder, ok := latestAgyPromptDraftRaw(captured)
			if ok && (strings.TrimSpace(draft) == "" || placeholder) {
				return nil
			}
		}
	}
}

func truncateAgyDraftForError(draft string, maxRunes int) string {
	runes := []rune(draft)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return draft
	}
	return string(runes[:maxRunes]) + "..."
}

// waitForAgyInputDraftVisible polls until the pasted message is visible in the
// prompt draft, returning true once seen or false if the timeout elapses. The
// boolean lets the caller detect a swallowed paste (CLI not ready) and retry.
func waitForAgyInputDraftVisible(ctx context.Context, sessionName, message string, timeout time.Duration) bool {
	if strings.TrimSpace(message) == "" {
		return false
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			return false
		case <-ticker.C:
			captured, err := captureAgyPane(deadline, sessionName)
			if err == nil && agyPaneShowsPromptDraft(captured, message) {
				return true
			}
		}
	}
}

func waitForAgyInteractiveResponse(ctx context.Context, sessionName, baseline, prompt string, historicalAssistantTexts []string, streamChan chan<- llmtypes.StreamChunk, autoApproveWebSearch bool) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	waitStartedAt := time.Now()
	firstActivityTimeout := agyInteractiveFirstActivityTimeout()
	stalePaneBackstop := agyInteractiveStalePaneBackstop()
	var sawActivity bool
	var idleSince time.Time
	var readyWithoutContentSince time.Time
	var submitRetryCount int
	var lastSubmitRetryAt time.Time
	var draftSubmitRetryCount int
	var lastDraftSubmitRetryAt time.Time
	var lastDraftSubmitValue string
	var lastCaptured string
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	var lastWebSearchApprovalAt time.Time
	var lastFeedbackSkipAt time.Time
	// Stale-pane backstop tracking: the raw capture from the previous tick and
	// the time it last changed. This is tracked at the top of every tick,
	// independent of all the branch logic below, so a prompt-detection bug that
	// keeps the loop in a "not ready" branch can never suppress it.
	var backstopPrevCapture string
	var paneUnchangedSince time.Time
	streamTerminalScreen := agyInteractiveStreamTmuxScreenEnabled()
	for {
		select {
		case <-ctx.Done():
			captured, _ := captureAgyPane(context.Background(), sessionName)
			return captured, ctx.Err()
		case <-ticker.C:
			captured, err := captureAgyPane(ctx, sessionName)
			if err != nil {
				return "", err
			}
			delta := agyCapturedAfterBaseline(captured, baseline)
			if tmuxcontrol.ConsumeForceComplete(sessionName) {
				return captured, tmuxcontrol.ErrForceComplete
			}
			// Stale-pane backstop. Independent of hasAgyReadyPrompt and every
			// branch below: if the pane has produced activity and then frozen
			// (byte-identical) for longer than the backstop, the turn is over but
			// completion detection failed to recognize it (e.g. a stale "○ " tool
			// card or leftover spinner frame holding the pane "not ready"). Extract
			// whatever response is present and return it rather than hang forever.
			if captured != backstopPrevCapture {
				backstopPrevCapture = captured
				paneUnchangedSince = time.Now()
			} else if sawActivity && stalePaneBackstop > 0 && !paneUnchangedSince.IsZero() &&
				time.Since(paneUnchangedSince) >= stalePaneBackstop {
				content := parseAgyInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
				if strings.TrimSpace(content) == "" {
					content = forcedAgyInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
				}
				if strings.TrimSpace(content) != "" {
					return captured, nil
				}
				return captured, fmt.Errorf("Antigravity CLI pane went unchanged for %s after activity but no ready prompt or visible assistant output was detected; latest pane:\n%s", stalePaneBackstop, captured)
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamAgyTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if autoApproveWebSearch && hasAgyWebSearchApprovalPrompt(captured) {
				if lastWebSearchApprovalAt.IsZero() || time.Since(lastWebSearchApprovalAt) >= 2*time.Second {
					if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "y"); err == nil {
						lastWebSearchApprovalAt = time.Now()
					}
				}
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if hasAgyFeedbackPrompt(captured) {
				if lastFeedbackSkipAt.IsZero() || time.Since(lastFeedbackSkipAt) >= 2*time.Second {
					if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "0"); err == nil {
						lastFeedbackSkipAt = time.Now()
					}
				}
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if hasAgyAuthPrompt(captured) {
				return captured, agyAuthPromptError(captured)
			}
			// Reset idle only when we have activity AND we're not yet
			// at the ready prompt. Agy's TUI leaves stale status
			// lines ("Running...", "Thinking...") visible for several
			// seconds after the → prompt reappears; those used to
			// keep restarting the idle timer and added 5-10s of
			// avoidable wait to every turn. hasAgyReadyPrompt
			// already handles the "→ visible + stale status" case
			// correctly (returns true), so once we're ready we let
			// the stable-window check drive completion.
			if !hasAgyReadyPrompt(captured) && hasAgyActivity(captured) {
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if strings.TrimSpace(delta) != "" {
				sawActivity = true
			}
			if !sawActivity {
				// The CLI has shown nothing since the prompt was submitted.
				// Every completion/failsafe path below requires sawActivity, so
				// without this cap a never-delivered prompt spins here forever
				// (the call context has no deadline by default). Fail cleanly so
				// the step surfaces an error instead of hanging.
				if firstActivityTimeout > 0 && time.Since(waitStartedAt) >= firstActivityTimeout {
					captured, _ := captureAgyPane(context.Background(), sessionName)
					return captured, fmt.Errorf("Antigravity CLI produced no activity within %s of submitting the prompt — the input was likely not delivered to the tmux pane; latest pane:\n%s", firstActivityTimeout, captured)
				}
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if !hasAgyReadyPrompt(captured) {
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if draft, ok := agyUnsubmittedPromptDraft(captured); ok {
				if draft != lastDraftSubmitValue {
					lastDraftSubmitValue = draft
					draftSubmitRetryCount = 0
					lastDraftSubmitRetryAt = time.Time{}
				}
				if draftSubmitRetryCount < 3 && (lastDraftSubmitRetryAt.IsZero() || time.Since(lastDraftSubmitRetryAt) >= time.Second) {
					_ = runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m")
					draftSubmitRetryCount++
					lastDraftSubmitRetryAt = time.Now()
				}
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			// Stability check. We only reach here once hasAgyReadyPrompt(captured)
			// is true (gated above), i.e. the idle "> " input box is showing — so
			// the turn is effectively done. Compare with the spinner animation
			// normalized OUT: agy can leave a spinner glyph cycling ("⣽"→"⣾"→"⣷",
			// "Generating…"→"Generating.") after the ready prompt is already up. The
			// raw bytes then change every ~100ms and the pane never looks "stable",
			// so completion never fires and the turn hangs forever even though the
			// answer is right there. Normalizing the cycling spinner lets the stable
			// window elapse and the turn complete. This is safe precisely because
			// we are past the ready-prompt gate — a genuinely mid-work spinner (no
			// "> " box yet) fails that gate and never reaches this comparison.
			if agySpinnerStableKey(captured) != agySpinnerStableKey(lastCaptured) {
				lastCaptured = captured
				idleSince = time.Now()
				continue
			}
			if idleSince.IsZero() {
				idleSince = time.Now()
				continue
			}
			if time.Since(idleSince) >= agyInteractiveStableWindow {
				content := parseAgyInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
				if strings.TrimSpace(content) == "" {
					if readyWithoutContentSince.IsZero() {
						readyWithoutContentSince = time.Now()
					}
					if agyPaneShowsPromptDraft(captured, prompt) && submitRetryCount < 3 && time.Since(lastSubmitRetryAt) >= time.Second {
						_ = runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m")
						submitRetryCount++
						lastSubmitRetryAt = time.Now()
						idleSince = time.Time{}
						continue
					}
					if time.Since(readyWithoutContentSince) >= 15*time.Second {
						return captured, fmt.Errorf("Antigravity CLI returned to the prompt without visible assistant output; latest pane:\n%s", captured)
					}
					continue
				}
				return captured, nil
			}
		}
	}
}

func agyUnsubmittedPromptDraft(captured string) (string, bool) {
	draft, placeholder, ok := latestAgyPromptDraftRaw(captured)
	draft = strings.TrimSpace(draft)
	return draft, ok && draft != "" && !placeholder
}

func parseAgyInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
	delta := agyCapturedAfterBaseline(captured, baseline)
	text := extractAgyVisibleAssistantText(delta)
	text = stripAgyEchoedUserPrompt(text, echoedUserPrompt)
	text = stripAgyHistoricalAssistantText(text, historicalAssistantTexts)
	return strings.TrimSpace(text)
}

func forcedAgyInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
	delta := agyCapturedAfterBaseline(captured, baseline)
	text := extractAgyVisibleAssistantText(delta)
	text = stripAgyEchoedUserPrompt(text, echoedUserPrompt)
	text = stripAgyHistoricalAssistantText(text, historicalAssistantTexts)
	return strings.TrimSpace(text)
}

func extractAgyVisibleAssistantText(delta string) string {
	lines := strings.Split(stripAgyANSI(delta), "\n")
	out := make([]string, 0, len(lines))
	skipThoughtTitle := false
	for _, line := range lines {
		trimmed := normalizeAgyPaneLine(line)
		if draft, ok := agyPromptLineDraft(trimmed); ok {
			if strings.TrimSpace(draft) != "" && !isAgyPromptPlaceholder(draft) {
				out = out[:0]
				skipThoughtTitle = false
				continue
			}
			break
		}
		if isAgyPromptBoundaryLine(trimmed) {
			break
		}
		if skipThoughtTitle && trimmed != "" {
			skipThoughtTitle = false
			continue
		}
		// "User: …" marks the start of a user turn. Everything previously
		// collected is from an older assistant turn and must be discarded —
		// otherwise a multi-turn pane (where baseline-diff falls back to
		// line-prefix mode and leaves prior turns in the delta) leaks the
		// stale reply into the new turn's extracted text.
		if isAgyUserTurnHeader(trimmed) {
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
		if isAgyThoughtStatusLine(trimmed) {
			out = out[:0]
			skipThoughtTitle = true
			continue
		}
		if isAgyToolStatusLine(trimmed) {
			out = out[:0]
			continue
		}
		// A line beginning with a Braille spinner glyph is agy's live/stale
		// generation status (e.g. "⣾ Generating…", or a frozen "⣟ Gener … ,N
		// tokens" left behind after a slow turn). It is never assistant content,
		// so skip it — otherwise the loop can return "Generating…" as the answer.
		if agyLineStartsWithSpinner(strings.TrimSpace(trimmed)) {
			continue
		}
		if isAgyTUILine(trimmed) || isAgyBoxDrawingLine(trimmed) {
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

// isAgyUserTurnHeader matches Agy's per-turn user header ("User: <prompt>").
// Anchored on the colon-space pair to avoid matching prose like "User input is".
func isAgyUserTurnHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "User:") && !strings.HasPrefix(trimmed, "user:") {
		return false
	}
	// Require the colon to be followed by whitespace OR end-of-line — distinguishes
	// Agy's "User: hi" header from prose like "User:enter your username".
	if len(trimmed) == len("User:") {
		return true
	}
	next := trimmed[len("User:")]
	return next == ' ' || next == '\t'
}

func normalizeAgyPaneLine(line string) string {
	line = strings.TrimSpace(stripAgyANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSuffix(line, "│")
	line = strings.TrimSpace(line)
	// Agy labels each assistant turn with a literal "Assistant:" header. Strip
	// it so the kept response reads as plain prose (and matches what the user
	// sees in the chat panel for Claude/Gemini, which have no such label).
	line = strings.TrimSpace(strings.TrimPrefix(line, "Assistant:"))
	return line
}

// agyShellEchoSuffix matches the duration suffix Agy appends to shell-tool
// echoes (e.g. "$ ls -1 /tmp 407ms", "$ sleep 1 1.0s"). Used to distinguish a
// shell-tool transcript line from a code block that legitimately starts with "$".
var agyShellEchoSuffix = regexp.MustCompile(`\s\d+(?:\.\d+)?(?:ms|s)\s*$`)

// agyFoundCountLine matches the tool-result summary Agy prints after
// grep/glob/list operations: "Found 33 files", "Found 1,024 matches", etc.
var agyFoundCountLine = regexp.MustCompile(`^found\s+[\d,]+\s+(files?|matches?|results?|symbols?)\b`)

// agyMultiToolSummaryLine matches Agy's per-turn tool-activity summary:
//
//	"Read, grepped, globbed 7 files, 4 greps, 2 globs"
//
// The line lists past-tense verbs followed by counts. Always tool narration,
// never response prose.
var agyMultiToolSummaryLine = regexp.MustCompile(`^(?:read|grepped|globbed|listed|searched)[,\s].*\b\d+\s+(?:files?|greps?|globs?|matches?|results?|symbols?|reads?|lists?|searches?)\b`)

// agyEarlierHiddenLine matches Agy's truncation header on long tool
// transcripts, e.g. "… 10 earlier items hidden" or "... 3 earlier tools hidden".
var agyEarlierHiddenLine = regexp.MustCompile(`^(?:…|\.\.\.)\s*\d+\s+earlier\s+(?:items?|tools?|results?)\b`)

// agyReadFileLine matches Agy's per-file read narration. Agy truncates
// long paths with `...`, so a real prose line "Read the docs" is unaffected — the
// regex requires a path token and either "lines N-M" or a file extension.
var agyReadFileLine = regexp.MustCompile(`^read\s+(?:\.\.\.|/|~)\S*\s+(?:lines?\s+\d+(?:-\d+)?|.*\.\w{1,8}\b)`)

// agyMCPToolCardLine matches collapsed MCP tool cards such as:
//
//	"● api-bridge/get_api_spec(Get API spec)"
//
// The provider/server segment is intentionally generic because workflow tools
// use server names beyond api-bridge.
var agyMCPToolCardLine = regexp.MustCompile(`^[\w.-]+/[\w.-]+\(`)

var agyToolCountLine = regexp.MustCompile(`^[+-]\s*\d+\s+tools?\b`)

func isAgyPromptBoundaryLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	// The → arrow is Agy's input agy — the most reliable structural
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
		strings.Contains(lower, "agy agent") && strings.Contains(lower, "workspace")
}

func isAgyTUILine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if trimmed == "" {
		return true
	}
	return strings.Contains(lower, "ctrl+") ||
		strings.HasSuffix(lower, "collapse)") ||
		strings.HasSuffix(lower, "to collapse)") ||
		strings.Contains(lower, "esc to") ||
		strings.Contains(lower, "press enter") ||
		strings.Contains(lower, "run everything") ||
		strings.Contains(lower, "ask (shift+tab") ||
		strings.HasPrefix(lower, "v20") ||
		strings.Contains(lower, "try composer") ||
		strings.Contains(lower, "composer") && strings.Contains(lower, "fast") ||
		strings.Contains(trimmed, " · ") ||
		strings.HasPrefix(trimmed, "→ ") ||
		strings.Contains(lower, "agy agent") ||
		strings.Contains(lower, "agy") && strings.Contains(lower, "model") ||
		strings.Contains(lower, "workspace:") ||
		strings.Contains(lower, "mode:") ||
		strings.Contains(lower, "approval") ||
		strings.Contains(lower, "permission") ||
		strings.Contains(lower, "pasted text") ||
		strings.HasPrefix(lower, "use /") ||
		strings.HasPrefix(lower, "add a follow-up") ||
		strings.HasPrefix(lower, "auto-run") ||
		// Agy labels each user turn with a literal "User:" header. It is a
		// structural marker, not response prose, so drop it from extraction.
		strings.HasPrefix(lower, "user:")
}

func isAgyToolStatusLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	// "⎿" (U+23BF) is Agy's tool-result continuation marker — it prefixes the
	// output detail of a tool/command card (e.g. "⎿  Statusline set to: …" from
	// the startup statusline config). It is never assistant prose, so any line
	// beginning with it is tool transcript.
	if strings.HasPrefix(trimmed, "⎿") {
		return true
	}
	nativeToolLine := strings.TrimSpace(strings.TrimLeft(trimmed, "●○◦* "))
	nativeToolLower := strings.ToLower(nativeToolLine)
	if strings.HasPrefix(nativeToolLower, "bash(") ||
		strings.HasPrefix(nativeToolLower, "generateimage(") ||
		strings.HasPrefix(nativeToolLower, "listpermissions(") ||
		strings.HasPrefix(nativeToolLower, "read(") ||
		strings.HasPrefix(nativeToolLower, "write(") ||
		strings.HasPrefix(nativeToolLower, "edit(") ||
		strings.HasPrefix(nativeToolLower, "grep(") ||
		strings.HasPrefix(nativeToolLower, "glob(") {
		return true
	}
	if agyMCPToolCardLine.MatchString(nativeToolLower) {
		return true
	}
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
	if agyToolCountLine.MatchString(lower) ||
		strings.HasPrefix(trimmed, "-H ") ||
		strings.HasPrefix(trimmed, "-d ") {
		return true
	}
	// Tool-result summary lines: "Found N files", "Found N matches", …
	if agyFoundCountLine.MatchString(lower) {
		return true
	}
	// Combined per-turn tool-activity summary, truncation header, per-file read
	// narration — all are tool transcript, never response prose.
	if agyMultiToolSummaryLine.MatchString(lower) ||
		agyEarlierHiddenLine.MatchString(lower) ||
		agyReadFileLine.MatchString(lower) {
		return true
	}
	// Shell-tool command echo: starts with "$ " and ends with a duration
	// suffix like "407ms" or "1.2s" — distinguishes the tool transcript from a
	// markdown code block that happens to begin with "$".
	if strings.HasPrefix(trimmed, "$ ") && agyShellEchoSuffix.MatchString(lower) {
		return true
	}
	// Truncation marker that closes a tool-output block:
	//   "… truncated (36 more lines) · ctrl+o to expand"
	// (Already filtered by isAgyTUILine's " · " rule in the common case;
	// handle the no-middot variant defensively.)
	if strings.Contains(lower, "truncated") &&
		(strings.Contains(lower, "more lines") || strings.Contains(lower, "more line")) {
		return true
	}
	return false
}

func isAgyThoughtStatusLine(line string) bool {
	// Strip any leading triangle/arrow glyphs (▸ right U+25B8, ▾ down U+25BE)
	// and spaces before checking. Agy uses both depending on whether the thought
	// block is collapsed (▾) or expanded (▸).
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimLeftFunc(trimmed, func(r rune) bool {
		return r == '▸' || r == '▾' || r == ' '
	})
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "thought for ") ||
		strings.HasPrefix(lower, "thought process")
}

func isAgyBoxDrawingLine(line string) bool {
	if line == "" {
		return true
	}
	// If the line contains a vertical column separator, it's a table row, not a TUI divider line.
	if strings.Contains(line, "│") || strings.Contains(line, "|") {
		return false
	}
	for _, r := range line {
		if strings.ContainsRune("─━▀▄▁▂▃▅▆▇█▌▐▝▜▗▟▘▛▙▚▞▖╭╮╰╯│┌┐└┘├┤┬┴┼╞╪╡╘╧╛╔╗╚╝═║╠╣╦╩╬╌╍╎╏┄┅┆┇┈┉┊┋ ", r) {
			continue
		}
		return false
	}
	return true
}

func stripAgyEchoedUserPrompt(text, prompt string) string {
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
	promptLines := nonEmptyAgyLines(prompt)
	if len(textNonEmpty) == 0 || len(promptLines) == 0 {
		return text
	}
	bestStart := -1
	bestLen := 0
	bestPromptStart := 0
	for start := 0; start < len(textNonEmpty) && start < 64; start++ {
		for promptStart := 0; promptStart < len(promptLines); promptStart++ {
			matchLen := 0
			for start+matchLen < len(textNonEmpty) &&
				promptStart+matchLen < len(promptLines) &&
				agyPromptLinesEqual(textNonEmpty[start+matchLen], promptLines[promptStart+matchLen]) {
				matchLen++
			}
			if matchLen > bestLen {
				bestStart = start
				bestLen = matchLen
				bestPromptStart = promptStart
			}
		}
	}
	if bestLen < 2 && !(len(promptLines) == 1 && bestLen == 1) {
		return text
	}
	if bestStart == 0 && bestPromptStart > 0 && bestLen == len(textNonEmpty) {
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

func stripAgyHistoricalAssistantText(text string, historicalAssistantTexts []string) string {
	text = strings.TrimSpace(text)
	if text == "" || len(historicalAssistantTexts) == 0 {
		return text
	}
	for i := len(historicalAssistantTexts) - 1; i >= 0; i-- {
		historical := strings.TrimSpace(historicalAssistantTexts[i])
		if historical == "" {
			continue
		}
		if stripped, ok := stripAgyHistoricalPrefix(text, historical); ok {
			text = strings.TrimSpace(stripped)
			i = len(historicalAssistantTexts)
		}
	}
	return text
}

func stripAgyHistoricalPrefix(text, historical string) (string, bool) {
	if text == historical {
		return "", true
	}
	if strings.HasPrefix(text, historical) {
		return text[len(historical):], true
	}
	historicalLines := nonEmptyAgyLines(historical)
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

func agyPromptLinesEqual(a, b string) bool {
	a = normalizeAgyPromptLine(a)
	b = normalizeAgyPromptLine(b)
	return a != "" && a == b
}

func normalizeAgyPromptLine(line string) string {
	line = strings.TrimSpace(stripAgyANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, ">")
	line = strings.TrimPrefix(line, "›")
	return strings.TrimSpace(line)
}

func nonEmptyAgyLines(text string) []string {
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

func agyPaneShowsPromptDraft(captured, prompt string) bool {
	captured = strings.ToLower(stripAgyANSI(captured))
	for _, line := range nonEmptyAgyLines(prompt) {
		line = strings.TrimSpace(stripAgyANSI(line))
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

func hasAgyReadyPrompt(captured string) bool {
	visible := agyVisiblePaneText(captured)
	if hasAgyTrustPrompt(visible) {
		return false
	}
	if hasAgyAuthPrompt(visible) {
		return false
	}
	if hasAgyWebSearchApprovalPrompt(visible) {
		return false
	}
	if hasAgyFeedbackPrompt(visible) {
		return false
	}
	cleaned := strings.ToLower(stripAgyANSI(visible))
	if !hasAgyReadyMarker(cleaned) {
		return false
	}
	// Live generation signals (composing spinner, ctrl+c to stop) mean the
	// turn is still in progress — never treat as ready.
	if hasAgyLiveGenerationActivity(cleaned) {
		return false
	}
	// Agy leaves stale status lines (Running..., Thinking...) in the pane
	// after a tool finishes. Once the → prompt is visible, stale activity
	// text should not keep the turn open forever. Recent Agy builds can use a
	// plain ">" input line after completion, so accept either structural prompt.
	if hasAgyActivity(visible) && !hasAgyReadyInputPromptLine(cleaned) {
		return false
	}
	return true
}

func hasAgyLiveGenerationActivity(cleaned string) bool {
	readyInputVisible := hasAgyReadyInputPromptLine(cleaned)
	for _, line := range strings.Split(cleaned, "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "ctrl+c to stop") ||
			strings.Contains(lower, "ctrl+c to cancel") ||
			strings.Contains(lower, "esc to interrupt") ||
			strings.Contains(lower, "composing") {
			return true
		}
		// A line beginning with the open-circle bullet ("○ ") is agy's marker
		// for a tool card. While a turn is mid-flight that signals a still-running
		// tool, but a COMPLETED tool card keeps the same "○ …(ctrl+o to expand)"
		// shape and lingers in the scrollback after the turn ends. Once the ready
		// input prompt ("> ") is visible the turn is finished, so a historical
		// "○ " card must NOT be read as live generation — otherwise a completed,
		// byte-stable pane is held "not ready" forever and the response loop hangs.
		// Gate it behind !readyInputVisible, matching the "esc to cancel" rule below.
		if strings.HasPrefix(lower, "○ ") && !readyInputVisible {
			return true
		}
		if strings.Contains(lower, "esc to cancel") && !readyInputVisible {
			return true
		}
	}
	return false
}

func hasAgyReadyMarker(cleaned string) bool {
	// The → arrow is Agy's structural input agy — the most reliable
	// signal that the prompt area is visible, regardless of placeholder text.
	if hasAgyReadyInputPromptLine(cleaned) {
		return true
	}
	return strings.Contains(cleaned, "type your message") ||
		strings.Contains(cleaned, "ask (shift+tab") ||
		strings.Contains(cleaned, "plan, search, build anything") ||
		strings.Contains(cleaned, "what can i help") ||
		strings.Contains(cleaned, "ask me anything") ||
		strings.Contains(cleaned, "message agy") ||
		strings.Contains(cleaned, "add a follow-up")
}

func hasAgyReadyInputPromptLine(cleaned string) bool {
	for _, line := range strings.Split(cleaned, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "→") || trimmed == ">" {
			return true
		}
	}
	return false
}

func hasAgyTrustPrompt(captured string) bool {
	cleaned := strings.ToLower(stripAgyANSI(agyVisiblePaneText(captured)))
	if strings.Contains(cleaned, "trusting workspace") {
		return false
	}
	return strings.Contains(cleaned, "workspace trust required") ||
		strings.Contains(cleaned, "do you trust the contents of this directory") ||
		strings.Contains(cleaned, "do you trust the contents of this project") ||
		strings.Contains(cleaned, "trust") && strings.Contains(cleaned, "workspace") &&
			(strings.Contains(cleaned, "y/n") || strings.Contains(cleaned, "yes") ||
				strings.Contains(cleaned, "[a]") || strings.Contains(cleaned, "[w]"))
}

func hasAgyAuthPrompt(captured string) bool {
	cleaned := strings.ToLower(stripAgyANSI(agyVisiblePaneText(captured)))
	return strings.Contains(cleaned, "welcome to the antigravity cli") &&
		strings.Contains(cleaned, "not signed in") &&
		(strings.Contains(cleaned, "select login method") ||
			strings.Contains(cleaned, "google oauth") ||
			strings.Contains(cleaned, "google cloud project"))
}

func agyAuthPromptError(captured string) error {
	return fmt.Errorf("%w; run `agy` locally and sign in before using the agy-cli provider; latest pane:\n%s", errAgyAuthRequired, captured)
}

func hasAgyWebSearchApprovalPrompt(captured string) bool {
	cleaned := strings.ToLower(stripAgyANSI(agyVisiblePaneText(captured)))
	return strings.Contains(cleaned, "allow this web search") ||
		strings.Contains(cleaned, "allow search (y)") ||
		strings.Contains(cleaned, "web search:") && strings.Contains(cleaned, "allow")
}

func hasAgyFeedbackPrompt(captured string) bool {
	cleaned := strings.ToLower(stripAgyANSI(agyVisiblePaneText(captured)))
	return strings.Contains(cleaned, "how's the cli experience so far") &&
		strings.Contains(cleaned, "help us improve") &&
		strings.Contains(cleaned, "[0] skip")
}

func agyTrustPromptResponse(captured string) string {
	cleaned := strings.ToLower(stripAgyANSI(captured))
	if strings.Contains(cleaned, "yes, i trust this folder") ||
		strings.Contains(cleaned, "do you trust the contents of this project") {
		return "Enter"
	}
	if strings.Contains(cleaned, "[a]") || strings.Contains(cleaned, "trust this workspace, but don't enable all mcp servers") {
		return "a"
	}
	return "y"
}

// agyBrailleSpinner reports whether r is one of agy's animated spinner glyphs.
// agy renders its in-progress status with a leading Braille Patterns glyph
// (U+2800–U+28FF), e.g. "⣾ Generating…"; a line beginning with one is a
// reliable "actively generating" signal that completed markers (▸ ● ○) are not.
func agyBrailleSpinner(r rune) bool { return r >= 0x2800 && r <= 0x28FF }

// agySpinnerStatusWords are the words agy animates in its in-place spinner
// ("⣾ Generating…", "Thinking…", "Working…"). Used to normalize a cycling
// spinner out of the pane for the stability comparison.
var agySpinnerStatusWords = []string{
	"generating", "thinking", "working", "loading", "analyzing", "exploring",
	"reviewing", "confirming", "refining", "investigating", "searching",
	"reading", "writing", "calling", "running", "navigating", "examining",
	"identifying", "saving", "extracting", "discovering", "processing",
	"waiting", "fetching", "building", "planning", "composing", "retrieving",
}

// agyIsAnimatedSpinnerLine reports whether a line is an animated spinner status
// line whose bytes cycle on their own (the Braille glyph rotating, the trailing
// dots growing) while agy is otherwise idle. Leading marker glyphs (Braille, ●,
// ○, ▸, ▾) and spaces are stripped first; a line that is then empty (glyph only)
// or begins with a known status word is treated as animation.
func agyIsAnimatedSpinnerLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	t = strings.TrimLeftFunc(t, func(r rune) bool {
		return r == ' ' || r == '●' || r == '○' || r == '▸' || r == '▾' || agyBrailleSpinner(r)
	})
	t = strings.TrimSpace(t)
	if t == "" {
		return true // glyph-only frame
	}
	lower := strings.ToLower(t)
	for _, w := range agySpinnerStatusWords {
		if strings.HasPrefix(lower, w) {
			return true
		}
	}
	return false
}

// agySpinnerStableKey returns the pane with animated spinner lines removed, so a
// spinner cycling in place (after the ready prompt is already up) does not read
// as a content change. Real output changes still change the key. Used only for
// the post-ready-prompt stability comparison — see waitForAgyInteractiveResponse.
func agySpinnerStableKey(captured string) string {
	if captured == "" {
		return ""
	}
	lines := strings.Split(captured, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if agyIsAnimatedSpinnerLine(line) {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t\r"))
	}
	return strings.Join(out, "\n")
}

// agyLineStartsWithSpinner reports whether the trimmed, lowercased line begins
// with an animated Braille spinner glyph.
func agyLineStartsWithSpinner(lower string) bool {
	for _, r := range lower {
		return agyBrailleSpinner(r)
	}
	return false
}

// agyActivityKeyword strips any leading spinner glyph / bullet / punctuation so
// a status word matches even when the live spinner prefixes it, e.g.
// "⣾ generating…" → "generating…".
func agyActivityKeyword(lower string) string {
	return strings.TrimLeftFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) })
}

func hasAgyActivity(captured string) bool {
	for _, line := range strings.Split(stripAgyANSI(agyVisiblePaneText(captured)), "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		// A Braille spinner glyph anywhere at the start of a line means agy is
		// mid-animation (e.g. "⣾ Generating…"); treat it as activity directly.
		if agyLineStartsWithSpinner(lower) {
			return true
		}
		// Match status words even when prefixed by a spinner/bullet glyph.
		keyword := agyActivityKeyword(lower)
		if strings.Contains(lower, "esc to interrupt") ||
			strings.Contains(lower, "esc to cancel") ||
			strings.Contains(lower, "ctrl+c to cancel") ||
			strings.Contains(lower, "ctrl+c to stop") ||
			strings.Contains(lower, "composing") ||
			strings.HasPrefix(keyword, "thinking") ||
			strings.HasPrefix(keyword, "working") ||
			strings.HasPrefix(keyword, "running") ||
			strings.HasPrefix(keyword, "generating") ||
			strings.HasPrefix(keyword, "editing") ||
			strings.HasPrefix(keyword, "applying") ||
			strings.HasPrefix(keyword, "calling ") {
			return true
		}
	}
	return false
}

func agyVisiblePaneText(captured string) string {
	_, rows := tmuxsize.Size()
	if rows <= 0 {
		rows = tmuxsize.DefaultRows
	}
	lines := strings.Split(captured, "\n")
	if len(lines) <= rows {
		return captured
	}
	return strings.Join(lines[len(lines)-rows:], "\n")
}

func streamAgyTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	streamAgyStatusLine(ctx, sessionName, streamChan)
	snapshot, err := captureAgyPaneForDisplay(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripAgyANSIPreserveColors(snapshot), "\n")
	if strings.TrimSpace(snapshot) == "" || snapshot == *lastTerminalSnapshot {
		return false
	}
	*lastTerminalSnapshot = snapshot
	select {
	case streamChan <- llmtypes.StreamChunk{
		Type:    llmtypes.StreamChunkTypeTerminal,
		Content: snapshot,
		Metadata: map[string]interface{}{
			"tmux_session":            sessionName,
			"agy_interactive_session": sessionName,
		},
	}:
		return true
	default:
		return false
	}
}

func interruptAgyInteractiveSession(sessionName string, logger interfaces.Logger) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runAgyCommand(interruptCtx, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil && logger != nil {
		logger.Debugf("Failed to send Escape to Agy interactive session %s: %v", sessionName, err)
	}
}

func requestAgyGracefulExit(ctx context.Context, sessionName string) {
	if strings.TrimSpace(sessionName) == "" {
		return
	}
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil {
		return
	}
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "-l", "/exit"); err != nil {
		return
	}
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return
	}
	waitForAgyTmuxSessionGone(ctx, sessionName, 2*time.Second)
}

func waitForAgyTmuxSessionGone(ctx context.Context, sessionName string, timeout time.Duration) bool {
	if strings.TrimSpace(sessionName) == "" {
		return true
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			return false
		case <-ticker.C:
			err := runAgyCommand(deadline, nil, "tmux", "has-session", "-t", sessionName)
			if err == nil {
				continue
			}
			if isAgyTmuxSessionLostError(err) {
				return true
			}
			return false
		}
	}
}

func resetAgyPaneForTurn(ctx context.Context, sessionName string) {
	// Preserve tmux scrollback for browser/UI history. We intentionally do NOT
	// send C-l (0x0C) anymore: Agy's raw-mode TUI
	// catches that keystroke as "clear display", which wipes the visible
	// chat history the operator is watching in the browser terminal pane.
	// Memory is bounded by the session history-limit, and per-turn parsing is
	// anchored to the captured baseline.
	// Baseline-diff logic in agyCapturedAfterBaseline tolerates an
	// already-populated pane via LastIndex(captured, baseline).
}

func captureAgyPane(ctx context.Context, sessionName string) (string, error) {
	return tmuxexec.CapturePane(ctx, sessionName, tmuxexec.DefaultScrollbackLines)
}

func captureAgyPaneForDisplay(ctx context.Context, sessionName string) (string, error) {
	// -e preserves ANSI SGR (color, bold, dim, etc.) so the frontend can
	// colorize the snapshot via ansi_up. Cursor positioning sequences are
	// stripped at the next layer (stripAgyANSIPreserveColors) so they don't
	// garble the rendered output.
	// -J joins wrapped lines so the frontend can handle wrapping natively without
	// hard splitting words mid-line.
	return tmuxexec.CapturePaneANSI(ctx, sessionName, tmuxexec.DefaultScrollbackLines)
}

func agyCapturedAfterBaseline(captured, baseline string) string {
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
	return agyLinePrefixDelta(normalizedCaptured, normalizedBaseline)
}

func agyLinePrefixDelta(captured, baseline string) string {
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

func killAgyTmuxSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return nil
	}
	if err := runAgyCommand(ctx, nil, "tmux", "kill-session", "-t", sessionName); err != nil {
		if isAgyTmuxSessionLostError(err) {
			return nil
		}
		return err
	}
	return nil
}

func isAgyTmuxSessionLostError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no server running") ||
		strings.Contains(lower, "can't find pane") ||
		strings.Contains(lower, "can't find session") ||
		strings.Contains(lower, "no current target")
}

func agyInteractiveSessionPrefix() string {
	prefix := strings.TrimSpace(os.Getenv(EnvAgyInteractiveSessionPrefix))
	if prefix == "" {
		prefix = "mlp-agy-cli-int"
	}
	return sanitizeAgyTmuxSessionName(prefix)
}

func newAgyTmuxSessionName() string {
	return sanitizeAgyTmuxSessionName(fmt.Sprintf("%s-%d-%s", agyInteractiveSessionPrefix(), time.Now().UnixNano(), agyRandomHex(4)))
}

func agyInteractiveTimeout() time.Duration {
	return agyDurationFromEnvAllowZero(EnvAgyInteractiveTimeoutSeconds, defaultAgyInteractiveTimeout)
}

func agyInteractiveCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := agyInteractiveTimeout()
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func agyInteractiveIdleTimeout() time.Duration {
	return agyDurationFromEnv(EnvAgyInteractiveIdleTimeoutSeconds, defaultAgyInteractiveIdleTimeout)
}

// agyInteractiveFirstActivityTimeout bounds the wait for the CLI's first sign of
// activity after the prompt is submitted. Always > 0 so a lost-input hang fails
// cleanly even when the caller and provider both run without a turn deadline.
func agyInteractiveFirstActivityTimeout() time.Duration {
	return agyDurationFromEnv(EnvAgyInteractiveFirstActivityTimeoutSeconds, defaultAgyInteractiveFirstActivityTimeout)
}

func agyInteractiveStalePaneBackstop() time.Duration {
	return agyDurationFromEnv(EnvAgyInteractiveStalePaneBackstopSeconds, defaultAgyInteractiveStalePaneBackstop)
}

func agyInteractiveRetention() time.Duration {
	return tmuxlaunch.Retention(defaultAgyInteractiveRetention)
}

func agyInteractivePromptWait() time.Duration {
	return tmuxlaunch.PromptWait(EnvAgyInteractivePromptWaitSeconds)
}

func agyInteractiveStreamTmuxScreenEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvAgyInteractiveStreamTmuxScreen))) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func agyDurationFromEnv(key string, fallback time.Duration) time.Duration {
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

func agyDurationFromEnvAllowZero(key string, fallback time.Duration) time.Duration {
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

func runAgyCommand(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	_, err := runAgyCommandOutput(ctx, stdin, name, args...)
	return err
}

func runAgyCommandOutput(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
	return tmuxexec.RunCommandOutput(ctx, stdin, nil, name, args...)
}

func agyShellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = agyShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func agyShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func agyMustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func agyRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sanitizeAgyTmuxSessionName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "agy"
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

func stripAgyANSI(s string) string {
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

// stripAgyANSIPreserveColors strips ANSI cursor positioning / clear-screen /
// erase-line sequences but preserves SGR (Select Graphic Rendition: color,
// bold, dim, underline, etc., terminated with `m`). The frontend feeds this
// output through ansi_up to colorize the rendered pane snapshot. Cursor
// positioning is dropped because ansi_up does not emulate VT100 movement,
// so leaving those sequences in would print as garbage. SGR is the only
// class of escape that's safe to forward verbatim.
func stripAgyANSIPreserveColors(s string) string {
	return paneview.StripANSIPreserveColors(s)
}

var (
	agyStatusLineStreamMu sync.Mutex
	agyStatusLineStreamed = make(map[string]string) // sessionName -> raw JSON content of last streamed statusline
)

func agyStatuslinePath(sessionName string) string {
	workingDir := getAgyActiveSessionWorkingDir(sessionName)
	var persistent bool
	agyPersistentRegistry.Lock()
	for _, sess := range agyPersistentRegistry.sessions {
		if sess != nil && (sess.tmuxSessionName == sessionName || sess.ownerSessionID == sessionName) {
			workingDir = sess.workingDir
			persistent = sess.persistent
			break
		}
	}
	agyPersistentRegistry.Unlock()

	if persistent {
		// Persistent session (main agent) runs on the host (no workspace isolation).
		// We use a global, secure, stable path in /tmp to avoid polluting workingDir.
		// /tmp is world-writable, so both backend process and tmux session can access it
		// without TMPDIR-based environment mismatches.
		return filepath.Join("/tmp", fmt.Sprintf("agy_statusline_%s.json", sessionName))
	}

	if workingDir != "" {
		// Bounded sessions (workflow steps) run in isolated environments (workspace isolation).
		// They can only read/write files inside the mounted workingDir.
		return filepath.Join(workingDir, fmt.Sprintf("agy_statusline_%s.json", sessionName))
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("agy_statusline_%s.json", sessionName))
}

func configureAgyStatusline(ctx context.Context, session *agyInteractiveSession, logger interfaces.Logger) error {
	var tempDir string
	var err error
	if session.persistent {
		tempDir, err = os.MkdirTemp("/tmp", "agy-statusline-config-*")
	} else {
		tempDir, err = os.MkdirTemp(session.workingDir, "agy-statusline-config-*")
	}
	if err != nil {
		return err
	}
	outputPath := agyStatuslinePath(session.tmuxSessionName)
	scriptPath := filepath.Join(tempDir, "statusline.sh")

	// Create helper script that cat's stdin to outputPath
	scriptContent := fmt.Sprintf("#!/bin/sh\ncat > %s\n", agyShellQuote(outputPath))
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}

	command := "/statusline sh " + agyShellQuote(scriptPath)
	if err := typeAgyInputToTmux(ctx, session.tmuxSessionName, command); err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}

	// Register a cleanup file helper so the temp dir gets removed on session exit
	oldCleanup := session.cleanupFiles
	session.cleanupFiles = func() {
		if oldCleanup != nil {
			oldCleanup()
		}
		_ = os.RemoveAll(tempDir)
		_ = os.Remove(outputPath)
	}
	return nil
}

func streamAgyStatusLine(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) bool {
	if streamChan == nil {
		return false
	}
	outputPath := agyStatuslinePath(sessionName)
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return false
	}

	// Deduplicate streams
	agyStatusLineStreamMu.Lock()
	last := agyStatusLineStreamed[sessionName]
	if last == trimmed {
		agyStatusLineStreamMu.Unlock()
		return false
	}
	agyStatusLineStreamed[sessionName] = trimmed
	agyStatusLineStreamMu.Unlock()

	// Parse the raw JSON
	var payload struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		TotalInputTokens         int `json:"total_input_tokens"`
		TotalOutputTokens        int `json:"total_output_tokens"`
		ContextWindow            struct {
			TotalInputTokens  int `json:"total_input_tokens"`
			TotalOutputTokens int `json:"total_output_tokens"`
			CurrentUsage      struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			} `json:"current_usage"`
		} `json:"context_window"`
	}

	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return false
	}

	// Build the generic StatusLine. Use the adapter's canonical display name
	// ("agy-cli", as used everywhere else in this adapter) so consumers can
	// render the provider verbatim without re-mapping the id.
	status := &llmtypes.StatusLine{
		Provider: "agy-cli",
	}

	// Look up the session to get the model ID
	var modelID string
	agyPersistentRegistry.Lock()
	for _, sess := range agyPersistentRegistry.sessions {
		if sess != nil && sess.tmuxSessionName == sessionName {
			modelID = sess.modelID
			break
		}
	}
	agyPersistentRegistry.Unlock()
	// "agy-cli" is the placeholder model id used when no real model is
	// configured; it equals the provider name, so emitting it as Model renders a
	// duplicate "agy-cli · agy-cli". Only set a model when it's a distinct value.
	if modelID != "" && modelID != "agy-cli" {
		status.Model = modelID
	}

	// Map token counts
	current := payload.ContextWindow.CurrentUsage
	if current.InputTokens > 0 || current.OutputTokens > 0 || current.CacheCreationInputTokens > 0 || current.CacheReadInputTokens > 0 {
		status.InputTokens = current.InputTokens
		status.OutputTokens = current.OutputTokens
		status.CacheCreationInputTokens = current.CacheCreationInputTokens
		status.CacheReadInputTokens = current.CacheReadInputTokens
	} else {
		status.InputTokens = payload.InputTokens
		status.OutputTokens = payload.OutputTokens
		status.CacheCreationInputTokens = payload.CacheCreationInputTokens
		status.CacheReadInputTokens = payload.CacheReadInputTokens
	}

	if status.InputTokens <= 0 {
		if payload.ContextWindow.TotalInputTokens > 0 {
			status.InputTokens = payload.ContextWindow.TotalInputTokens
		} else if payload.TotalInputTokens > 0 {
			status.InputTokens = payload.TotalInputTokens
		}
	}
	if status.OutputTokens <= 0 {
		if payload.ContextWindow.TotalOutputTokens > 0 {
			status.OutputTokens = payload.ContextWindow.TotalOutputTokens
		} else if payload.TotalOutputTokens > 0 {
			status.OutputTokens = payload.TotalOutputTokens
		}
	}

	status.TotalInputTokens = payload.TotalInputTokens
	if status.TotalInputTokens <= 0 {
		status.TotalInputTokens = payload.ContextWindow.TotalInputTokens
	}
	status.TotalOutputTokens = payload.TotalOutputTokens
	if status.TotalOutputTokens <= 0 {
		status.TotalOutputTokens = payload.ContextWindow.TotalOutputTokens
	}

	// We can also put raw payload or extra metadata fields if needed
	var rawMap map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &rawMap); err == nil {
		status.Metadata = rawMap
	}
	// Tag the owning tmux session so downstream consumers can attribute this
	// telemetry to the exact coding-agent pane (a session may host several).
	if status.Metadata == nil {
		status.Metadata = map[string]interface{}{}
	}
	status.Metadata["tmux_session"] = sessionName

	select {
	case streamChan <- llmtypes.StreamChunk{
		Type:       llmtypes.StreamChunkTypeStatusLine,
		StatusLine: status,
	}:
		return true
	default:
		return false
	}
}

// GetStatusLine retrieves a snapshot of the current statusline for the active session.
// Satisfies the llmtypes.StatusLineProvider interface.
func (c *AgyCLIAdapter) GetStatusLine(ctx context.Context, sessionID string) (*llmtypes.StatusLine, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}

	// Find the session in the registry
	var tmuxSessionName string
	agyPersistentRegistry.Lock()
	for _, sess := range agyPersistentRegistry.sessions {
		if sess != nil && (sess.ownerSessionID == sessionID || sess.tmuxSessionName == sessionID) {
			tmuxSessionName = sess.tmuxSessionName
			break
		}
	}
	agyPersistentRegistry.Unlock()

	if tmuxSessionName == "" {
		tmuxSessionName = sessionID // Fallback: treat sessionID as the tmux session name
	}

	outputPath := agyStatuslinePath(tmuxSessionName)
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read statusline payload: %w", err)
	}

	var payload struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		TotalInputTokens         int `json:"total_input_tokens"`
		TotalOutputTokens        int `json:"total_output_tokens"`
		ContextWindow            struct {
			TotalInputTokens  int `json:"total_input_tokens"`
			TotalOutputTokens int `json:"total_output_tokens"`
			CurrentUsage      struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			} `json:"current_usage"`
		} `json:"context_window"`
	}

	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse statusline payload: %w", err)
	}

	status := &llmtypes.StatusLine{
		Provider: "agy-cli",
	}
	// Skip the "agy-cli" placeholder model id so the label doesn't render a
	// duplicate "agy-cli · agy-cli" (see streamAgyStatusLine).
	if c.modelID != "" && c.modelID != "agy-cli" {
		status.Model = c.modelID
	}

	// Map token counts
	current := payload.ContextWindow.CurrentUsage
	if current.InputTokens > 0 || current.OutputTokens > 0 || current.CacheCreationInputTokens > 0 || current.CacheReadInputTokens > 0 {
		status.InputTokens = current.InputTokens
		status.OutputTokens = current.OutputTokens
		status.CacheCreationInputTokens = current.CacheCreationInputTokens
		status.CacheReadInputTokens = current.CacheReadInputTokens
	} else {
		status.InputTokens = payload.InputTokens
		status.OutputTokens = payload.OutputTokens
		status.CacheCreationInputTokens = payload.CacheCreationInputTokens
		status.CacheReadInputTokens = payload.CacheReadInputTokens
	}

	if status.InputTokens <= 0 {
		if payload.ContextWindow.TotalInputTokens > 0 {
			status.InputTokens = payload.ContextWindow.TotalInputTokens
		} else if payload.TotalInputTokens > 0 {
			status.InputTokens = payload.TotalInputTokens
		}
	}
	if status.OutputTokens <= 0 {
		if payload.ContextWindow.TotalOutputTokens > 0 {
			status.OutputTokens = payload.ContextWindow.TotalOutputTokens
		} else if payload.TotalOutputTokens > 0 {
			status.OutputTokens = payload.TotalOutputTokens
		}
	}

	status.TotalInputTokens = payload.TotalInputTokens
	if status.TotalInputTokens <= 0 {
		status.TotalInputTokens = payload.ContextWindow.TotalInputTokens
	}
	status.TotalOutputTokens = payload.TotalOutputTokens
	if status.TotalOutputTokens <= 0 {
		status.TotalOutputTokens = payload.ContextWindow.TotalOutputTokens
	}

	var rawMap map[string]interface{}
	if err := json.Unmarshal(raw, &rawMap); err == nil {
		status.Metadata = rawMap
	}

	return status, nil
}
