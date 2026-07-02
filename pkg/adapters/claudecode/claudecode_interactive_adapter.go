package claudecode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/paneview"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

const (
	// Default to no provider-level turn timeout. Workflow/background callers own
	// their execution deadline; the adapter should not cancel a still-running tmux
	// coding agent before the outer workflow timeout.
	defaultTmuxTimeout           = 0
	defaultPersistentIdleTimeout = 3 * time.Hour
	defaultBoundedRetention      = 30 * time.Minute
	defaultTmuxPollInterval      = 750 * time.Millisecond
	defaultTmuxCaptureLines      = "10000"
	defaultTmuxHistoryLimit      = "20000"
	minTmuxMajorVersion          = 3
	claudeIdleStableWindow       = 1200 * time.Millisecond
	claudeTailFallbackMaxLines   = 120
	// defaultClaudeInteractiveStalePaneBackstop is a detection-independent
	// backstop for the assistant-response wait loop: if the pane has produced
	// activity and then frozen (byte-identical) for this long, the turn is over
	// but ready-prompt detection failed to recognize it. Generous so it never
	// trips a real turn. Set the env var to 0 to disable.
	defaultClaudeInteractiveStalePaneBackstop = 120 * time.Second
	promptPasteVisibleStableWindow            = 900 * time.Millisecond
	promptPasteInvisibleGrace                 = 1500 * time.Millisecond
	promptPasteLiveInputWait                  = 250 * time.Millisecond
	// Compaction handling. While Claude Code is compacting/summarizing the
	// conversation it replaces the input box with a "Compacting…/Summarizing…"
	// status and refuses input — pasting+submitting into that window fuses our
	// text onto a prior draft or leaves it stuck unsubmitted. So a send waits for
	// compaction to finish first, then paste/submit runs against a real prompt.
	// This is the ONLY window where a send blocks; outside it the wait is a
	// single cheap pane read and the send stays fast.
	claudeCompactionMaxWait      = 5 * time.Minute
	claudeCompactionPollInterval = 500 * time.Millisecond
	claudeCompactionEndGrace     = 300 * time.Millisecond
	// Clearing a stale draft. A stuck draft can be multi-line (e.g. a previously
	// unsubmitted auto-notification), and one C-u only clears the current line —
	// so the earlier lines remain and the next paste stacks onto them. Repeat the
	// clear until the input line reads empty, bounded so a non-clearing TUI can't
	// spin forever.
	claudeDraftClearMaxRounds = 8
	claudeDraftClearSettle    = 100 * time.Millisecond

	EnvClaudeTmuxSessionPrefix     = "CLAUDE_CODE_TMUX_SESSION_PREFIX"
	EnvClaudeTmuxTimeoutSeconds    = "CLAUDE_CODE_TMUX_TIMEOUT_SECONDS"
	EnvClaudeTmuxPromptWaitSeconds = "CLAUDE_CODE_TMUX_PROMPT_WAIT_SECONDS"
	// EnvClaudeTmuxPromptMaxWaitSeconds caps the total time waitForTmuxPrompt
	// will wait for a ready input prompt while Claude is actively working
	// (e.g. compacting a long conversation on resume). The prompt-wait above
	// is a sliding inactivity window; this is the absolute backstop.
	EnvClaudeTmuxPromptMaxWaitSeconds = "CLAUDE_CODE_TMUX_PROMPT_MAX_WAIT_SECONDS"
	EnvClaudeTmuxIdleTimeoutSeconds   = "CLAUDE_CODE_TMUX_IDLE_TIMEOUT_SECONDS"
	EnvClaudeTmuxStreamTmuxScreen     = "CLAUDE_CODE_STREAM_TMUX_SCREEN"
	// EnvClaudeInteractiveStalePaneBackstopSeconds bounds how long the
	// assistant-response loop will keep waiting on a pane that produced activity
	// and then went byte-identical without ever reaching a ready prompt. Set to
	// 0 to disable the backstop.
	EnvClaudeInteractiveStalePaneBackstopSeconds = "CLAUDE_CODE_INTERACTIVE_STALE_PANE_BACKSTOP_SECONDS"

	// Legacy env names kept for existing deployments and test runners.
	EnvClaudeExperimentalSessionPrefix      = "CLAUDE_CODE_EXPERIMENTAL_SESSION_PREFIX"
	EnvClaudeExperimentalTimeoutSeconds     = "CLAUDE_CODE_EXPERIMENTAL_TIMEOUT_SECONDS"
	EnvClaudeExperimentalPromptWaitSeconds  = "CLAUDE_CODE_EXPERIMENTAL_PROMPT_WAIT_SECONDS"
	EnvClaudeExperimentalIdleTimeoutSeconds = "CLAUDE_CODE_EXPERIMENTAL_IDLE_TIMEOUT_SECONDS"
	EnvClaudeExperimentalStreamTmuxScreen   = EnvClaudeTmuxStreamTmuxScreen
	EnvClaudePromptSuggestion               = "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"
)

var claudeInteractiveSessionRegistry = struct {
	sync.Mutex
	sessions map[string]struct{}
}{
	sessions: map[string]struct{}{},
}

var claudeLiveInputSubmitBackoff = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
}

var claudeInteractiveOwnerRegistry = struct {
	sync.RWMutex
	sessions map[string]string
}{
	sessions: map[string]string{},
}

type claudeInteractivePersistentSession struct {
	ownerSessionID  string
	tmuxSessionName string
	nativeSessionID string
	workingDir      string
	tempFiles       []string
	idleTimer       *time.Timer
	initErr         error
	createdAt       time.Time
	lastUsed        time.Time
	mu              sync.Mutex
}

var claudeInteractivePersistentRegistry = struct {
	sync.Mutex
	sessions map[string]*claudeInteractivePersistentSession
}{
	sessions: map[string]*claudeInteractivePersistentSession{},
}

func newClaudeCallContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	var callCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		callCtx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		callCtx, cancel = context.WithCancel(context.Background())
	}
	done := make(chan struct{})
	var once sync.Once

	go func() {
		select {
		case <-parent.Done():
			if parent.Err() == context.Canceled {
				cancel()
			}
		case <-done:
		}
	}()

	return callCtx, func() {
		once.Do(func() {
			close(done)
			cancel()
		})
	}
}

// ClaudeCodeInteractiveAdapter runs Claude Code through its interactive tmux transport.
// It intentionally does not invoke `claude -p`.
type ClaudeCodeInteractiveAdapter struct {
	modelID string
	logger  interfaces.Logger
}

func NewClaudeCodeInteractiveAdapter(modelID string, logger interfaces.Logger) *ClaudeCodeInteractiveAdapter {
	return newClaudeCodeInteractiveAdapter(modelID, logger)
}

func newClaudeCodeInteractiveAdapter(modelID string, logger interfaces.Logger) *ClaudeCodeInteractiveAdapter {
	if modelID == "" {
		modelID = "claude-code"
	}
	return &ClaudeCodeInteractiveAdapter{
		modelID: modelID,
		logger:  logger,
	}
}

func (c *ClaudeCodeInteractiveAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}
	// Defensive backstop for direct adapter callers that bypass the
	// orchestrator's image-content pre-processing. In practice, callers
	// running through mcp-agent-builder-go funnel CLI image input as a
	// TEXT message containing the absolute workspace path (see
	// workspace_advanced_tools.go:1509-1517 `pathBasedImageAnalysisProvider`
	// branch); the model uses its filesystem-read capability to view the
	// file. So image input across all CLI providers IS uniform — text +
	// path — and this rejection rarely fires.
	if containsClaudeImageContent(messages) {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, fmt.Errorf("claude-code tmux transport does not support llmtypes.ImageContent yet")
	}

	return llmtypes.WithObservability(ctx, llmtypes.ObservabilityConfig{
		Provider:     "claudecode",
		Model:        c.modelID,
		Opts:         opts,
		MessageCount: len(messages),
		Messages:     messages,
		HeaderLine:   fmt.Sprintf("claude (tmux) model=%s msgs=%d", c.modelID, len(messages)),
		RequestMetaExtra: map[string]interface{}{
			"transport": "tmux",
		},
	}, func(sink *llmtypes.StreamSink) (*llmtypes.ContentResponse, error) {
		_ = sink
		return c.generateContentTmuxBody(ctx, opts, messages)
	})
}

func (c *ClaudeCodeInteractiveAdapter) generateContentTmuxBody(ctx context.Context, opts *llmtypes.CallOptions, messages []llmtypes.MessageContent) (*llmtypes.ContentResponse, error) {
	if err := ensureTmuxAvailable(ctx); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claude cli not found in PATH; install and authenticate Claude Code first")
	}

	resumeID := claudeResumeIDFromOptions(opts)
	interactiveSessionID := claudeInteractiveSessionIDFromOptions(opts)
	persistentInteractive := claudePersistentInteractiveFromOptions(opts) && interactiveSessionID != ""

	// On user-initiated cancellation, tear down the persistent tmux
	// session so the live pane closes alongside the workflow step.
	defer func() {
		if interactiveSessionID == "" || ctx.Err() != context.Canceled {
			return
		}
		closeClaudePersistentInteractiveSession(interactiveSessionID, "workflow context canceled", c.logger)
	}()
	nativeSessionID := resumeID
	if nativeSessionID == "" {
		nativeSessionID = newClaudeNativeSessionID()
	}
	workingDir := claudeWorkingDirFromOptions(opts)

	// Capture turn-start before launching the CLI so we only aggregate
	// usage from JSONL events emitted during this turn (the transcript
	// persists prior turns when --resume is used).
	turnStart := time.Now().UTC()

	callCtx, cancel := newClaudeCallContext(ctx, tmuxTimeout())
	defer cancel()

	runID := "mlp-claude-run-" + randomHex(8)

	systemPrompt, conversationMessages := splitSystemPrompt(messages)

	var sessionName string
	var persistentSession *claudeInteractivePersistentSession
	releasePersistentSession := false
	discardPersistentSession := func(err error) {
		if persistentInteractive && persistentSession != nil {
			markClaudePersistentInteractiveSessionFailedLocked(persistentSession, err, c.logger)
			releasePersistentSession = false
			failedSession := persistentSession
			persistentSession = nil
			failedSession.mu.Unlock()
			cleanupFailedClaudePersistentInteractiveSession(failedSession)
		}
	}
	defer func() {
		if releasePersistentSession && persistentSession != nil {
			releaseClaudePersistentInteractiveSession(persistentSession, c.logger)
		}
	}()

	if persistentInteractive {
		var err error
		persistentSession, err = c.acquirePersistentInteractiveSession(callCtx, interactiveSessionID, nativeSessionID, opts, systemPrompt, workingDir)
		if err != nil {
			return nil, err
		}
		releasePersistentSession = true
		sessionName = persistentSession.tmuxSessionName
		nativeSessionID = persistentSession.nativeSessionID
	} else {
		sessionName = newTmuxSessionName()
		args, tempFiles, err := c.buildClaudeArgs(opts, sessionName, nativeSessionID, systemPrompt)
		if err != nil {
			return nil, err
		}
		defer removeFiles(tempFiles)

		if err := c.startSession(callCtx, sessionName, args, workingDir); err != nil {
			return nil, err
		}
		registerClaudeInteractiveSession(sessionName)
		cleanupSession := cleanupClaudeInteractiveSessionAfter(sessionName, llmtypes.TmuxKillDelay)
		defer cleanupSession()
		if interactiveSessionID != "" {
			registerClaudeInteractiveOwner(interactiveSessionID, sessionName)
			defer unregisterClaudeInteractiveOwner(interactiveSessionID, sessionName)
		}
	}

	if err := waitForTmuxPrompt(callCtx, sessionName, opts.StreamChan); err != nil {
		discardPersistentSession(err)
		return nil, err
	}
	resetTmuxPaneForTurn(callCtx, sessionName)
	if err := waitForTmuxPrompt(callCtx, sessionName, opts.StreamChan); err != nil {
		discardPersistentSession(err)
		return nil, err
	}

	if llmtypes.CodingProviderLaunchOnlyFromOptions(opts) {
		var lastSnapshot string
		streamClaudeTerminalSnapshot(callCtx, sessionName, opts.StreamChan, &lastSnapshot)
		additional := map[string]interface{}{
			"provider":                           "claude-code",
			"claude_code_mode":                   "tmux",
			"claude_code_run_id":                 runID,
			"claude_code_session":                sessionName,
			"claude_code_session_id":             nativeSessionID,
			"claude_code_native_session_id":      nativeSessionID,
			"claude_code_resumed_session_id":     resumeID,
			"claude_code_uses_print_flag":        false,
			"claude_code_structured_streaming":   false,
			"claude_code_persistent_interactive": persistentInteractive,
		}
		gi := &llmtypes.GenerationInfo{Additional: additional}
		llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
			Provider:        "claude-code",
			Transport:       llmtypes.CodingProviderTransportTmux,
			NativeSessionID: nativeSessionID,
			TmuxSession:     sessionName,
			WorkingDir:      workingDir,
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

	prompt, err := buildTmuxPrompt(conversationMessages, opts, resumeID, persistentInteractive)
	if err != nil {
		return nil, err
	}

	c.logger.Infof("Executing Claude Code tmux session: %s", sessionName)
	paneBaseline, _ := captureTmuxPane(callCtx, sessionName)
	if err := sendPromptToTmux(callCtx, sessionName, prompt); err != nil {
		return nil, err
	}

	content, err := waitForMarkedResponse(callCtx, sessionName, "", "", paneBaseline, opts.StreamChan)
	if err != nil {
		if isClaudeTmuxSessionLostError(err) {
			discardPersistentSession(err)
		}
		if isContextCanceledError(err) {
			if interruptErr := interruptClaudeInteractiveSession(sessionName, c.logger); interruptErr != nil {
				if persistentInteractive && persistentSession != nil {
					discardPersistentSession(fmt.Errorf("Claude Code tmux session did not return to prompt after context cancellation: %w", interruptErr))
				}
			}
		}
		// opts.StreamChan close is owned by WithObservability.
		return nil, err
	}
	// Trailing-capture grace window — see llmtypes.RunTrailingPaneCapture.
	// Skip for persistent interactive sessions: they live past the call
	// and are scraped by other paths.
	if !persistentInteractive {
		llmtypes.RunTrailingPaneCapture(callCtx, opts.StreamChan,
			func(ctx context.Context) (string, error) {
				snap, err := captureTmuxPane(ctx, sessionName)
				if err != nil {
					return "", err
				}
				return strings.TrimRight(snap, "\n"), nil
			},
			map[string]interface{}{
				"tmux_session":                    sessionName,
				"claude_code_interactive_session": sessionName,
			},
		)
	}
	if persistentInteractive && persistentSession != nil {
		checkCtx, checkCancel := context.WithTimeout(context.Background(), time.Second)
		exists, checkErr := claudeTmuxSessionExists(checkCtx, sessionName)
		checkCancel()
		if checkErr == nil && !exists {
			discardPersistentSession(fmt.Errorf("Claude Code tmux session %s ended after response capture", sessionName))
		}
	}
	closeResumeRef := ""
	responseSessionID := nativeSessionID
	if !persistentInteractive {
		closeResumeRef = closeClaudeSessionForResume(sessionName, c.logger)
		if isUUIDLike(strings.TrimSpace(closeResumeRef)) {
			responseSessionID = closeResumeRef
		}
	}

	// opts.StreamChan close is owned by WithObservability.

	additional := map[string]interface{}{
		"provider":                           "claude-code",
		"claude_code_mode":                   "tmux",
		"claude_code_run_id":                 runID,
		"claude_code_session":                sessionName,
		"claude_code_session_id":             responseSessionID,
		"claude_code_native_session_id":      nativeSessionID,
		"claude_code_resumed_session_id":     resumeID,
		"claude_code_close_resume_ref":       closeResumeRef,
		"claude_code_uses_print_flag":        false,
		"claude_code_structured_streaming":   false,
		"claude_code_persistent_interactive": persistentInteractive,
	}
	if !persistentInteractive {
		// terminal_retention_seconds intentionally not set: the rail
		// snapshot stays until the user dismisses it via the X button.
		additional["claude_code_interactive_retention_seconds"] = int(boundedRetentionTimeout().Seconds())
	}

	// Best-effort usage extraction from the local JSONL transcript.
	// claude-code's tmux TUI does not emit usage on stdout, but its
	// session-id'd transcript file at ~/.claude/projects/<...>/<sid>.jsonl
	// records `usage` on every assistant event.
	gi := &llmtypes.GenerationInfo{Additional: additional}
	effectiveModel := c.modelID
	if usage, transcriptModel := readClaudeTranscriptUsage(responseSessionID, turnStart); usage != nil || transcriptModel != "" {
		if usage != nil {
			gi.PromptTokens = usage.PromptTokens
			gi.CompletionTokens = usage.CompletionTokens
			gi.TotalTokens = usage.TotalTokens
			gi.CachedContentTokens = usage.CachedContentTokens
			// Forward raw cache token keys (cache_read_input_tokens /
			// cache_creation_input_tokens) into the adapter's local
			// Additional map so they survive to the cost ledger's
			// extractCacheTokens, which keys off those raw names
			// rather than the typed CachedContentTokens field.
			for k, v := range usage.Additional {
				additional[k] = v
			}
		}
		if transcriptModel != "" {
			effectiveModel = transcriptModel
			additional["claude_code_model"] = transcriptModel
		}
		if transcriptModel != "" {
			if meta, _ := c.GetModelMetadata(transcriptModel); meta != nil {
				if cost := llmtypes.ComputeUSDCostFromMetadata(meta, gi); cost > 0 {
					additional["cost_usd_estimated"] = cost
					additional["cost_model_id"] = transcriptModel
				}
			}
		}
	}
	llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
		Provider:        "claude-code",
		Transport:       llmtypes.CodingProviderTransportTmux,
		NativeSessionID: responseSessionID,
		TmuxSession:     sessionName,
		WorkingDir:      workingDir,
		Model:           effectiveModel,
		Status:          llmtypes.CodingProviderSessionStatusIdle,
	})

	// Reconstruct the CLI's internal tool-use loop from the same
	// sidecar JSONL we read usage from, so workflow conversation logs
	// can splice the in-CLI text/tool_use/tool_result trail into
	// their persisted history. Best-effort: empty when the transcript
	// is missing, unparsable, or only contains the final assistant
	// text with no internal loop.
	if sidecarMsgs := readClaudeTranscriptMessages(responseSessionID, turnStart); len(sidecarMsgs) > 0 {
		llmtypes.AttachCodingProviderIntermediateMessages(gi, llmtypes.CodingProviderIntermediateMessages{
			Provider:  "claude-code",
			Transport: llmtypes.CodingProviderTransportTmux,
			Messages:  sidecarMsgs,
		})
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        content,
				StopReason:     "stop",
				GenerationInfo: gi,
			},
		},
	}, nil
}

func containsClaudeImageContent(messages []llmtypes.MessageContent) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch part.(type) {
			case llmtypes.ImageContent, *llmtypes.ImageContent:
				return true
			}
		}
	}
	return false
}

func (c *ClaudeCodeInteractiveAdapter) GetModelID() string {
	return c.modelID
}

func (c *ClaudeCodeInteractiveAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = c.modelID
	}

	switch modelID {
	case "claude-fable-5":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              "claude-code",
			ModelName:             "Claude Fable 5",
			ContextWindow:         1000000,
			InputCostPer1MTokens:  10.00,
			OutputCostPer1MTokens: 50.00,
		}, nil
	case "claude-opus-4-8":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              "claude-code",
			ModelName:             "Claude Opus 4.8",
			ContextWindow:         200000,
			InputCostPer1MTokens:  5.00,
			OutputCostPer1MTokens: 25.00,
		}, nil
	case "claude-opus-4-7":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              "claude-code",
			ModelName:             "Claude Opus 4.7",
			ContextWindow:         200000,
			InputCostPer1MTokens:  5.00,
			OutputCostPer1MTokens: 25.00,
		}, nil
	case "claude-opus-4-6":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              "claude-code",
			ModelName:             "Claude Opus 4.6",
			ContextWindow:         200000,
			InputCostPer1MTokens:  5.00,
			OutputCostPer1MTokens: 25.00,
		}, nil
	case "claude-sonnet-5":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              "claude-code",
			ModelName:             "Claude Sonnet 5",
			ContextWindow:         200000,
			InputCostPer1MTokens:  3.00,
			OutputCostPer1MTokens: 15.00,
		}, nil
	case "claude-sonnet-4-6":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              "claude-code",
			ModelName:             "Claude Sonnet 4.6",
			ContextWindow:         200000,
			InputCostPer1MTokens:  3.00,
			OutputCostPer1MTokens: 15.00,
		}, nil
	case "claude-haiku-4-5-20251001":
		return &llmtypes.ModelMetadata{
			ModelID:               modelID,
			Provider:              "claude-code",
			ModelName:             "Claude Haiku 4.5",
			ContextWindow:         200000,
			InputCostPer1MTokens:  1.00,
			OutputCostPer1MTokens: 5.00,
		}, nil
	default:
		return &llmtypes.ModelMetadata{
			ModelID:       modelID,
			Provider:      "claude-code",
			ModelName:     "Claude Code",
			ContextWindow: 200000,
		}, nil
	}
}

func (c *ClaudeCodeInteractiveAdapter) buildClaudeArgs(opts *llmtypes.CallOptions, sessionName, nativeSessionID, systemPrompt string) ([]string, []string, error) {
	extraArgs := []string{}
	var tempFiles []string
	toolsArg := ""
	resumeID := claudeResumeIDFromOptions(opts)

	if opts.Metadata != nil && opts.Metadata.Custom != nil {
		if mcpConfig, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpConfig) != "" {
			configPath, err := writeTempJSONConfig("claude-code-mcp-*.json", mcpConfig)
			if err != nil {
				return nil, nil, err
			}
			tempFiles = append(tempFiles, configPath)
			extraArgs = append(extraArgs, "--mcp-config", configPath, "--strict-mcp-config")
		}

		if sessionName != "" {
			settingsPath, sFiles, err := c.prepareStatusLineSettings(opts, sessionName)
			if err != nil {
				return nil, nil, err
			}
			tempFiles = append(tempFiles, sFiles...)
			extraArgs = append(extraArgs, "--settings", settingsPath)
		} else {
			if settings, ok := opts.Metadata.Custom[MetadataKeySettings].(string); ok && strings.TrimSpace(settings) != "" {
				settingsArg := settings
				if strings.HasPrefix(strings.TrimSpace(settings), "{") {
					settingsPath, err := writeTempJSONConfig("claude-code-settings-*.json", settings)
					if err != nil {
						return nil, nil, err
					}
					tempFiles = append(tempFiles, settingsPath)
					settingsArg = settingsPath
				}
				extraArgs = append(extraArgs, "--settings", settingsArg)
			}
		}

		if tools, ok := opts.Metadata.Custom[MetadataKeyTools].(string); ok {
			toolsArg = tools
		}
		if allowedTools, ok := opts.Metadata.Custom[MetadataKeyAllowedTools].(string); ok && strings.TrimSpace(allowedTools) != "" {
			extraArgs = append(extraArgs, "--allowed-tools", allowedTools)
		}
		if effort, ok := opts.Metadata.Custom[MetadataKeyEffort].(string); ok && strings.TrimSpace(effort) != "" {
			extraArgs = append(extraArgs, "--effort", effort)
		}
	} else {
		if sessionName != "" {
			settingsPath, sFiles, err := c.prepareStatusLineSettings(opts, sessionName)
			if err != nil {
				return nil, nil, err
			}
			tempFiles = append(tempFiles, sFiles...)
			extraArgs = append(extraArgs, "--settings", settingsPath)
		}
	}

	// --no-chrome prevents the "Claude in Chrome extension detected" startup modal
	// (offered in dontAsk mode when the Chrome extension is present), which would
	// otherwise block the input prompt and time the session out after 5m.
	args := []string{"claude", "--permission-mode", "dontAsk", "--no-chrome"}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	} else if nativeSessionID != "" {
		args = append(args, "--session-id", nativeSessionID, "--name", defaultClaudeDisplayName())
	}
	if c.shouldPassModelFlag() {
		args = append(args, "--model", c.modelID)
	}

	// projectedToClaudeMd records whether the system prompt was successfully
	// written to <workingDir>/CLAUDE.md below. In project-instruction-only
	// mode it gates whether we still pass --system-prompt-file: when the
	// CLAUDE.md projection succeeded we skip the flag (the prompt is applied
	// once, via project instructions); otherwise we fall back to the flag.
	projectedToClaudeMd := false

	// Project the system prompt into <workingDir>/CLAUDE.md (Claude
	// Code's project-instructions convention) with byte-restore on
	// cleanup. ON by default; operators that need to protect their own
	// CLAUDE.md can opt out with WithClaudeCodeWriteProjectInstructionFile(false).
	// By default the prompt is ALSO injected via --system-prompt-file below;
	// CLAUDE.md makes it visible inside the workspace for debugging and
	// downstream tooling that reads project instructions. With
	// WithProjectInstructionOnly(true) the --system-prompt-file injection is
	// skipped and CLAUDE.md becomes the sole carrier (no doubled prompt).
	//
	// When the same flag is on AND an MCP config was provided via
	// WithMCPConfig, we also project the MCP servers into
	// <workingDir>/.mcp.json (Claude Code's project-scoped MCP
	// convention) with byte-restore on cleanup so operator-owned
	// .mcp.json content survives the session.
	if writeProjectInstructionFromOptions(opts) && opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		workingDir, _ := opts.Metadata.Custom[MetadataKeyWorkingDir].(string)
		restoreProjectFiles := restoreProjectFilesFromOptions(opts)
		if strings.TrimSpace(systemPrompt) != "" {
			if rulePath, err := writeClaudeCodeProjectInstructionFile(workingDir, systemPrompt, restoreProjectFiles); err != nil {
				// Best-effort: a write failure here must not block the
				// session. projectedToClaudeMd stays false so the
				// --system-prompt-file fallback below still fires.
				_ = err
			} else if rulePath != "" {
				tempFiles = append(tempFiles, rulePath)
				projectedToClaudeMd = true
			}
		}
		// .mcp.json projection is intentionally gated behind an env
		// var: dropping it triggers Claude Code's "New MCP server found
		// in .mcp.json — approve?" discovery prompt at startup, which
		// the tmux adapter cannot dismiss. Pre-recording approval in
		// ~/.claude.json (enabledMcpjsonServers + mcpServers) was
		// verified NOT to suppress the prompt on Claude Code v2.1.150.
		// The --mcp-config <temp> + --strict-mcp-config flags above
		// already load the MCP servers without triggering the prompt,
		// so disabling this projection by default loses only the
		// workspace-visibility belt-and-suspenders, not the actual MCP
		// loading. Set MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS=1 to
		// turn it back on if you have a way to handle the prompt
		// (e.g. a tmux send-keys post-launch dismissal).
		if os.Getenv("MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS") != "" {
			if mcpConfig, ok := opts.Metadata.Custom[MetadataKeyMCPConfig].(string); ok && strings.TrimSpace(mcpConfig) != "" && strings.TrimSpace(workingDir) != "" {
				if mcpPath, err := writeClaudeCodeProjectMCPFile(workingDir, mcpConfig, restoreProjectFiles); err != nil {
					_ = err
				} else if mcpPath != "" {
					tempFiles = append(tempFiles, mcpPath)
				}
			}
		}
	}

	// Inject the system prompt via --system-prompt-file UNLESS the caller
	// opted into project-instruction-only mode AND the CLAUDE.md projection
	// actually succeeded. In that case the prompt lives solely in CLAUDE.md
	// (auto-loaded as project instructions), so passing it here too would
	// double the system prompt. If CLAUDE.md was skipped or failed to write,
	// projectedToClaudeMd is false and we fall back to the flag so the prompt
	// is never silently dropped.
	if strings.TrimSpace(systemPrompt) != "" && !(projectInstructionOnlyFromOptions(opts) && projectedToClaudeMd) {
		systemPromptPath, err := writeTempFile("claude-code-system-prompt-*.md", systemPrompt)
		if err != nil {
			return nil, nil, err
		}
		tempFiles = append(tempFiles, systemPromptPath)
		args = append(args, "--system-prompt-file", systemPromptPath)
	}

	// Project attached skills into .claude/skills/ so Claude Code's
	// skill loader picks them up at startup. Independent of the
	// writeProjectInstructionFromOptions gate (which controls CLAUDE.md);
	// skills are useful even when the instruction-file projection is off.
	// Best-effort; matches the codex/gemini/cursor/agy/pi pattern.
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if workingDir, _ := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); strings.TrimSpace(workingDir) != "" {
			if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
				_ = c.ProjectSkills(workingDir, skills)
			}
		}
	}

	args = append(args, "--tools", toolsArg)
	args = append(args, extraArgs...)

	return args, tempFiles, nil
}

// writeProjectInstructionFromOptions reads the feature flag for writing
// the per-session system prompt to <workingDir>/CLAUDE.md. Defaults to
// true when the key is unset; callers can opt out by passing
// WithClaudeCodeWriteProjectInstructionFile(false).
func writeProjectInstructionFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return true
	}
	v, ok := opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile]
	if !ok {
		return true
	}
	enabled, _ := v.(bool)
	return enabled
}

// restoreProjectFilesFromOptions reads the OFF-by-default feature flag for
// preserving operator-owned project artifacts across a session. Returns
// false when the key is unset: the default is to write fresh and delete on
// cleanup, never restoring pre-existing content. Callers opt back into the
// legacy byte-restore behavior with WithRestoreProjectFiles(true).
func restoreProjectFilesFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyRestoreProjectFiles].(bool)
	return enabled
}

// projectInstructionOnlyFromOptions reads the OFF-by-default feature flag for
// injecting the system prompt only via CLAUDE.md (skipping --system-prompt-file).
// Returns false when the key is unset. Callers opt in with
// WithProjectInstructionOnly(true).
func projectInstructionOnlyFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyProjectInstructionOnly].(bool)
	return enabled
}

// writeClaudeCodeProjectInstructionFile installs the per-session system
// prompt at <workingDir>/CLAUDE.md (Claude Code's project-instructions
// convention). When restorePrior is true and a pre-existing CLAUDE.md is
// present, its bytes are registered with claudeProjectFileRestores so
// removeFiles restores them on session cleanup. When restorePrior is false
// (the default), no prior bytes are stashed: the freshly-written file is
// simply os.Remove'd on cleanup, so every run reflects the latest
// orchestrator output and never resurrects stale content. Returns the
// absolute path so the caller can append it to tempFiles for the existing
// cleanup flow.
//
// Returns "" with nil error when no work needs doing (empty workingDir);
// returns a non-empty path with non-nil error only when the write itself
// failed. The caller treats any error as best-effort and continues; the
// primary --system-prompt-file injection has already succeeded.
//
// Risk caveat: CLAUDE.md is a single-file convention. With restorePrior
// false, an operator's pre-existing CLAUDE.md is overwritten and removed
// without recovery — that is the intended default. Pass
// WithRestoreProjectFiles(true) to byte-restore operator content instead.
func writeClaudeCodeProjectInstructionFile(workingDir, systemPrompt string, restorePrior bool) (string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return "", fmt.Errorf("ensure claude working dir: %w", err)
	}
	path := filepath.Join(workingDir, "CLAUDE.md")
	if restorePrior {
		if prior, err := os.ReadFile(path); err == nil {
			claudeProjectFileRestores.Store(path, prior)
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("read pre-existing CLAUDE.md: %w", err)
		}
	}
	body := "<!-- mlp-session-instructions: orchestrator-generated per-session system prompt. Restored on cleanup. -->\n\n" + systemPrompt
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		claudeProjectFileRestores.Delete(path)
		return "", fmt.Errorf("write CLAUDE.md: %w", err)
	}
	return path, nil
}

func (c *ClaudeCodeInteractiveAdapter) shouldPassModelFlag() bool {
	modelID := strings.TrimSpace(c.modelID)
	return modelID != "" && modelID != "claude-code"
}

func (c *ClaudeCodeInteractiveAdapter) startSession(ctx context.Context, sessionName string, args []string, workingDir string) error {
	if workingDir != "" {
		// Pre-trust the working directory so Claude Code does not show its
		// interactive "Do you trust the files in this folder?" dialog, which
		// the adapter cannot dismiss in tmux mode and would cause a timeout.
		preTrustClaudeWorkingDir(workingDir)
	}
	shellCommand := claudeInteractiveShellCommand(args, workingDir)
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, claudePromptSuggestionEnvArgs()...)
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start Claude Code tmux session %q: %w", sessionName, err)
	}
	if err := runCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on"); err != nil {
		return fmt.Errorf("failed to configure Claude Code tmux session %q: %w", sessionName, err)
	}
	if err := runCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "history-limit", defaultTmuxHistoryLimit); err != nil {
		return fmt.Errorf("failed to configure Claude Code tmux history for session %q: %w", sessionName, err)
	}
	// Pin the window size to manual. The session is detached (no attached
	// client), so tmux's default window-size "latest" recomputes the size
	// from the most-recent client — and with zero clients it collapses to
	// default-size (80x24). When that happens the Claude Code TUI reflows
	// into half its width: box borders squish, columns compress, lines
	// double-wrap, and the captured pane becomes unreadable. "manual" freezes
	// the size we launched at (tmuxsize.Args) until the frontend explicitly
	// resize-windows it.
	_ = runCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "window-size", "manual")
	// Enable focus-events so the Claude Code TUI receives focus in/out
	// sequences and stops parking its "tmux focus-events off · add
	// 'set -g focus-events on'" nag line in the footer.
	_ = runCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "focus-events", "on")
	return nil
}

var preTrustClaudeMu sync.Mutex

// preTrustClaudeWorkingDir marks workingDir as trusted in ~/.claude.json so
// Claude Code skips its interactive "Do you trust the files in this folder?"
// dialog. Trust is recorded under projects.<path>.hasTrustDialogAccepted.
// On macOS /var is a symlink to /private/var, so we record both the raw and
// resolved paths. Errors are silently ignored — the session will still launch
// and the adapter will time out on the trust prompt rather than failing here.
func preTrustClaudeWorkingDir(workingDir string) {
	paths := []string{workingDir}
	if resolved, err := os.Readlink(workingDir); err == nil && resolved != workingDir {
		paths = append(paths, resolved)
	}
	// EvalSymlinks resolves the full chain (handles /var -> /private/var on macOS).
	if resolved, err := filepath.EvalSymlinks(workingDir); err == nil {
		seen := false
		for _, p := range paths {
			if p == resolved {
				seen = true
				break
			}
		}
		if !seen {
			paths = append(paths, resolved)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	configPath := filepath.Join(home, ".claude.json")

	preTrustClaudeMu.Lock()
	defer preTrustClaudeMu.Unlock()

	raw, readErr := os.ReadFile(configPath)
	var config map[string]interface{}
	if readErr == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &config)
	}
	if config == nil {
		config = map[string]interface{}{}
	}

	projects, _ := config["projects"].(map[string]interface{})
	if projects == nil {
		projects = map[string]interface{}{}
	}
	for _, p := range paths {
		entry, _ := projects[p].(map[string]interface{})
		if entry == nil {
			entry = map[string]interface{}{}
		}
		entry["hasTrustDialogAccepted"] = true
		entry["hasCompletedProjectOnboarding"] = true
		projects[p] = entry
	}
	config["projects"] = projects

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(configPath, out, 0o600)
}

func claudePromptSuggestionEnvArgs() []string {
	return []string{
		"-e", EnvClaudePromptSuggestion + "=false",
		// The tmux adapter relies on the user's Claude Code login. Inherited
		// Anthropic API env vars make recent Claude Code builds stop at an
		// interactive auth-choice prompt that this transport cannot answer.
		"-e", "ANTHROPIC_API_KEY=",
		"-e", "ANTHROPIC_BASE_URL=",
	}
}

func claudeInteractiveShellCommand(args []string, workingDir string) string {
	return shelllaunch.Command(args, workingDir)
}

func buildTmuxPrompt(messages []llmtypes.MessageContent, opts *llmtypes.CallOptions, resumeID string, persistentInteractive bool) (string, error) {
	var b strings.Builder

	latestIndex := latestHumanMessageIndex(messages)
	if persistentInteractive || resumeID != "" || len(messages) <= 1 {
		latest := latestMessageForPrompt(messages, latestIndex)
		if latest != nil {
			b.WriteString(tmuxMessagePartsToText(latest.Parts))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Previous conversation context:\n")
		for i, msg := range messages {
			if i == latestIndex {
				continue
			}
			b.WriteString("\n")
			b.WriteString(tmuxPromptRoleLabel(msg.Role))
			b.WriteString(":\n")
			b.WriteString(tmuxMessagePartsToText(msg.Parts))
			b.WriteString("\n")
		}
		if latestIndex >= 0 {
			b.WriteString("\nCurrent user message:\n")
			b.WriteString(tmuxMessagePartsToText(messages[latestIndex].Parts))
			b.WriteString("\n")
		}
	}

	if opts != nil && opts.JSONSchema != nil && opts.JSONSchema.Schema != nil {
		schemaBytes, err := json.Marshal(opts.JSONSchema.Schema)
		if err != nil {
			return "", fmt.Errorf("failed to marshal JSON schema: %w", err)
		}
		b.WriteString("\nReturn a response that conforms to this JSON schema:\n")
		b.Write(schemaBytes)
		b.WriteString("\n")
	}

	return b.String(), nil
}

func latestMessageForPrompt(messages []llmtypes.MessageContent, latestHumanIndex int) *llmtypes.MessageContent {
	if latestHumanIndex >= 0 {
		return &messages[latestHumanIndex]
	}
	if len(messages) == 0 {
		return nil
	}
	return &messages[len(messages)-1]
}

func tmuxPromptRoleLabel(role llmtypes.ChatMessageType) string {
	switch role {
	case llmtypes.ChatMessageTypeHuman:
		return "User"
	case llmtypes.ChatMessageTypeAI:
		return "Assistant"
	case llmtypes.ChatMessageTypeSystem:
		return "System"
	case llmtypes.ChatMessageTypeTool:
		return "Tool"
	default:
		return "Message"
	}
}

func splitSystemPrompt(messages []llmtypes.MessageContent) (string, []llmtypes.MessageContent) {
	systemPrompts := make([]string, 0, 1)
	conversationMessages := make([]llmtypes.MessageContent, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			text := strings.TrimSpace(tmuxMessagePartsToText(msg.Parts))
			if text != "" {
				systemPrompts = append(systemPrompts, text)
			}
			continue
		}
		conversationMessages = append(conversationMessages, msg)
	}
	return strings.Join(systemPrompts, "\n\n"), conversationMessages
}

func tmuxMessagePartsToText(parts []llmtypes.ContentPart) string {
	if len(parts) == 0 {
		return ""
	}

	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch p := part.(type) {
		case llmtypes.TextContent:
			texts = append(texts, p.Text)
		case *llmtypes.TextContent:
			if p != nil {
				texts = append(texts, p.Text)
			}
		case map[string]interface{}:
			texts = append(texts, tmuxMapContentPartToText(p))
		case map[string]string:
			texts = append(texts, tmuxStringMapContentPartToText(p))
		case json.RawMessage:
			texts = append(texts, tmuxRawMessageContentPartToText(p))
		case []byte:
			texts = append(texts, tmuxRawMessageContentPartToText(json.RawMessage(p)))
		case llmtypes.ImageContent:
			texts = append(texts, fmt.Sprintf("[image content: source_type=%s media_type=%s]", p.SourceType, p.MediaType))
		case *llmtypes.ImageContent:
			if p != nil {
				texts = append(texts, fmt.Sprintf("[image content: source_type=%s media_type=%s]", p.SourceType, p.MediaType))
			}
		case llmtypes.ToolCallResponse:
			texts = append(texts, fmt.Sprintf("[tool result id=%s name=%s is_error=%t]\n%s", p.ToolCallID, p.Name, p.IsError, p.Content))
		case *llmtypes.ToolCallResponse:
			if p != nil {
				texts = append(texts, fmt.Sprintf("[tool result id=%s name=%s is_error=%t]\n%s", p.ToolCallID, p.Name, p.IsError, p.Content))
			}
		default:
			texts = append(texts, fmt.Sprintf("[unsupported content part %T]", part))
		}
	}

	return strings.Join(texts, "\n")
}

func tmuxMapContentPartToText(part map[string]interface{}) string {
	for _, key := range []string{"Text", "text", "Content", "content"} {
		if text, ok := part[key].(string); ok {
			return text
		}
	}
	if typeValue, _ := part["type"].(string); typeValue != "" {
		return fmt.Sprintf("[unsupported %s content part]", typeValue)
	}
	return fmt.Sprintf("[unsupported content part %T]", part)
}

func tmuxStringMapContentPartToText(part map[string]string) string {
	for _, key := range []string{"Text", "text", "Content", "content"} {
		if text, ok := part[key]; ok {
			return text
		}
	}
	if typeValue := part["type"]; typeValue != "" {
		return fmt.Sprintf("[unsupported %s content part]", typeValue)
	}
	return fmt.Sprintf("[unsupported content part %T]", part)
}

func tmuxRawMessageContentPartToText(part json.RawMessage) string {
	var decoded map[string]interface{}
	if err := json.Unmarshal(part, &decoded); err != nil {
		return fmt.Sprintf("[unsupported content part %T]", part)
	}
	return tmuxMapContentPartToText(decoded)
}

func latestHumanMessageIndex(messages []llmtypes.MessageContent) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llmtypes.ChatMessageTypeHuman {
			return i
		}
	}
	return -1
}

func claudeResumeIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if resumeID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		return strings.TrimSpace(resumeID)
	}
	return ""
}

func newClaudeNativeSessionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func defaultClaudeDisplayName() string {
	return "mcp-agent-" + time.Now().Format("20060102-150405")
}

func claudeInteractiveStreamTmuxScreenEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvClaudeExperimentalStreamTmuxScreen))) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func waitForTmuxPrompt(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) error {
	// promptWait is a sliding INACTIVITY window, not a hard cap on the whole
	// wait. On resume Claude Code frequently compacts the conversation, and a
	// large compaction routinely runs longer than promptWait. A fixed deadline
	// fired mid-compaction, so waitForTmuxPrompt returned a timeout and the
	// caller never sent the user's message. Instead we reset the inactivity
	// timer whenever Claude is actively working (the compaction spinner / "esc
	// to interrupt" / running progress lines), so a genuinely hung pane still
	// aborts after promptWait of silence while a busy-but-progressing pane is
	// allowed to finish. maxWait is the absolute backstop for a pane that
	// reports activity forever.
	promptWait := promptReadyTimeout()
	maxWait := promptReadyMaxWait(promptWait)
	deadline, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	ticker := time.NewTicker(defaultTmuxPollInterval)
	defer ticker.Stop()
	resumePromptHandled := false
	trustPromptHandled := false
	featurePromptsDismissed := 0
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	streamTerminalScreen := claudeInteractiveStreamTmuxScreenEnabled()

	lastActivityAt := time.Now()
	for {
		select {
		case <-deadline.Done():
			captured, _ := captureTmuxPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out after %s waiting for Claude Code prompt; %s", maxWait, llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return fmt.Errorf("timed out after %s waiting for Claude Code prompt", maxWait)
		case <-ticker.C:
			captured, err := captureTmuxPane(deadline, sessionName)
			if err != nil {
				if isClaudeTmuxSessionLostError(err) {
					return err
				}
				continue
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamClaudeTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if !resumePromptHandled {
				resumePromptKeys := claudeResumeCompressionPromptSubmitKeys(captured)
				if len(resumePromptKeys) > 0 {
					resumePromptHandled = true
					args := append([]string{"send-keys", "-t", sessionName}, resumePromptKeys...)
					if err := runCommand(deadline, nil, "tmux", args...); err != nil {
						return fmt.Errorf("failed to continue Claude Code resumed session without compaction: %w", err)
					}
					lastActivityAt = time.Now()
					continue
				}
			}
			// Claude Code shows a one-time "Is this a project you trust?" folder
			// safety prompt on first launch in a fresh workspace (the per-run temp
			// dir is always new), which blocks the input prompt and eventually times
			// out. Auto-accept it ("1. Yes, I trust this folder").
			if !trustPromptHandled && isClaudeTrustFolderPrompt(captured) {
				trustPromptHandled = true
				if err := runCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "1", "C-m"); err != nil {
					return fmt.Errorf("failed to accept Claude Code trust-folder prompt: %w", err)
				}
				lastActivityAt = time.Now()
				continue
			}
			// Catch-all for any OTHER numbered choice menu blocking the prompt — the
			// "Resume from summary / Resume full session" prompt, or a future startup
			// choice claude adds. Trust ("1. Yes") and feature-onboarding (Esc) are
			// handled above and excluded inside the predicate; here we accept claude's
			// highlighted recommended default with Enter (its convention for the safe
			// "proceed" option) so the session is never stuck on an unrecognized menu.
			if featurePromptsDismissed < 5 && isClaudeBlockingChoiceMenu(captured) {
				featurePromptsDismissed++
				if err := runCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "Enter"); err != nil {
					return fmt.Errorf("failed to accept Claude Code choice menu: %w", err)
				}
				lastActivityAt = time.Now()
				continue
			}
			// Claude Code shows optional onboarding/feature modals on startup
			// (e.g. "Claude in Chrome extension detected", "Try the new fullscreen
			// renderer?") which block the input prompt and time the session out.
			// Decline them with Esc. --no-chrome prevents the Chrome one; this is the
			// defensive, future-proof catch for the whole class. Capped so a prompt
			// Esc fails to clear can't spin forever. The trust-folder security prompt
			// is excluded (handled above with "1. Yes").
			if featurePromptsDismissed < 5 && isClaudeDismissableFeaturePrompt(captured) {
				featurePromptsDismissed++
				if err := runCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil {
					return fmt.Errorf("failed to dismiss Claude Code feature prompt: %w", err)
				}
				lastActivityAt = time.Now()
				continue
			}
			if hasReadyInputPrompt(captured) {
				return nil
			}
			// Reset the inactivity window while Claude is busy (compacting,
			// thinking, running tools) so a slow-but-progressing resume isn't
			// aborted before the input prompt appears.
			if hasClaudeActivity(captured) || isClaudeCompactionInProgress(captured) {
				lastActivityAt = time.Now()
				continue
			}
			if time.Since(lastActivityAt) >= promptWait {
				captured, _ := captureTmuxPane(context.Background(), sessionName)
				if strings.TrimSpace(captured) != "" {
					return fmt.Errorf("timed out after %s of inactivity waiting for Claude Code prompt; %s", promptWait, llmtypes.CompactTerminalPaneForError(sessionName, captured))
				}
				return fmt.Errorf("timed out after %s of inactivity waiting for Claude Code prompt", promptWait)
			}
		}
	}
}

// isClaudeTrustFolderPrompt detects Claude Code's first-launch "Is this a
// project you created or one you trust?" folder-trust dialog so the caller can
// auto-accept it instead of hanging until the inactivity timeout.
func isClaudeTrustFolderPrompt(captured string) bool {
	c := strings.ToLower(captured)
	return strings.Contains(c, "yes, i trust this folder") ||
		(strings.Contains(c, "trust this folder") && strings.Contains(c, "no, exit"))
}

// isClaudeDismissableFeaturePrompt detects claude's optional startup onboarding /
// feature modals — a numbered menu offering to turn on a new feature, with a
// decline option ("Not now", "keep browser tools off", "Maybe later", ...). These
// block the input prompt; Esc declines them safely. Examples: "Claude in Chrome
// extension detected", "Try the new fullscreen renderer?". The trust-folder
// security prompt is explicitly excluded (it needs an affirmative "1. Yes").
func isClaudeDismissableFeaturePrompt(captured string) bool {
	if isClaudeTrustFolderPrompt(captured) {
		return false
	}
	c := strings.ToLower(captured)
	hasMenu := strings.Contains(c, "❯ 1.") || (strings.Contains(c, "1.") && strings.Contains(c, "2."))
	hasDecline := strings.Contains(c, "not now") || strings.Contains(c, "maybe later") ||
		strings.Contains(c, "keep browser tools off") || strings.Contains(c, "no thanks") ||
		strings.Contains(c, "no, keep")
	return hasMenu && hasDecline
}

// isClaudeBlockingChoiceMenu is the generic catch-all for a numbered selection
// menu that blocks the input prompt and is explicitly awaiting a choice (e.g.
// "Resume from summary / Resume full session", or a future startup choice). It
// EXCLUDES the two cases with a known non-default action — the trust prompt
// (needs "1. Yes") and feature-onboarding prompts (need Esc to decline) — so it
// only fires on an otherwise-unrecognized choice, where pressing Enter accepts
// claude's highlighted recommended default (its convention for the safe
// "proceed" option). The "awaiting a choice" gate keeps it off normal output
// that merely contains "1." / "2.".
func isClaudeBlockingChoiceMenu(captured string) bool {
	if isClaudeTrustFolderPrompt(captured) || isClaudeDismissableFeaturePrompt(captured) {
		return false
	}
	c := strings.ToLower(captured)
	hasMenu := strings.Contains(c, "❯ 1.") || (strings.Contains(c, "1.") && strings.Contains(c, "2."))
	awaitingChoice := strings.Contains(c, "enter to confirm") || strings.Contains(c, "press enter")
	return hasMenu && awaitingChoice
}

func claudeResumeCompressionPromptSubmitKeys(captured string) []string {
	if isClaudeResumeSummaryMenu(captured) {
		return []string{"C-m"}
	}
	// Check TUI selection menu before the older text-based format \u2014 both may
	// contain "compact"+"continue", but the TUI menu needs arrow keys, not typing.
	if isClaudeResumeSelectMenu(captured) {
		return claudeResumeSelectMenuKeys(captured)
	}
	if isClaudeResumeCompressionPrompt(captured) {
		return []string{"continue", "C-m"}
	}
	return nil
}

func isClaudeResumeSummaryMenu(captured string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(captured, "\u00a0", " "))
	return strings.Contains(normalized, "resume from summary") &&
		strings.Contains(normalized, "enter to confirm") &&
		(strings.Contains(normalized, "resume full session") ||
			strings.Contains(normalized, "usage limits") ||
			strings.Contains(normalized, "substantial portion"))
}

// isClaudeResumeSelectMenu detects the interactive TUI selection menu Claude Code
// shows on resume when the conversation is long: a \u276f-cursor menu with a compact
// option and a "run/continue as is" option. The \u276f distinguishes it from the older
// text-based prompt where the user types "continue".
func isClaudeResumeSelectMenu(captured string) bool {
	normalized := strings.ReplaceAll(captured, "\u00a0", " ")
	lower := strings.ToLower(normalized)
	if !strings.Contains(normalized, "\u276f") {
		return false
	}
	hasCompact := strings.Contains(lower, "compact") || strings.Contains(lower, "compress")
	hasRunAsIs := strings.Contains(lower, "as is") ||
		strings.Contains(lower, "without compact") ||
		strings.Contains(lower, "without compress") ||
		strings.Contains(lower, "run as") ||
		strings.Contains(lower, "continue as") ||
		strings.Contains(lower, "continue without")
	return hasCompact && hasRunAsIs
}

// claudeResumeSelectMenuKeys returns the tmux key sequence to choose the
// "continue without compacting / run as is" option from the TUI selection menu.
// It checks which option the \u276f cursor is on: if it's already on the
// continue/as-is option, Enter is enough; if it's on the compact option,
// navigate down first.
func claudeResumeSelectMenuKeys(captured string) []string {
	lines := strings.Split(strings.ReplaceAll(captured, "\u00a0", " "), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "\u276f") {
			continue
		}
		lower := strings.ToLower(trimmed)
		// A line is the "continue/run-as-is" option when it has qualifier words
		// like "without", "as is", "run as", or "continue as". These take
		// priority over "compact" appearing in the same line (e.g. "without compacting").
		isRunAsIs := strings.Contains(lower, "without") ||
			strings.Contains(lower, "as is") ||
			strings.Contains(lower, "run as") ||
			strings.Contains(lower, "continue as") ||
			strings.Contains(lower, "continue without")
		isCompact := (strings.Contains(lower, "compact") || strings.Contains(lower, "compress")) && !isRunAsIs
		if isCompact {
			// Cursor is on the compact option \u2014 move to the continue/as-is option below.
			return []string{"Down", "C-m"}
		}
		// Cursor is already on continue/run-as-is.
		return []string{"C-m"}
	}
	// Fallback: accept whatever is selected.
	return []string{"C-m"}
}

func isClaudeResumeCompressionPrompt(captured string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(captured, "\u00a0", " "))
	// Match the DISTINCTIVE wording of Claude Code's legacy text resume prompt
	// ("Would you like to compact the conversation or continue without
	// compacting?"), not loose keyword co-occurrence. The old check matched any
	// pane containing compact/compress + continue + resume/conversation/context
	// \u2014 which normal scrollback frequently does \u2014 so we'd type a stray "continue"
	// into whatever input was focused, garbling real drafts (e.g. turning a
	// "1. do it" prompt into "1. do itcontinue"). These phrases only appear in
	// the actual prompt.
	return strings.Contains(normalized, "without compacting") ||
		strings.Contains(normalized, "without compaction") ||
		strings.Contains(normalized, "continue without compact") ||
		(strings.Contains(normalized, "would you like to compact") && strings.Contains(normalized, "continue"))
}

func sendPromptToTmux(ctx context.Context, sessionName, prompt string) error {
	bufferName := "mlp-claude-prompt-" + randomHex(6)
	prompt = strings.TrimRight(prompt, "\n")

	if err := waitForClaudeCompactionToSettle(ctx, sessionName); err != nil {
		return err
	}
	if err := clearClaudePromptDraftBeforePaste(ctx, sessionName); err != nil {
		return err
	}
	paneBeforePaste, _ := captureTmuxPane(ctx, sessionName)
	if err := runCommand(ctx, strings.NewReader(prompt), "tmux", "load-buffer", "-b", bufferName, "-"); err != nil {
		return fmt.Errorf("failed to load prompt into tmux buffer: %w", err)
	}
	if err := runCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-p", "-r", "-b", bufferName, "-t", sessionName); err != nil {
		return fmt.Errorf("failed to paste prompt into Claude Code tmux session: %w", err)
	}
	// Wait for the pasted draft to appear, best-effort. Do NOT early-return on a
	// "submitted" signal: a bracketed paste never auto-submits in Claude Code, so
	// any "looks submitted" reading here is a false positive (usually residual
	// activity from the agent's just-finished turn). Trusting it skipped the
	// submit keystroke entirely, leaving the prompt (e.g. an AUTO-NOTIFICATION)
	// sitting unsubmitted in the ❯ box until the response wait hit its inactivity
	// timeout ("all LLMs failed … waiting for Claude Code prompt"). So always run
	// the submit loop and verify the draft actually cleared.
	if _, err := waitForPromptPaste(ctx, sessionName, paneBeforePaste); err != nil {
		if ctx.Err() != nil || isClaudeTmuxSessionLostError(err) {
			return err
		}
		// Paste detection can time out even when the draft is visible (Claude's
		// busy status line changes wording); still attempt the submit so the
		// prompt doesn't sit unsubmitted.
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		preSubmitPane, _ := captureTmuxPane(ctx, sessionName)
		args := append([]string{"send-keys", "-t", sessionName}, claudeSubmitPromptKeys()...)
		if err := runCommand(ctx, nil, "tmux", args...); err != nil {
			return fmt.Errorf("failed to submit prompt to Claude Code tmux session: %w", err)
		}
		if err := waitForPromptAccepted(ctx, sessionName, preSubmitPane, prompt); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	return fmt.Errorf("Claude Code prompt did not start after submit retries: %w", lastErr)
}

func sendInputToActiveTmux(ctx context.Context, sessionName, message string) error {
	bufferName := "mlp-claude-steer-" + randomHex(6)
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Claude Code tmux input is empty")
	}
	if err := waitForClaudeCompactionToSettle(ctx, sessionName); err != nil {
		return err
	}
	if err := clearClaudePromptDraftBeforePaste(ctx, sessionName); err != nil {
		return err
	}
	paneBeforePaste, _ := captureTmuxPane(ctx, sessionName)
	if err := runCommand(ctx, strings.NewReader(message), "tmux", "load-buffer", "-b", bufferName, "-"); err != nil {
		return fmt.Errorf("failed to load Claude Code tmux input into tmux buffer: %w", err)
	}
	if err := runCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-p", "-r", "-b", bufferName, "-t", sessionName); err != nil {
		return fmt.Errorf("failed to paste input into Claude Code tmux session: %w", err)
	}
	if _, err := waitForPromptPasteWithTimeout(ctx, sessionName, paneBeforePaste, promptPasteLiveInputWait); err != nil {
		if ctx.Err() != nil || isClaudeTmuxSessionLostError(err) {
			return err
		}
		// Live steering is best-effort after paste-buffer succeeds. Claude Code's
		// busy status line changes wording often, so paste detection can time out
		// even though the draft is already visible in the TUI. Still submit once
		// so the user's message does not sit unsubmitted in the input line.
	}
	var lastErr error
	for _, submitWait := range claudeLiveInputSubmitBackoff {
		args := append([]string{"send-keys", "-t", sessionName}, claudeSubmitPromptKeys()...)
		if err := runCommand(ctx, nil, "tmux", args...); err != nil {
			return fmt.Errorf("failed to submit input to Claude Code tmux session: %w", err)
		}
		if err := waitForClaudeLiveInputSubmitted(ctx, sessionName, message, submitWait); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("Claude Code tmux input remained unsubmitted after submit retries: %w", lastErr)
}

func claudeSubmitPromptKeys() []string {
	// Claude Code can leave a bracket-pasted live message as a visible draft when
	// Enter is sent while the cursor/focus is not at the accepted end of input.
	// Move to the end first, then submit.
	return []string{"C-e", "Enter"}
}

// waitForClaudeCompactionToSettle blocks while the pane is actively compacting or
// summarizing the conversation, returning once that finishes. Claude Code refuses
// input during compaction, so pasting then would mangle the message; waiting lets
// the subsequent paste/submit run against a restored prompt. It is a fast no-op
// (one pane read) when no compaction is in progress, so it adds no latency to the
// common path. On timeout it returns an error rather than send into a still-
// compacting pane.
func waitForClaudeCompactionToSettle(ctx context.Context, sessionName string) error {
	if captured, err := captureTmuxPane(ctx, sessionName); err == nil && !claudeCompactionBlocksInput(captured) {
		return nil
	}
	deadline, cancel := context.WithTimeout(ctx, claudeCompactionMaxWait)
	defer cancel()
	ticker := time.NewTicker(claudeCompactionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Claude Code still compacting after %s; not sending to avoid mangling input", claudeCompactionMaxWait)
		case <-ticker.C:
			captured, err := captureTmuxPane(deadline, sessionName)
			if err != nil {
				if isClaudeTmuxSessionLostError(err) {
					return err
				}
				continue
			}
			if !claudeCompactionBlocksInput(captured) {
				// If the box isn't back up yet (compaction just ended), give the
				// TUI a moment to restore the input line before we paste.
				if !hasReadyInputPrompt(captured) {
					sleepCtx(deadline, claudeCompactionEndGrace)
				}
				return nil
			}
		}
	}
}

// sleepCtx sleeps for d unless ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func clearClaudePromptDraftBeforePaste(ctx context.Context, sessionName string) error {
	captured, err := captureTmuxPane(ctx, sessionName)
	if err != nil {
		if isClaudeTmuxSessionLostError(err) {
			return err
		}
		return nil
	}
	draft, shouldClear := claudePromptDraftToClearBeforePaste(captured)
	if !shouldClear {
		return nil
	}
	// Repeat C-e + C-u until the input line is empty so multi-line stale drafts are
	// fully cleared (a single C-u leaves the earlier lines, which the next paste
	// would stack onto). This only runs once we have decided to clear (idle pane
	// with a real stale draft); the busy-pane gate above still protects legitimately
	// queued messages from being touched.
	for round := 0; round < claudeDraftClearMaxRounds; round++ {
		if err := runCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-e", "C-u"); err != nil {
			return fmt.Errorf("failed to clear stale Claude Code prompt draft %q: %w", truncateClaudeDraftForError(draft, 120), err)
		}
		sleepCtx(ctx, claudeDraftClearSettle)
		cleared, err := claudePromptDraftCleared(ctx, sessionName)
		if err != nil {
			if isClaudeTmuxSessionLostError(err) {
				return err
			}
			continue // transient capture/repaint; try the next round
		}
		if cleared {
			return nil
		}
	}
	// Did not confirm an empty line within the bounded rounds — fall back to the
	// timed wait so we still surface a failure rather than paste onto a dirty draft.
	if err := waitForClaudePromptDraftCleared(ctx, sessionName); err != nil {
		return fmt.Errorf("failed to clear stale Claude Code prompt draft %q: %w", truncateClaudeDraftForError(draft, 120), err)
	}
	return nil
}

// claudePromptDraftCleared reports whether the live ❯ input line is now empty or
// a placeholder — i.e. nothing stale remains for the next paste to stack onto. A
// missing ❯ line (transient repaint) is treated as not-yet-confirmed.
func claudePromptDraftCleared(ctx context.Context, sessionName string) (bool, error) {
	captured, err := captureTmuxPane(ctx, sessionName)
	if err != nil {
		return false, err
	}
	draft, placeholder, ok := latestClaudePromptDraftRaw(captured)
	if !ok {
		return false, nil
	}
	return strings.TrimSpace(draft) == "" || placeholder, nil
}

func claudePromptDraftToClearBeforePaste(captured string) (string, bool) {
	if !hasReadyInputPrompt(captured) {
		return "", false
	}
	draft, placeholder, ok := latestClaudePromptDraftRaw(captured)
	if !ok {
		return "", false
	}
	draft = strings.TrimSpace(draft)
	return draft, draft != "" && !placeholder
}

func latestClaudePromptDraftRaw(captured string) (draft string, placeholder bool, ok bool) {
	lines := strings.Split(captured, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "❯" {
			return "", false, true
		}
		if strings.HasPrefix(trimmed, "❯") {
			draft := strings.TrimSpace(strings.TrimPrefix(trimmed, "❯"))
			return draft, isClaudePromptPlaceholder(draft), true
		}
	}
	return "", false, false
}

func isClaudePromptPlaceholder(draft string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(draft), " "))
	if normalized == "" {
		return false
	}
	return strings.HasPrefix(normalized, "type your message") ||
		strings.HasPrefix(normalized, "press up to edit queued messages") ||
		(strings.HasPrefix(normalized, "try ") && strings.Contains(normalized, "\"")) ||
		normalized == "show me what it found"
}

func waitForClaudePromptDraftCleared(ctx context.Context, sessionName string) error {
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
				captured, _ = captureTmuxPane(context.Background(), sessionName)
			}
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out waiting for Claude Code prompt draft to clear; %s", llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return fmt.Errorf("timed out waiting for Claude Code prompt draft to clear")
		case <-ticker.C:
			captured, err := captureTmuxPane(deadline, sessionName)
			if err != nil {
				if isClaudeTmuxSessionLostError(err) {
					return err
				}
				continue
			}
			lastCaptured = captured
			draft, placeholder, ok := latestClaudePromptDraftRaw(captured)
			if ok && (strings.TrimSpace(draft) == "" || placeholder) {
				return nil
			}
		}
	}
}

func closeClaudeSessionForResume(sessionName string, logger interfaces.Logger) string {
	closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	promptCtx, promptCancel := context.WithTimeout(closeCtx, 10*time.Second)
	if err := waitForReadyInputPrompt(promptCtx, sessionName); err != nil && logger != nil {
		logger.Debugf("Claude Code tmux session %s was not visibly idle before close: %v", sessionName, err)
	}
	promptCancel()

	if err := runCommand(closeCtx, nil, "tmux", "send-keys", "-t", sessionName, "C-u", "/exit", "C-m"); err != nil {
		if logger != nil {
			logger.Errorf("Failed to close Claude Code tmux session %s cleanly: %v", sessionName, err)
		}
		return ""
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-closeCtx.Done():
			if logger != nil {
				logger.Errorf("Timed out waiting for Claude Code resume id from session %s", sessionName)
			}
			return ""
		case <-ticker.C:
			captured, err := captureTmuxPane(closeCtx, sessionName)
			if err != nil {
				if logger != nil {
					logger.Errorf("Failed to capture Claude Code close output for session %s: %v", sessionName, err)
				}
				return ""
			}
			if sessionID := parseClaudeResumeSessionID(captured); sessionID != "" {
				return sessionID
			}
			if strings.Contains(captured, "Pane is dead") {
				if logger != nil {
					logger.Errorf("Claude Code tmux session %s closed without a resume id", sessionName)
				}
				return ""
			}
		}
	}
}

func interruptClaudeInteractiveSession(sessionName string, logger interfaces.Logger) error {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := runCommand(interruptCtx, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil {
		if logger != nil {
			logger.Debugf("Failed to send Escape to Claude Code tmux session %s: %v", sessionName, err)
		}
		return err
	}
	if err := waitForReadyInputPrompt(interruptCtx, sessionName); err != nil {
		if logger != nil {
			logger.Debugf("Claude Code tmux session %s did not return to prompt after Escape: %v", sessionName, err)
		}
		return err
	}
	return nil
}

func isContextCanceledError(err error) bool {
	return err != nil && errors.Is(err, context.Canceled)
}

func resetTmuxPaneForTurn(ctx context.Context, sessionName string) {
	_ = ctx
	_ = sessionName
}

func waitForReadyInputPrompt(ctx context.Context, sessionName string) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			captured, err := captureTmuxPane(ctx, sessionName)
			if err != nil {
				continue
			}
			if hasReadyInputPrompt(captured) {
				return nil
			}
		}
	}
}

func hasReadyEmptyInputPrompt(captured string) bool {
	if !hasReadyInputPrompt(captured) {
		return false
	}
	draft, placeholder, ok := latestClaudePromptDraftRaw(captured)
	if !ok {
		return false
	}
	return strings.TrimSpace(draft) == "" || placeholder
}

func hasReadyInputPrompt(captured string) bool {
	normalized := strings.ReplaceAll(captured, "\u00a0", " ")
	lines := strings.Split(normalized, "\n")
	start := len(lines) - 80
	if start < 0 {
		start = 0
	}
	promptIndex := -1
	for i := len(lines) - 1; i >= start; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "❯" || strings.HasPrefix(trimmed, "❯ ") {
			promptIndex = i
			break
		}
		if trimmed == "" || isIgnorableClaudePromptFooterLine(trimmed) || isClaudeTUIBoundaryLine(trimmed) {
			continue
		}
		return false
	}
	if promptIndex < 0 {
		return false
	}
	for i := promptIndex - 1; i >= start; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || isClaudeTUIBoundaryLine(trimmed) {
			continue
		}
		cleaned := cleanClaudeTerminalProgressLine(trimmed)
		if isClaudeRunningProgressLine(trimmed) ||
			isClaudeRunningProgressLine(cleaned) ||
			isClaudeToolProgressLine(cleaned) {
			return false
		}
		break
	}
	return true
}

func isIgnorableClaudePromptFooterLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "⏵") ||
		strings.Contains(trimmed, "tmux focus-events") ||
		strings.Contains(trimmed, "set -g focus-events") ||
		// Claude Code CLI's upgrade-notice footer: "current: X · latest: Y".
		// When a new release is available the TUI parks this line at the
		// very bottom of the pane, between the input box and end of screen.
		// Without recognizing it, hasReadyInputPrompt walks up from the
		// end, hits this non-prompt line before finding ❯, and returns
		// false — so the wait loop never sees the agent as ready and
		// the adapter hangs indefinitely on every turn until the user
		// upgrades the CLI.
		(strings.Contains(trimmed, "current:") && strings.Contains(trimmed, "latest:"))
}

func hasClaudeActivity(captured string) bool {
	normalized := strings.ReplaceAll(captured, "\u00a0", " ")
	lines := strings.Split(normalized, "\n")
	if promptIndex := lastClaudePromptLineIndex(lines); promptIndex >= 0 {
		for i := promptIndex + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if strings.Contains(trimmed, "esc to interrupt") ||
				isClaudeRunningProgressLine(trimmed) {
				return true
			}
		}
		start := promptIndex - 8
		if start < 0 {
			start = 0
		}
		for i := promptIndex - 1; i >= start; i-- {
			trimmed := strings.TrimSpace(lines[i])
			if isClaudeRunningProgressLine(trimmed) {
				return true
			}
		}
		return false
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "esc to interrupt") ||
			isClaudeRunningProgressLine(trimmed) ||
			isClaudeToolProgressLine(trimmed) {
			return true
		}
	}
	return false
}

func lastClaudePromptLineIndex(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "❯" || strings.HasPrefix(trimmed, "❯ ") {
			return i
		}
	}
	return -1
}

func parseClaudeResumeSessionID(captured string) string {
	idx := strings.LastIndex(captured, "claude --resume")
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(captured[idx:])
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "--resume" {
			return strings.Trim(fields[i+1], "\"'`")
		}
	}
	return ""
}

func isUUIDLike(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, ch := range value {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}

func waitForPromptPaste(ctx context.Context, sessionName, paneBeforePaste string) (bool, error) {
	return waitForPromptPasteWithTimeout(ctx, sessionName, paneBeforePaste, 15*time.Second)
}

func waitForPromptPasteWithTimeout(ctx context.Context, sessionName, paneBeforePaste string, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	started := time.Now()
	baselineActive := hasClaudeActivity(paneBeforePaste)
	var sawPaste bool
	var lastCaptured string
	var stableSince time.Time

	for {
		select {
		case <-deadline.Done():
			captured, _ := captureTmuxPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return false, fmt.Errorf("timed out waiting for Claude Code prompt paste; %s", llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return false, fmt.Errorf("timed out waiting for Claude Code prompt paste")
		case <-ticker.C:
			captured, err := captureTmuxPane(deadline, sessionName)
			if err != nil {
				if isClaudeTmuxSessionLostError(err) {
					return false, err
				}
				continue
			}
			// If the pane already showed activity before paste, a changed screen can
			// just be the pasted draft appearing while the old activity is still
			// visible. Do not treat that as implicit submission; let the caller send
			// the explicit submit keys after paste stabilizes.
			if hasClaudeActivity(captured) && !baselineActive && (captured != paneBeforePaste || sawPaste) {
				return true, nil
			}
			if captured != paneBeforePaste || strings.Contains(captured, "[Pasted text") {
				sawPaste = true
			}
			if !sawPaste {
				if time.Since(started) >= promptPasteInvisibleGrace && hasReadyInputPrompt(captured) {
					return false, nil
				}
				continue
			}
			if captured != lastCaptured {
				lastCaptured = captured
				stableSince = time.Now()
				continue
			}
			if !stableSince.IsZero() && time.Since(stableSince) >= promptPasteVisibleStableWindow {
				return false, nil
			}
		}
	}
}

func waitForPromptAccepted(ctx context.Context, sessionName, preSubmitPane, prompt string) error {
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline.Done():
			captured, _ := captureTmuxPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out waiting for Claude Code prompt to start; %s", llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return fmt.Errorf("timed out waiting for Claude Code prompt to start")
		case <-ticker.C:
			captured, err := captureTmuxPane(deadline, sessionName)
			if err != nil {
				if isClaudeTmuxSessionLostError(err) {
					return err
				}
				continue
			}
			// A pasted prompt whose Enter did not actually submit stays visible
			// in the ❯ input box (as the draft text or a "[Pasted text …]" chip).
			// hasClaudeActivity alone false-positives when Claude was already
			// busy from earlier work — the screen still shows the old spinner —
			// so the submit loop would declare success while our text sits
			// unsent in the box. Require the draft to have cleared first, the
			// same check the live-steering path uses.
			draft, ok := latestClaudePromptDraft(captured)
			if !ok {
				// Missing ❯ line is a transient repaint, not proof of submission
				// (same false-success that left auto-notifications stuck as
				// unsubmitted drafts). Keep polling rather than falling through
				// to the hasClaudeActivity shortcut below.
				continue
			}
			if claudePromptDraftStillMatchesMessage(draft, prompt) {
				continue
			}
			if hasClaudeActivity(captured) {
				return nil
			}
			if hasReadyEmptyInputPrompt(captured) && hasNewAssistantOutput(capturedAfterPaneBaseline(captured, preSubmitPane)) {
				return nil
			}
		}
	}
}

func waitForClaudeLiveInputSubmitted(ctx context.Context, sessionName, message string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	var lastCaptured string
	for {
		select {
		case <-deadline.Done():
			captured := lastCaptured
			if strings.TrimSpace(captured) == "" {
				captured, _ = captureTmuxPane(context.Background(), sessionName)
			}
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out waiting for Claude Code live input draft to clear; %s", llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return fmt.Errorf("timed out waiting for Claude Code live input draft to clear")
		case <-ticker.C:
			captured, err := captureTmuxPane(deadline, sessionName)
			if err != nil {
				if isClaudeTmuxSessionLostError(err) {
					return err
				}
				continue
			}
			lastCaptured = captured
			draft, ok := latestClaudePromptDraft(captured)
			if !ok {
				// No ❯ input line in this frame. This is almost always a
				// transient TUI repaint mid-paste, NOT proof the message was
				// submitted: Claude Code keeps the ❯ input visible (empty) even
				// while a turn runs, so a genuine submit shows up as an
				// empty/changed draft on the ok branch below. Previously this
				// returned success whenever hasClaudeActivity was true, but that
				// activity is frequently just residual spinner output from
				// Claude's *prior* turn — so an unsubmitted draft (e.g. a pasted
				// multi-line AUTO-NOTIFICATION) got reported as sent, the next
				// paste stacked on top of it, and notifications piled up
				// unsubmitted in the input box. Treat a missing prompt line as
				// inconclusive and keep polling; the submit retry loop will press
				// Enter again until the draft positively clears.
				continue
			}
			if !claudePromptDraftStillMatchesMessage(draft, message) {
				return nil
			}
		}
	}
}

func latestClaudePromptDraft(captured string) (string, bool) {
	draft, _, ok := latestClaudePromptDraftRaw(captured)
	if !ok {
		return "", false
	}
	return draft, true
}

func claudePromptDraftStillMatchesMessage(draft, message string) bool {
	normalizedDraft := strings.ToLower(strings.Join(strings.Fields(draft), " "))
	normalizedMessage := strings.ToLower(strings.Join(strings.Fields(message), " "))
	if normalizedDraft == "" {
		return false
	}
	if strings.HasPrefix(normalizedDraft, "[pasted") {
		return true
	}
	if normalizedMessage == "" {
		return false
	}
	return strings.Contains(normalizedDraft, normalizedMessage) || strings.Contains(normalizedMessage, normalizedDraft)
}

func truncateClaudeDraftForError(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if max <= 0 || len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}

func hasNewAssistantOutput(delta string) bool {
	normalized := strings.ReplaceAll(delta, "\u00a0", " ")
	for _, line := range strings.Split(normalized, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "⏺ ") {
			return true
		}
	}
	return false
}

func waitForMarkedResponse(ctx context.Context, sessionName, startMarker, endMarker, paneBaseline string, streamChan chan<- llmtypes.StreamChunk) (string, error) {
	captured, err := waitForClaudeIdleAfterActivity(ctx, sessionName, false, paneBaseline, endMarker, streamChan)
	forcedComplete := errors.Is(err, tmuxcontrol.ErrForceComplete)
	if err != nil && !forcedComplete {
		if content, ok := parseClaudeResponseFromCaptured(captured, paneBaseline, startMarker, endMarker); ok {
			return content, nil
		}
		if isClaudeTmuxSessionLostError(err) {
			return "", fmt.Errorf("Claude Code tmux session ended before response could be captured: %w", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("timed out waiting for Claude Code response: %w", err)
		}
		return "", fmt.Errorf("Claude Code response wait failed: %w", err)
	}

	if content, ok := parseClaudeResponseFromCaptured(captured, paneBaseline, startMarker, endMarker); ok {
		return content, nil
	}
	if forcedComplete {
		if content := forcedClaudeResponseFromCaptured(captured, paneBaseline); strings.TrimSpace(content) != "" {
			return content, nil
		}
	}
	return "", fmt.Errorf("Claude Code tmux session returned to idle without a parseable response; %s", llmtypes.CompactTerminalPaneForError(sessionName, captured))
}

func parseClaudeResponseFromCaptured(captured, paneBaseline, startMarker, endMarker string) (string, bool) {
	newOutput := capturedAfterPaneBaseline(captured, paneBaseline)
	if content, ok := extractBetweenLastMarkers(newOutput, startMarker, endMarker); ok {
		return strings.TrimSpace(content), true
	}
	if !hasTrailingClaudeTUIStatus(newOutput) {
		if content, ok := extractLatestUnmarkedAssistantResponse(newOutput); ok {
			if markedContent, markedOK := extractBetweenLastMarkers(content, startMarker, endMarker); markedOK {
				return strings.TrimSpace(markedContent), true
			}
			return strings.TrimSpace(content), true
		}
		if content, ok := extractTrailingUnmarkedAssistantResponse(newOutput); ok {
			return strings.TrimSpace(content), true
		}
	}
	if content, ok := extractTailAssistantTextFallback(newOutput, claudeTailFallbackMaxLines); ok {
		return strings.TrimSpace(content), true
	}
	return "", false
}

func forcedClaudeResponseFromCaptured(captured, paneBaseline string) string {
	newOutput := capturedAfterPaneBaseline(captured, paneBaseline)
	if content, ok := extractLatestUnmarkedAssistantResponse(newOutput); ok {
		return strings.TrimSpace(content)
	}
	if content, ok := extractTrailingUnmarkedAssistantResponse(newOutput); ok {
		return strings.TrimSpace(content)
	}
	return strings.TrimSpace(newOutput)
}

func waitForClaudeIdleAfterActivity(ctx context.Context, sessionName string, activityAlreadySeen bool, paneBaseline, endMarker string, streamChan chan<- llmtypes.StreamChunk) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	sawActivity := activityAlreadySeen
	var idleSince time.Time
	var lastCaptured string
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	streamTerminalScreen := claudeInteractiveStreamTmuxScreenEnabled()
	stalePaneBackstop := claudeInteractiveStalePaneBackstop()
	// Stale-pane backstop tracking: the raw capture from the previous tick and
	// the time it last changed. Tracked at the top of every tick, independent of
	// all the branch logic below, so a ready-prompt detection bug that keeps the
	// loop in a "not ready" branch can never suppress it.
	var backstopPrevCapture string
	var paneUnchangedSince time.Time

	for {
		select {
		case <-ctx.Done():
			captured, _ := captureTmuxPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return captured, fmt.Errorf("%w; %s", ctx.Err(), llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return "", ctx.Err()
		case <-ticker.C:
			captured, err := captureTmuxPane(ctx, sessionName)
			if err != nil {
				if ctx.Err() != nil {
					latest, _ := captureTmuxPane(context.Background(), sessionName)
					if strings.TrimSpace(latest) != "" {
						return latest, fmt.Errorf("%w; %s", ctx.Err(), llmtypes.CompactTerminalPaneForError(sessionName, latest))
					}
					if strings.TrimSpace(lastCaptured) != "" {
						return lastCaptured, ctx.Err()
					}
				}
				if strings.TrimSpace(lastCaptured) != "" {
					return lastCaptured, err
				}
				return "", err
			}
			delta := capturedAfterPaneBaseline(captured, paneBaseline)
			if errText := detectTmuxFatalStatus(captured); errText != "" {
				return "", fmt.Errorf("claude code tmux session failed: %s", errText)
			}
			if tmuxcontrol.ConsumeForceComplete(sessionName) {
				return captured, tmuxcontrol.ErrForceComplete
			}
			// Stale-pane backstop. Independent of hasReadyEmptyInputPrompt and
			// every branch below: if the pane has produced activity and then
			// frozen (byte-identical) for longer than the backstop, the turn is
			// over but completion detection failed to recognize it (e.g. a
			// leftover spinner frame or status line holding the pane "not
			// ready"). Extract whatever response is present and return it rather
			// than spin forever (the call context has no turn deadline by
			// default). sawActivity guards the no-input case where the pane never
			// changed from baseline.
			if captured != backstopPrevCapture {
				backstopPrevCapture = captured
				paneUnchangedSince = time.Now()
			} else if sawActivity && stalePaneBackstop > 0 && !paneUnchangedSince.IsZero() &&
				time.Since(paneUnchangedSince) >= stalePaneBackstop {
				content, ok := parseClaudeResponseFromCaptured(captured, paneBaseline, "", endMarker)
				if !ok || strings.TrimSpace(content) == "" {
					content = forcedClaudeResponseFromCaptured(captured, paneBaseline)
				}
				if strings.TrimSpace(content) != "" {
					return captured, nil
				}
				return captured, fmt.Errorf("Claude Code CLI pane went unchanged for %s after activity but no ready prompt or visible assistant output was detected; latest pane:\n%s", stalePaneBackstop, captured)
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamClaudeTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if hasClaudeActivity(captured) {
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if hasNewAssistantOutput(delta) || (endMarker != "" && strings.Contains(delta, endMarker)) {
				sawActivity = true
			}
			if !sawActivity || !hasReadyEmptyInputPrompt(captured) {
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
			if time.Since(idleSince) >= claudeIdleStableWindow {
				return captured, nil
			}
		}
	}
}

func streamClaudeTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	streamClaudeStatusLine(ctx, sessionName, streamChan)
	snapshot, err := captureTmuxPaneForDisplay(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripClaudeANSIPreserveColors(snapshot), "\n")
	if strings.TrimSpace(snapshot) == "" || snapshot == *lastTerminalSnapshot {
		return false
	}
	select {
	case streamChan <- llmtypes.StreamChunk{
		Type:    llmtypes.StreamChunkTypeTerminal,
		Content: snapshot,
		Metadata: map[string]interface{}{
			"tmux_session":                    sessionName,
			"claude_code_interactive_session": sessionName,
		},
	}:
		*lastTerminalSnapshot = snapshot
		return true
	default:
		return false
	}
}

func capturedAfterPaneBaseline(captured, baseline string) string {
	if baseline == "" {
		return captured
	}
	if idx := strings.LastIndex(captured, baseline); idx >= 0 {
		return captured[idx+len(baseline):]
	}

	normalizedCaptured := strings.ReplaceAll(captured, "\u00a0", " ")
	normalizedBaseline := strings.ReplaceAll(baseline, "\u00a0", " ")
	if idx := strings.LastIndex(normalizedCaptured, normalizedBaseline); idx >= 0 {
		return normalizedCaptured[idx+len(normalizedBaseline):]
	}
	return lineDeltaAfterBaseline(normalizedCaptured, normalizedBaseline)
}

func lineDeltaAfterBaseline(captured, baseline string) string {
	capturedLines := strings.Split(captured, "\n")
	baselineLines := strings.Split(baseline, "\n")
	maxOverlap := len(capturedLines)
	if len(baselineLines) < maxOverlap {
		maxOverlap = len(baselineLines)
	}
	for overlap := maxOverlap; overlap > 0; overlap-- {
		if equalStringSlices(baselineLines[len(baselineLines)-overlap:], capturedLines[:overlap]) {
			return strings.Join(capturedLines[overlap:], "\n")
		}
	}
	return captured
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func detectTmuxFatalStatus(captured string) string {
	switch {
	case strings.Contains(captured, "You've hit your limit"):
		return "rate limit reached"
	case strings.Contains(captured, "Not logged in"):
		return "not logged in"
	case strings.Contains(captured, "Pane is dead"):
		return "claude code process exited"
	default:
		return ""
	}
}

func isClaudeNonTextAssistantBlock(content string) bool {
	firstLine := content
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = firstLine[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return true
	}
	return isClaudeToolProgressLine(firstLine) ||
		isClaudeFatalProgressLine(firstLine) ||
		isClaudeRunningProgressLine(firstLine)
}

func isClaudeRunningProgressLine(trimmed string) bool {
	if isClaudeCompletedWorkStatusLine(trimmed) {
		return false
	}
	return strings.Contains(trimmed, "esc to interrupt") ||
		strings.HasPrefix(trimmed, "· ") ||
		strings.HasPrefix(trimmed, "✶ ") ||
		strings.HasPrefix(trimmed, "✳ ") ||
		strings.HasPrefix(trimmed, "✢ ") ||
		strings.HasPrefix(trimmed, "✽ ") ||
		strings.HasPrefix(trimmed, "✻ ")
}

func isClaudeCompletedWorkStatusLine(trimmed string) bool {
	trimmed = strings.TrimSpace(trimmed)
	if !(strings.HasPrefix(trimmed, "· ") ||
		strings.HasPrefix(trimmed, "✶ ") ||
		strings.HasPrefix(trimmed, "✳ ") ||
		strings.HasPrefix(trimmed, "✢ ") ||
		strings.HasPrefix(trimmed, "✽ ") ||
		strings.HasPrefix(trimmed, "✻ ")) {
		return false
	}
	return strings.Contains(trimmed, " for ") &&
		!strings.Contains(trimmed, "…") &&
		!strings.Contains(strings.ToLower(trimmed), "esc to interrupt")
}

func isClaudeToolProgressLine(trimmed string) bool {
	return strings.Contains(trimmed, "Calling ") ||
		strings.Contains(trimmed, "Called ") ||
		(strings.Contains(trimmed, " - ") && strings.Contains(trimmed, " (MCP)"))
}

func isClaudeFatalProgressLine(trimmed string) bool {
	return strings.Contains(trimmed, "You've hit your limit") ||
		strings.Contains(trimmed, "Not logged in") ||
		strings.Contains(trimmed, "Pane is dead")
}

func isClaudePromptEchoLine(trimmed string) bool {
	return strings.Contains(trimmed, "Final answer format:") ||
		strings.Contains(trimmed, "Start marker:") ||
		strings.Contains(trimmed, "End marker:") ||
		strings.Contains(trimmed, "Conversation:") ||
		strings.HasPrefix(trimmed, "HUMAN:") ||
		strings.HasPrefix(trimmed, "AI:") ||
		strings.HasPrefix(trimmed, "SYSTEM:")
}

func cleanClaudeTerminalProgressLine(trimmed string) string {
	trimmed = strings.TrimPrefix(trimmed, "⏺ ")
	trimmed = strings.TrimPrefix(trimmed, "● ")
	trimmed = strings.TrimSpace(trimmed)
	if isClaudeCompletedWorkStatusLine(trimmed) ||
		strings.HasPrefix(trimmed, "· ") ||
		strings.HasPrefix(trimmed, "⎿ Tip:") ||
		isClaudeTUIStatusLine(trimmed) ||
		strings.HasPrefix(trimmed, "⏵") ||
		strings.Contains(trimmed, "/effort") ||
		strings.Contains(trimmed, "don't ask on") {
		return ""
	}
	if isClaudeToolProgressLine(trimmed) {
		return normalizeClaudeToolProgressLine(trimmed)
	}
	return trimmed
}

func normalizeClaudeToolProgressLine(line string) string {
	line = strings.TrimSpace(line)
	if idx := strings.Index(line, " (ctrl+o"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	line = strings.TrimRight(line, ".… ")

	fields := strings.Fields(line)
	if len(fields) < 2 {
		return line
	}

	switch fields[0] {
	case "Calling":
		return "Calling " + fields[1] + "..."
	case "Called":
		if len(fields) >= 4 && fields[2] != "1" && strings.HasPrefix(fields[3], "time") {
			return "Called " + fields[1] + " " + fields[2] + " times"
		}
		return "Called " + fields[1]
	default:
		return line
	}
}

func captureTmuxPane(ctx context.Context, sessionName string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", sessionName, "-p", "-J", "-S", "-"+defaultTmuxCaptureLines)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to capture Claude Code tmux session: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// stripClaudeANSIPreserveColors strips ANSI cursor positioning / clear-screen
// sequences but preserves SGR (Select Graphic Rendition: color, bold, dim,
// underline, etc., terminated with `m`). The frontend feeds this output
// through ansi_up to colorize the rendered pane snapshot. Cursor positioning
// is dropped because ansi_up does not emulate VT100 movement.
func stripClaudeANSIPreserveColors(s string) string {
	return paneview.StripANSIPreserveColors(s)
}

func captureTmuxPaneForDisplay(ctx context.Context, sessionName string) (string, error) {
	var out bytes.Buffer
	// -e preserves ANSI SGR (color, bold, dim, etc.) so the frontend can
	// colorize the snapshot via ansi_up. Cursor positioning sequences are
	// stripped by stripClaudeANSIPreserveColors in streamClaudeTerminalSnapshot
	// before the snapshot leaves the adapter so they don't garble rendering.
	// -J joins wrapped lines so the frontend can handle wrapping natively without
	// hard splitting words mid-line. Display snapshots intentionally capture only
	// the visible pane: deep scrollback flattens prior spinner/redraw frames into
	// duplicate rows before the app can render the current screen.
	cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", sessionName, "-p", "-e", "-J")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to capture Claude Code display session: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

func extractBetweenLastMarkers(text, startMarker, endMarker string) (string, bool) {
	if startMarker == "" || endMarker == "" {
		return "", false
	}
	end := strings.LastIndex(text, endMarker)
	if end < 0 {
		return "", false
	}

	start := strings.LastIndex(text[:end], startMarker)
	if start < 0 {
		return "", false
	}
	start += len(startMarker)

	content := text[start:end]
	content = normalizeCapturedAssistantText(content)
	if content == "" || isClaudeTUIArtifact(content) {
		return "", false
	}
	return content, true
}

func isClaudeTUIArtifact(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "<final answer>" {
		return true
	}
	if isClaudeLikelyQueuedUserEcho(trimmed) {
		return true
	}
	if isClaudeTUIStatusLine(trimmed) {
		return true
	}
	if strings.Contains(trimmed, "<final answer>") &&
		(strings.Contains(trimmed, "✶ Composing") || strings.Contains(trimmed, "❯")) {
		return true
	}
	return false
}

func isClaudeLikelyQueuedUserEcho(text string) bool {
	lines := nonEmptyClaudeLines(text)
	if len(lines) == 0 {
		return false
	}
	lower := strings.ToLower(strings.Join(lines, "\n"))
	return strings.Contains(lower, "pre-validation failed") &&
		(strings.Contains(lower, "checks:") ||
			strings.Contains(lower, "fix the specific issue") ||
			strings.Contains(lower, "validation failed") ||
			strings.Contains(lower, "must exist")) ||
		strings.Contains(lower, "fix the specific issue") &&
			strings.Contains(lower, "re-produce the required outputs")
}

func nonEmptyClaudeLines(text string) []string {
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

func isClaudeTUIStatusLine(trimmed string) bool {
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimPrefix(trimmed, "⎿")
	trimmed = strings.TrimSpace(trimmed)
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "compacted") &&
		(strings.Contains(lower, "ctrl+o") || strings.Contains(lower, "full summary") || strings.Contains(lower, "history"))
}

// isClaudeCompactionInProgress reports whether the pane is actively compacting
// (or otherwise summarizing) the conversation — the long-running resume step
// that must NOT count against waitForTmuxPrompt's inactivity window. Matches
// the in-progress wording ("Compacting…" / "Summarizing…") but not the
// finished "Compacted" status line handled by isClaudeTUIStatusLine.
func isClaudeCompactionInProgress(captured string) bool {
	lower := strings.ToLower(strings.ReplaceAll(captured, " ", " "))
	return strings.Contains(lower, "compacting") || strings.Contains(lower, "summarizing")
}

// claudeCompactionBlocksInput reports whether compaction is currently REPLACING
// the input box — the only state in which a send must wait. isClaudeCompactionInProgress
// is a loose substring match that also fires on the words "compacting"/"summarizing"
// appearing in scrollback or the workflow's own conversation content; gating it on
// "no ready ❯ prompt" means a pane that is actually accepting input is never treated
// as compacting, which prevents a spurious multi-minute stall on every such send.
func claudeCompactionBlocksInput(captured string) bool {
	return isClaudeCompactionInProgress(captured) && !hasReadyInputPrompt(captured)
}

func hasTrailingClaudeTUIStatus(text string) bool {
	lines := strings.Split(strings.ReplaceAll(text, "\u00a0", " "), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if isClaudeTUIStatusLine(trimmed) {
			return true
		}
		if isClaudeTUIBoundaryLine(trimmed) {
			continue
		}
		return false
	}
	return false
}

func extractLatestUnmarkedAssistantResponse(text string) (string, bool) {
	normalized := strings.ReplaceAll(text, "\u00a0", " ")
	lines := strings.Split(normalized, "\n")

	for start := len(lines) - 1; start >= 0; start-- {
		if !strings.HasPrefix(strings.TrimSpace(lines[start]), "⏺ ") {
			continue
		}

		responseLines := make([]string, 0, len(lines)-start)
		for _, line := range lines[start:] {
			trimmed := strings.TrimSpace(line)
			if len(responseLines) > 0 && isClaudeTUIBoundaryLine(trimmed) {
				break
			}
			responseLines = append(responseLines, line)
		}

		content := normalizeCapturedAssistantText(strings.Join(responseLines, "\n"))
		if content == "" || isClaudeTUIArtifact(content) || isClaudeNonTextAssistantBlock(content) {
			continue
		}
		return content, true
	}
	return "", false
}

func extractTrailingUnmarkedAssistantResponse(text string) (string, bool) {
	if hasTrailingClaudeTUIStatus(text) {
		return "", false
	}
	normalized := strings.ReplaceAll(text, "\u00a0", " ")
	lines := strings.Split(normalized, "\n")

	end := len(lines)
	for end > 0 {
		trimmed := strings.TrimSpace(lines[end-1])
		if trimmed == "" ||
			isClaudeTUIBoundaryLine(trimmed) ||
			isClaudePromptEchoLine(trimmed) ||
			strings.Contains(trimmed, "tmux focus-events") ||
			strings.Contains(trimmed, "set -g focus-events") {
			end--
			continue
		}
		break
	}
	if end <= 0 {
		return "", false
	}

	start := 0
	for i := end - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "⏺ ") {
			start = i
			break
		}
		if isClaudeTUIBoundaryLine(trimmed) ||
			isClaudePromptEchoLine(trimmed) ||
			isClaudeToolProgressLine(cleanClaudeTerminalProgressLine(trimmed)) ||
			isClaudeRunningProgressLine(cleanClaudeTerminalProgressLine(trimmed)) {
			start = i + 1
			break
		}
	}

	cleaned := make([]string, 0, end-start)
	for _, line := range lines[start:end] {
		trimmed := strings.TrimSpace(line)
		cleanedProgress := cleanClaudeTerminalProgressLine(trimmed)
		if trimmed == "" {
			cleaned = append(cleaned, line)
			continue
		}
		if isClaudeTUIBoundaryLine(trimmed) ||
			isClaudePromptEchoLine(trimmed) ||
			cleanedProgress == "" ||
			isClaudeToolProgressLine(cleanedProgress) ||
			isClaudeFatalProgressLine(cleanedProgress) ||
			isClaudeRunningProgressLine(cleanedProgress) ||
			strings.Contains(trimmed, "tmux focus-events") ||
			strings.Contains(trimmed, "set -g focus-events") {
			continue
		}
		cleaned = append(cleaned, line)
	}

	content := normalizeCapturedAssistantText(strings.Join(cleaned, "\n"))
	if content == "" || isClaudeTUIArtifact(content) || isClaudeNonTextAssistantBlock(content) {
		return "", false
	}
	return content, true
}

func extractTailAssistantTextFallback(text string, maxLines int) (string, bool) {
	if maxLines <= 0 {
		maxLines = 24
	}
	normalized := strings.ReplaceAll(text, "\u00a0", " ")
	lines := strings.Split(normalized, "\n")
	tail := make([]string, 0, maxLines)
	for i := len(lines) - 1; i >= 0 && len(tail) < maxLines; i-- {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		cleanedProgress := cleanClaudeTerminalProgressLine(trimmed)
		if trimmed == "" ||
			isClaudeTUIBoundaryLine(trimmed) ||
			isClaudePromptEchoLine(trimmed) ||
			cleanedProgress == "" ||
			isClaudeToolProgressLine(cleanedProgress) ||
			isClaudeFatalProgressLine(cleanedProgress) ||
			isClaudeRunningProgressLine(cleanedProgress) ||
			strings.Contains(trimmed, "tmux focus-events") ||
			strings.Contains(trimmed, "set -g focus-events") {
			continue
		}
		tail = append(tail, line)
	}
	if len(tail) == 0 {
		return "", false
	}
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	content := normalizeCapturedAssistantText(strings.Join(tail, "\n"))
	if content == "" || isClaudeTUIArtifact(content) || isClaudeNonTextAssistantBlock(content) {
		return "", false
	}
	return content, true
}

func isClaudeTUIBoundaryLine(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "✻ ") ||
		strings.HasPrefix(trimmed, "✽ ") ||
		strings.HasPrefix(trimmed, "✶ ") ||
		strings.HasPrefix(trimmed, "✳ ") ||
		strings.HasPrefix(trimmed, "✢ ") ||
		strings.HasPrefix(trimmed, "╭") ||
		strings.HasPrefix(trimmed, "╰") ||
		strings.HasPrefix(trimmed, "│") ||
		strings.HasPrefix(trimmed, "────────────────") ||
		isClaudeTUIStatusLine(trimmed) ||
		strings.HasPrefix(trimmed, "❯") ||
		strings.HasPrefix(trimmed, "⏵")
}

func normalizeCapturedAssistantText(content string) string {
	lines := strings.Split(content, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimPrefix(line, "⏺ ")
		line = strings.TrimPrefix(line, "  ")
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func truncateClaudePaneForError(captured string) string {
	const maxRunes = 4000
	captured = strings.TrimSpace(captured)
	if captured == "" {
		return captured
	}
	runes := []rune(captured)
	if len(runes) <= maxRunes {
		return captured
	}
	return fmt.Sprintf("[truncated to last %d of %d chars]\n%s", maxRunes, len(runes), string(runes[len(runes)-maxRunes:]))
}

func tmuxTimeout() time.Duration {
	seconds, ok := claudeDurationSecondsFromEnv(EnvClaudeTmuxTimeoutSeconds, EnvClaudeExperimentalTimeoutSeconds)
	if !ok || seconds < 0 {
		return defaultTmuxTimeout
	}
	return time.Duration(seconds) * time.Second
}

func promptReadyTimeout() time.Duration {
	if parsed, ok := claudePositiveDurationFromEnv(EnvClaudeTmuxPromptWaitSeconds); ok {
		return parsed
	}
	return tmuxlaunch.PromptWait(EnvClaudeExperimentalPromptWaitSeconds)
}

// promptReadyMaxWait is the absolute ceiling waitForTmuxPrompt will wait for a
// ready input prompt while Claude keeps reporting activity (e.g. a long
// compaction on resume). The inactivity window (promptReadyTimeout) handles a
// hung pane; this only bounds a pane that looks busy forever. Defaults to a
// generous multiple of the inactivity window so real compactions complete.
func promptReadyMaxWait(idleWait time.Duration) time.Duration {
	if parsed, ok := claudePositiveDurationFromEnv(EnvClaudeTmuxPromptMaxWaitSeconds); ok {
		return parsed
	}
	maxWait := idleWait * 8
	if maxWait < 15*time.Minute {
		maxWait = 15 * time.Minute
	}
	return maxWait
}

func persistentInteractiveIdleTimeout() time.Duration {
	seconds, ok := claudeDurationSecondsFromEnv(EnvClaudeTmuxIdleTimeoutSeconds, EnvClaudeExperimentalIdleTimeoutSeconds)
	if !ok || seconds <= 0 {
		return defaultPersistentIdleTimeout
	}
	return time.Duration(seconds) * time.Second
}

// claudeInteractiveStalePaneBackstop returns the stale-pane backstop duration
// for the assistant-response loop. An explicit 0 (or negative) disables it.
func claudeInteractiveStalePaneBackstop() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(EnvClaudeInteractiveStalePaneBackstopSeconds)); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil {
			if seconds <= 0 {
				return 0
			}
			return time.Duration(seconds) * time.Second
		}
	}
	return defaultClaudeInteractiveStalePaneBackstop
}

func claudePositiveDurationFromEnv(keys ...string) (time.Duration, bool) {
	seconds, ok := claudeDurationSecondsFromEnv(keys...)
	if !ok || seconds <= 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func claudeDurationSecondsFromEnv(keys ...string) (int, bool) {
	for _, key := range keys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		seconds, err := strconv.Atoi(raw)
		if err != nil {
			return 0, false
		}
		return seconds, true
	}
	return 0, false
}

func boundedRetentionTimeout() time.Duration {
	return tmuxlaunch.Retention(defaultBoundedRetention)
}

func ensureTmuxAvailable(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not found in PATH; claude-code requires tmux %d.x or newer for interactive mode", minTmuxMajorVersion)
	}

	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(versionCtx, "tmux", "-V").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check tmux version: %w: %s", err, strings.TrimSpace(string(out)))
	}

	major, ok := parseTmuxMajorVersion(string(out))
	if !ok {
		return fmt.Errorf("failed to parse tmux version from %q; claude-code requires tmux %d.x or newer", strings.TrimSpace(string(out)), minTmuxMajorVersion)
	}
	if major < minTmuxMajorVersion {
		return fmt.Errorf("tmux %s is too old; claude-code requires tmux %d.x or newer", strings.TrimSpace(string(out)), minTmuxMajorVersion)
	}
	return nil
}

func parseTmuxMajorVersion(version string) (int, bool) {
	fields := strings.Fields(strings.TrimSpace(version))
	if len(fields) < 2 || fields[0] != "tmux" {
		return 0, false
	}

	raw := fields[1]
	digits := strings.Builder{}
	for _, r := range raw {
		if r < '0' || r > '9' {
			break
		}
		digits.WriteRune(r)
	}
	if digits.Len() == 0 {
		return 0, false
	}

	major, err := strconv.Atoi(digits.String())
	if err != nil {
		return 0, false
	}
	return major, true
}

// CleanupClaudeCodeTmuxSessions cleans up registered Claude Code tmux sessions.
func CleanupClaudeCodeTmuxSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}

	sessions := activeClaudeInteractiveSessions()
	persistentSessions := drainClaudePersistentInteractiveSessions()
	for _, session := range persistentSessions {
		sessions = appendUniqueStrings(sessions, session.tmuxSessionName)
		unregisterClaudeInteractiveOwner(session.ownerSessionID, session.tmuxSessionName)
		removeFiles(session.tempFiles)
	}

	var failures []string
	for _, sessionName := range sessions {
		if err := killClaudeInteractiveSession(ctx, sessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up Claude Code tmux sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func activeClaudeInteractiveSessions() []string {
	claudeInteractiveSessionRegistry.Lock()
	defer claudeInteractiveSessionRegistry.Unlock()

	sessions := make([]string, 0, len(claudeInteractiveSessionRegistry.sessions))
	for sessionName := range claudeInteractiveSessionRegistry.sessions {
		sessions = append(sessions, sessionName)
	}
	return sessions
}

func appendUniqueStrings(base []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, value := range append(base, extra...) {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func claudeInteractiveSessionPrefix() string {
	prefix := strings.TrimSpace(os.Getenv(EnvClaudeTmuxSessionPrefix))
	if prefix == "" {
		prefix = strings.TrimSpace(os.Getenv(EnvClaudeExperimentalSessionPrefix))
	}
	if prefix == "" {
		prefix = "mlp-claude-code"
	}
	return sanitizeTmuxSessionName(prefix)
}

func newTmuxSessionName() string {
	prefix := claudeInteractiveSessionPrefix()
	return sanitizeTmuxSessionName(fmt.Sprintf("%s-%d-%s", prefix, time.Now().UnixNano(), randomHex(4)))
}

func registerClaudeInteractiveSession(sessionName string) {
	claudeInteractiveSessionRegistry.Lock()
	defer claudeInteractiveSessionRegistry.Unlock()
	claudeInteractiveSessionRegistry.sessions[sessionName] = struct{}{}
}

func unregisterClaudeInteractiveSession(sessionName string) {
	claudeInteractiveSessionRegistry.Lock()
	defer claudeInteractiveSessionRegistry.Unlock()
	delete(claudeInteractiveSessionRegistry.sessions, sessionName)
}

func registerClaudeInteractiveOwner(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if ownerSessionID == "" || tmuxSessionName == "" {
		return
	}
	claudeInteractiveOwnerRegistry.Lock()
	defer claudeInteractiveOwnerRegistry.Unlock()
	claudeInteractiveOwnerRegistry.sessions[ownerSessionID] = tmuxSessionName
}

func unregisterClaudeInteractiveOwner(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return
	}
	claudeInteractiveOwnerRegistry.Lock()
	defer claudeInteractiveOwnerRegistry.Unlock()
	if current := claudeInteractiveOwnerRegistry.sessions[ownerSessionID]; current == tmuxSessionName {
		delete(claudeInteractiveOwnerRegistry.sessions, ownerSessionID)
	}
}

func activeClaudeInteractiveOwner(ownerSessionID string) (string, bool) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return "", false
	}
	claudeInteractiveOwnerRegistry.RLock()
	defer claudeInteractiveOwnerRegistry.RUnlock()
	sessionName, ok := claudeInteractiveOwnerRegistry.sessions[ownerSessionID]
	return sessionName, ok && strings.TrimSpace(sessionName) != ""
}

func claudeInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyInteractiveSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func claudePersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyPersistentInteractive].(bool)
	return ok && enabled
}

func claudeWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if dir, ok := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); ok {
		return strings.TrimSpace(dir)
	}
	return ""
}

// acquirePersistentInteractiveSession returns with session.mu held. The caller
// must releaseClaudePersistentInteractiveSession on normal completion, or mark,
// unlock, and clean up the session on a ready-prompt failure.
func (c *ClaudeCodeInteractiveAdapter) acquirePersistentInteractiveSession(ctx context.Context, ownerSessionID, nativeSessionID string, opts *llmtypes.CallOptions, systemPrompt string, workingDir string) (*claudeInteractivePersistentSession, error) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return nil, fmt.Errorf("persistent Claude Code tmux session requires an owner session ID")
	}

	claudeInteractivePersistentRegistry.Lock()
	existing := claudeInteractivePersistentRegistry.sessions[ownerSessionID]
	if existing != nil {
		// Release the registry (map) lock BEFORE taking the per-session lock.
		// session.mu is held for a whole turn; holding the global map lock
		// across it stalls every other acquire behind a busy session
		// (lock-held-across-blocking-call deadlock).
		claudeInteractivePersistentRegistry.Unlock()
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
		return existing, nil
	}

	now := time.Now()
	sessionName := newTmuxSessionName()
	session := &claudeInteractivePersistentSession{
		ownerSessionID:  ownerSessionID,
		tmuxSessionName: sessionName,
		nativeSessionID: nativeSessionID,
		workingDir:      strings.TrimSpace(workingDir),
		createdAt:       now,
		lastUsed:        now,
	}
	session.mu.Lock()
	claudeInteractivePersistentRegistry.sessions[ownerSessionID] = session
	claudeInteractivePersistentRegistry.Unlock()

	args, tempFiles, err := c.buildClaudeArgs(opts, sessionName, nativeSessionID, systemPrompt)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		removeClaudePersistentInteractiveSession(ownerSessionID, session)
		return nil, err
	}
	session.tempFiles = tempFiles

	if err := c.startSession(ctx, sessionName, args, workingDir); err != nil {
		session.initErr = err
		session.mu.Unlock()
		removeClaudePersistentInteractiveSession(ownerSessionID, session)
		removeFiles(tempFiles)
		return nil, err
	}
	registerClaudeInteractiveSession(sessionName)
	registerClaudeInteractiveOwner(ownerSessionID, sessionName)
	return session, nil
}

func releaseClaudePersistentInteractiveSession(session *claudeInteractivePersistentSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	session.lastUsed = time.Now()
	idleTimeout := persistentInteractiveIdleTimeout()
	session.idleTimer = time.AfterFunc(idleTimeout, func() {
		closeClaudePersistentInteractiveSession(session.ownerSessionID, "idle timeout", logger)
	})
	session.mu.Unlock()
}

// CloseClaudeCodeInteractiveSessionForOwner closes the persistent
// Claude Code interactive session for the given owner. See agycli's
// equivalent CloseAgyCLIInteractiveSessionForOwner for the
// mid-chat-prompt-change motivation.
func CloseClaudeCodeInteractiveSessionForOwner(ownerSessionID, reason string) {
	closeClaudePersistentInteractiveSession(ownerSessionID, reason, nil)
}

// CloseClaudeCodeInteractiveSessionByTmux closes the persistent Claude Code
// interactive session whose backing tmux session matches tmuxSessionName,
// regardless of the owner key it was registered under. Teardown backstop for
// when the owning session ID is unknown or has drifted. Delegates to the
// owner-keyed close so the same graceful exit + cleanup runs. No-op when no
// live session matches.
func CloseClaudeCodeInteractiveSessionByTmux(tmuxSessionName, reason string) {
	name := strings.TrimSpace(tmuxSessionName)
	if name == "" {
		return
	}
	claudeInteractivePersistentRegistry.Lock()
	owner := ""
	for o, s := range claudeInteractivePersistentRegistry.sessions {
		if s != nil && s.tmuxSessionName == name {
			owner = o
			break
		}
	}
	claudeInteractivePersistentRegistry.Unlock()
	if owner == "" {
		return
	}
	closeClaudePersistentInteractiveSession(owner, reason, nil)
}

func closeClaudePersistentInteractiveSession(ownerSessionID, reason string, logger interfaces.Logger) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return
	}

	claudeInteractivePersistentRegistry.Lock()
	session := claudeInteractivePersistentRegistry.sessions[ownerSessionID]
	if session == nil {
		claudeInteractivePersistentRegistry.Unlock()
		return
	}
	delete(claudeInteractivePersistentRegistry.sessions, ownerSessionID)
	claudeInteractivePersistentRegistry.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Closing persistent Claude Code tmux session %s for owner %s: %s", session.tmuxSessionName, ownerSessionID, reason)
	}
	_ = closeClaudeSessionForResume(session.tmuxSessionName, logger)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killClaudeInteractiveSession(cleanupCtx, session.tmuxSessionName)
	unregisterClaudeInteractiveOwner(ownerSessionID, session.tmuxSessionName)
	unregisterClaudeInteractiveSession(session.tmuxSessionName)
	removeFiles(session.tempFiles)
}

func markClaudePersistentInteractiveSessionFailedLocked(session *claudeInteractivePersistentSession, err error, logger interfaces.Logger) {
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
		logger.Debugf("Discarding persistent Claude Code tmux session %s for owner %s: %v", session.tmuxSessionName, session.ownerSessionID, err)
	}
}

func cleanupFailedClaudePersistentInteractiveSession(session *claudeInteractivePersistentSession) {
	if session == nil {
		return
	}
	removeClaudePersistentInteractiveSession(session.ownerSessionID, session)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killClaudeInteractiveSession(cleanupCtx, session.tmuxSessionName)
	unregisterClaudeInteractiveOwner(session.ownerSessionID, session.tmuxSessionName)
	unregisterClaudeInteractiveSession(session.tmuxSessionName)
	removeFiles(session.tempFiles)
}

func removeClaudePersistentInteractiveSession(ownerSessionID string, session *claudeInteractivePersistentSession) {
	claudeInteractivePersistentRegistry.Lock()
	defer claudeInteractivePersistentRegistry.Unlock()
	if current := claudeInteractivePersistentRegistry.sessions[ownerSessionID]; current == session {
		delete(claudeInteractivePersistentRegistry.sessions, ownerSessionID)
	}
}

func drainClaudePersistentInteractiveSessions() []*claudeInteractivePersistentSession {
	claudeInteractivePersistentRegistry.Lock()
	sessions := make([]*claudeInteractivePersistentSession, 0, len(claudeInteractivePersistentRegistry.sessions))
	for _, session := range claudeInteractivePersistentRegistry.sessions {
		sessions = append(sessions, session)
	}
	claudeInteractivePersistentRegistry.sessions = map[string]*claudeInteractivePersistentSession{}
	claudeInteractivePersistentRegistry.Unlock()

	for _, session := range sessions {
		stopClaudeIdleTimerIfAvailable(session)
	}
	return sessions
}

func stopClaudeIdleTimerIfAvailable(session *claudeInteractivePersistentSession) {
	if session == nil || !session.mu.TryLock() {
		return
	}
	defer session.mu.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
}

// SendClaudeCodeInput routes live input into a registered Claude Code tmux session.
func SendClaudeCodeInput(ctx context.Context, ownerSessionID, message string) error {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return fmt.Errorf("Claude Code owner session ID is required")
	}
	sessionName, ok := activeClaudeInteractiveOwner(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Claude Code tmux session registered for owner session %s", ownerSessionID)
	}
	return sendInputToActiveTmux(ctx, sessionName, message)
}

func cleanupClaudeInteractiveSessionAfter(sessionName string, retention time.Duration) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if retention <= 0 {
				closeClaudeInteractiveSessionNow(sessionName)
				return
			}
			time.AfterFunc(retention, func() {
				closeClaudeInteractiveSessionNow(sessionName)
			})
		})
	}
}

func closeClaudeInteractiveSessionNow(sessionName string) {
	defer unregisterClaudeInteractiveSession(sessionName)
	killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killClaudeInteractiveSession(killCtx, sessionName)
}

func killClaudeInteractiveSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return nil
	}
	// Reap the pane process trees (CLI + spawned MCP node subprocesses) before
	// killing the session — kill-session only SIGHUPs the pane process, so the
	// children would otherwise orphan and leak.
	tmuxcontrol.ReapSessionProcessTree(ctx, sessionName)
	if err := runCommand(ctx, nil, "tmux", "kill-session", "-t", sessionName); err != nil {
		if isTmuxMissingSessionError(err) || isTmuxNoServerError(err) {
			return nil
		}
		return err
	}
	return nil
}

func claudeTmuxSessionExists(ctx context.Context, sessionName string) (bool, error) {
	if strings.TrimSpace(sessionName) == "" {
		return false, nil
	}
	if err := runCommand(ctx, nil, "tmux", "has-session", "-t", sessionName); err != nil {
		if isTmuxMissingSessionError(err) || isTmuxNoServerError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func claudeInteractiveSessionsFromTmuxList(out, prefix string) []string {
	var sessions []string
	for _, line := range strings.Split(out, "\n") {
		sessionName := strings.TrimSpace(line)
		if sessionName == "" {
			continue
		}
		if sessionName == prefix || strings.HasPrefix(sessionName, prefix+"-") {
			sessions = append(sessions, sessionName)
		}
	}
	return sessions
}

func isTmuxNoServerError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no server running") ||
		strings.Contains(msg, "failed to connect to server")
}

func isTmuxMissingSessionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "can't find pane") ||
		strings.Contains(msg, "no current target")
}

func isClaudeTmuxSessionLostError(err error) bool {
	return isTmuxNoServerError(err) || isTmuxMissingSessionError(err)
}

func writeTempJSONConfig(pattern, value string) (string, error) {
	return writeTempFile(pattern, value)
}

func writeTempFile(pattern, value string) (string, error) {
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	path := tmp.Name()
	if _, err := tmp.WriteString(value); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("failed to write temp file %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("failed to close temp file %s: %w", path, err)
	}
	return path, nil
}

func removeFiles(paths []string) {
	for _, path := range paths {
		// Honor byte-restore registrations from
		// writeClaudeCodeProjectInstructionFile and
		// writeClaudeCodeProjectMCPFile: if the path is in
		// claudeProjectFileRestores, write the prior bytes back instead
		// of deleting the file. Files we created from nothing fall
		// through to the unconditional os.Remove path.
		if restored, ok := claudeProjectFileRestores.LoadAndDelete(path); ok {
			if bs, isBytes := restored.([]byte); isBytes {
				_ = os.WriteFile(path, bs, 0o600)
				continue
			}
		}
		_ = os.Remove(path)
	}
}

func runCommand(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	_, err := runCommandOutput(ctx, stdin, name, args...)
	return err
}

func runCommandOutput(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sanitizeTmuxSessionName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "mlp-claude-code"
	}

	var b strings.Builder
	for _, r := range name {
		if r == ':' || r == '.' || r == '/' || r == '\\' || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			b.WriteByte('-')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var (
	claudeStatusLineStreamMu sync.Mutex
	claudeStatusLineStreamed = make(map[string]string) // sessionName -> raw JSON content of last streamed statusline
)

// readClaudeStatuslineWithWait reads the statusline temp file, polling until it
// exists and is non-empty. Claude writes it asynchronously when its TUI renders
// the statusLine command, which can lag a freshly-returned turn — so a one-shot
// read races the render. Bounded by the smaller of ~15s and the context
// deadline; returns the last read error if the file never materializes.
func readClaudeStatuslineWithWait(ctx context.Context, outputPath string) ([]byte, error) {
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	var lastErr error
	for {
		raw, err := os.ReadFile(outputPath)
		if err == nil && len(bytes.TrimSpace(raw)) > 0 {
			return raw, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("statusline payload at %s is empty", outputPath)
		}
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func claudeStatuslinePath(sessionName string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("claude_statusline_%s.json", sessionName))
}

func (c *ClaudeCodeInteractiveAdapter) prepareStatusLineSettings(opts *llmtypes.CallOptions, sessionName string) (string, []string, error) {
	outputPath := claudeStatuslinePath(sessionName)
	// Create helper script that cat's stdin to outputPath
	scriptContent := fmt.Sprintf("#!/bin/sh\ncat > %s\n", outputPath)
	scriptPath, err := writeTempFile("claude-statusline-helper-*.sh", scriptContent)
	if err != nil {
		return "", nil, err
	}
	_ = os.Chmod(scriptPath, 0o755)

	var tempFiles []string
	tempFiles = append(tempFiles, scriptPath)

	settingsMap := make(map[string]interface{})
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if settings, ok := opts.Metadata.Custom[MetadataKeySettings].(string); ok && strings.TrimSpace(settings) != "" {
			settings = strings.TrimSpace(settings)
			if strings.HasPrefix(settings, "{") {
				_ = json.Unmarshal([]byte(settings), &settingsMap)
			} else {
				if raw, err := os.ReadFile(settings); err == nil {
					_ = json.Unmarshal(raw, &settingsMap)
				}
			}
		}
	}

	settingsMap["statusLine"] = map[string]interface{}{
		"type":    "command",
		"command": "sh " + scriptPath,
		"padding": 0,
	}

	settingsJSON, err := json.Marshal(settingsMap)
	if err != nil {
		removeFiles(tempFiles)
		return "", nil, err
	}

	settingsPath, err := writeTempJSONConfig("claude-code-settings-*.json", string(settingsJSON))
	if err != nil {
		removeFiles(tempFiles)
		return "", nil, err
	}

	tempFiles = append(tempFiles, settingsPath)
	return settingsPath, tempFiles, nil
}

func streamClaudeStatusLine(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) bool {
	if streamChan == nil {
		return false
	}
	outputPath := claudeStatuslinePath(sessionName)
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return false
	}

	// Deduplicate streams
	claudeStatusLineStreamMu.Lock()
	last := claudeStatusLineStreamed[sessionName]
	if last == trimmed {
		claudeStatusLineStreamMu.Unlock()
		return false
	}
	claudeStatusLineStreamed[sessionName] = trimmed
	claudeStatusLineStreamMu.Unlock()

	// Pass no default model: the real model comes from the statusLine JSON
	// (model.display_name). Fabricating "claude-3-5-sonnet" here would show a
	// wrong model whenever a different Claude model is in use.
	status, err := parseClaudeStatusLineJSON(raw, "")
	if err != nil {
		return false
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

func parseClaudeStatusLineJSON(raw []byte, defaultModel string) (*llmtypes.StatusLine, error) {
	// Parse with standard mapping, supporting both camelCase and snake_case or customized Claude Code output
	var payload struct {
		InputTokens              int `json:"input_tokens"`
		InputTokensCamel         int `json:"inputTokens"`
		OutputTokens             int `json:"output_tokens"`
		OutputTokensCamel        int `json:"outputTokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheCreationInputCamel  int `json:"cacheCreationInputTokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheReadInputCamel      int `json:"cacheReadInputTokens"`
		TotalInputTokens         int `json:"total_input_tokens"`
		TotalInputCamel          int `json:"totalInputTokens"`
		TotalOutputTokens        int `json:"total_output_tokens"`
		TotalOutputCamel         int `json:"totalOutputTokens"`
	}

	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		return nil, err
	}

	status := &llmtypes.StatusLine{
		Provider: "claudecode",
		Model:    defaultModel,
	}

	// Resolve the token counts
	if payload.InputTokens > 0 {
		status.InputTokens = payload.InputTokens
	} else {
		status.InputTokens = payload.InputTokensCamel
	}

	if payload.OutputTokens > 0 {
		status.OutputTokens = payload.OutputTokens
	} else {
		status.OutputTokens = payload.OutputTokensCamel
	}

	if payload.CacheCreationInputTokens > 0 {
		status.CacheCreationInputTokens = payload.CacheCreationInputTokens
	} else {
		status.CacheCreationInputTokens = payload.CacheCreationInputCamel
	}

	if payload.CacheReadInputTokens > 0 {
		status.CacheReadInputTokens = payload.CacheReadInputTokens
	} else {
		status.CacheReadInputTokens = payload.CacheReadInputCamel
	}

	if payload.TotalInputTokens > 0 {
		status.TotalInputTokens = payload.TotalInputTokens
	} else {
		status.TotalInputTokens = payload.TotalInputCamel
	}

	if payload.TotalOutputTokens > 0 {
		status.TotalOutputTokens = payload.TotalOutputTokens
	} else {
		status.TotalOutputTokens = payload.TotalOutputCamel
	}

	// Map raw payload to metadata
	var rawMap map[string]interface{}
	if err := json.Unmarshal(raw, &rawMap); err == nil {
		status.Metadata = rawMap

		// Claude Code's native statusLine input carries the real model and cost,
		// which the token-focused struct above ignores. Extract them defensively
		// from the raw map so we surface the actual model (not a hardcoded
		// default) and the cost — without breaking the custom token format that
		// has neither. model may be an object ({id, display_name}) or a string.
		switch m := rawMap["model"].(type) {
		case map[string]interface{}:
			if dn, _ := m["display_name"].(string); strings.TrimSpace(dn) != "" {
				status.Model = dn
			} else if id, _ := m["id"].(string); strings.TrimSpace(id) != "" {
				status.Model = id
			}
		case string:
			if strings.TrimSpace(m) != "" {
				status.Model = m
			}
		}
		switch cost := rawMap["cost"].(type) {
		case map[string]interface{}:
			if c := claudeFloatFromAny(cost["total_cost_usd"]); c > 0 {
				status.CostUSD = c
			}
		default:
			if c := claudeFloatFromAny(rawMap["total_cost_usd"]); c > 0 {
				status.CostUSD = c
			}
		}

		// Expose plan rate-limit usage, context fill, and effort as generic
		// statusline extras so UIs can render them next to cost without knowing
		// Claude Code's schema.
		status.SetStatusExtras(claudeStatusExtras(rawMap))
	}

	// "claude-code"/"claudecode" are placeholder ids equal to the provider name;
	// emitting them as Model renders a duplicate "claudecode · claude-code".
	if status.Model == "claude-code" || status.Model == "claudecode" {
		status.Model = ""
	}

	return status, nil
}

// claudeFloatFromAny coerces a JSON-decoded number (float64) — or a numeric
// string — into a float64, returning 0 when the value isn't numeric.
func claudeFloatFromAny(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

// claudeStatusExtras turns Claude Code's native statusLine fields into
// display-ready segments, in footer order: plan rate-limit usage ("5h N%",
// "7d N%"), context-window fill ("ctx N%"), and reasoning effort ("xhigh").
// Every field is optional — rate_limits only appears for Pro/Max after the first
// API response, each window may be independently absent, and effort/context may
// be missing — so a missing field simply yields no segment. Schema:
//
//	"rate_limits":    {"five_hour": {"used_percentage": 24.0}, "seven_day": {"used_percentage": 41.0}}
//	"context_window": {"used_percentage": 4}
//	"effort":         {"level": "xhigh"}
func claudeStatusExtras(rawMap map[string]interface{}) []string {
	var extras []string
	if rl, ok := rawMap["rate_limits"].(map[string]interface{}); ok {
		for _, w := range []struct{ key, label string }{
			{"five_hour", "5h"},
			{"seven_day", "7d"},
		} {
			win, ok := rl[w.key].(map[string]interface{})
			if !ok {
				continue
			}
			if _, present := win["used_percentage"]; !present {
				continue
			}
			extras = append(extras, llmtypes.FormatUsageExtraWithReset(
				w.label,
				claudeFloatFromAny(win["used_percentage"]),
				int64(claudeFloatFromAny(win["resets_at"])),
				time.Now(),
			))
		}
	}
	if cw, ok := rawMap["context_window"].(map[string]interface{}); ok {
		if _, present := cw["used_percentage"]; present {
			extras = append(extras, llmtypes.FormatUsageExtra("ctx", claudeFloatFromAny(cw["used_percentage"])))
		}
	}
	if eff, ok := rawMap["effort"].(map[string]interface{}); ok {
		if level, _ := eff["level"].(string); strings.TrimSpace(level) != "" {
			extras = append(extras, level)
		}
	}
	return extras
}

// GetStatusLine retrieves a snapshot of the current statusline for the active session.
// Satisfies the llmtypes.StatusLineProvider interface.
func (c *ClaudeCodeInteractiveAdapter) GetStatusLine(ctx context.Context, sessionID string) (*llmtypes.StatusLine, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}

	// Find the session in the registry or use fallback
	var tmuxSessionName string
	claudeInteractivePersistentRegistry.Lock()
	for _, sess := range claudeInteractivePersistentRegistry.sessions {
		if sess != nil && (sess.ownerSessionID == sessionID || sess.tmuxSessionName == sessionID) {
			tmuxSessionName = sess.tmuxSessionName
			break
		}
	}
	claudeInteractivePersistentRegistry.Unlock()

	if tmuxSessionName == "" {
		tmuxSessionName = sessionID
	}

	outputPath := claudeStatuslinePath(tmuxSessionName)
	raw, err := readClaudeStatuslineWithWait(ctx, outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read statusline payload: %w", err)
	}

	// Use the adapter's configured model as the fallback; the statusLine JSON's
	// model (when present) overrides it inside parseClaudeStatusLineJSON, and the
	// "claude-code" placeholder is stripped there.
	status, err := parseClaudeStatusLineJSON(raw, c.modelID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse statusline payload: %w", err)
	}

	return status, nil
}
