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
	defaultTmuxTimeout             = 0
	defaultPersistentIdleTimeout   = 20 * time.Minute
	defaultBoundedRetention        = 30 * time.Minute
	defaultTmuxPollInterval        = 750 * time.Millisecond
	defaultTmuxCaptureLines        = "3000"
	minTmuxMajorVersion            = 3
	claudeIdleStableWindow         = 1200 * time.Millisecond
	claudeTailFallbackMaxLines     = 120
	promptPasteVisibleStableWindow = 900 * time.Millisecond
	promptPasteInvisibleGrace      = 1500 * time.Millisecond
	promptPasteLiveInputWait       = 2 * time.Second

	EnvClaudeTmuxSessionPrefix      = "CLAUDE_CODE_TMUX_SESSION_PREFIX"
	EnvClaudeTmuxTimeoutSeconds     = "CLAUDE_CODE_TMUX_TIMEOUT_SECONDS"
	EnvClaudeTmuxPromptWaitSeconds  = "CLAUDE_CODE_TMUX_PROMPT_WAIT_SECONDS"
	EnvClaudeTmuxIdleTimeoutSeconds = "CLAUDE_CODE_TMUX_IDLE_TIMEOUT_SECONDS"
	EnvClaudeTmuxStreamTmuxScreen   = "CLAUDE_CODE_STREAM_TMUX_SCREEN"

	// Legacy env names kept for existing deployments and test runners.
	EnvClaudeExperimentalSessionPrefix      = "CLAUDE_CODE_EXPERIMENTAL_SESSION_PREFIX"
	EnvClaudeExperimentalTimeoutSeconds     = "CLAUDE_CODE_EXPERIMENTAL_TIMEOUT_SECONDS"
	EnvClaudeExperimentalPromptWaitSeconds  = "CLAUDE_CODE_EXPERIMENTAL_PROMPT_WAIT_SECONDS"
	EnvClaudeExperimentalIdleTimeoutSeconds = "CLAUDE_CODE_EXPERIMENTAL_IDLE_TIMEOUT_SECONDS"
	EnvClaudeExperimentalStreamTmuxScreen   = EnvClaudeTmuxStreamTmuxScreen
	EnvClaudePromptSuggestion               = "CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"
)

var claudeExperimentalSessionRegistry = struct {
	sync.Mutex
	sessions map[string]struct{}
}{
	sessions: map[string]struct{}{},
}

var claudeExperimentalInteractiveRegistry = struct {
	sync.RWMutex
	sessions map[string]string
}{
	sessions: map[string]string{},
}

type claudeExperimentalPersistentSession struct {
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

var claudeExperimentalPersistentRegistry = struct {
	sync.Mutex
	sessions map[string]*claudeExperimentalPersistentSession
}{
	sessions: map[string]*claudeExperimentalPersistentSession{},
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

// ClaudeCodeExperimentalAdapter is the legacy exported name for
// ClaudeCodeInteractiveAdapter. New callers should use NewClaudeCodeInteractiveAdapter.
type ClaudeCodeExperimentalAdapter = ClaudeCodeInteractiveAdapter

func NewClaudeCodeInteractiveAdapter(modelID string, logger interfaces.Logger) *ClaudeCodeInteractiveAdapter {
	return newClaudeCodeInteractiveAdapter(modelID, logger)
}

// NewClaudeCodeExperimentalAdapter is kept for compatibility.
// Deprecated: use NewClaudeCodeInteractiveAdapter.
func NewClaudeCodeExperimentalAdapter(modelID string, logger interfaces.Logger) *ClaudeCodeInteractiveAdapter {
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
	var persistentSession *claudeExperimentalPersistentSession
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
		args, tempFiles, err := c.buildClaudeArgs(opts, nativeSessionID, systemPrompt)
		if err != nil {
			return nil, err
		}
		defer removeFiles(tempFiles)

		if err := c.startSession(callCtx, sessionName, args, workingDir); err != nil {
			return nil, err
		}
		registerClaudeExperimentalSession(sessionName)
		cleanupSession := cleanupClaudeExperimentalSessionAfter(sessionName, llmtypes.TmuxKillDelay)
		defer cleanupSession()
		if interactiveSessionID != "" {
			registerClaudeExperimentalInteractiveSession(interactiveSessionID, sessionName)
			defer unregisterClaudeExperimentalInteractiveSession(interactiveSessionID, sessionName)
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
			if interruptErr := interruptClaudeExperimentalSession(sessionName, c.logger); interruptErr != nil {
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

func (c *ClaudeCodeInteractiveAdapter) buildClaudeArgs(opts *llmtypes.CallOptions, nativeSessionID, systemPrompt string) ([]string, []string, error) {
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
		if tools, ok := opts.Metadata.Custom[MetadataKeyTools].(string); ok {
			toolsArg = tools
		}
		if allowedTools, ok := opts.Metadata.Custom[MetadataKeyAllowedTools].(string); ok && strings.TrimSpace(allowedTools) != "" {
			extraArgs = append(extraArgs, "--allowed-tools", allowedTools)
		}
		if effort, ok := opts.Metadata.Custom[MetadataKeyEffort].(string); ok && strings.TrimSpace(effort) != "" {
			extraArgs = append(extraArgs, "--effort", effort)
		}
	}

	args := []string{"claude", "--permission-mode", "dontAsk"}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	} else if nativeSessionID != "" {
		args = append(args, "--session-id", nativeSessionID, "--name", defaultClaudeDisplayName())
	}
	if c.shouldPassModelFlag() {
		args = append(args, "--model", c.modelID)
	}
	if strings.TrimSpace(systemPrompt) != "" {
		systemPromptPath, err := writeTempFile("claude-code-system-prompt-*.md", systemPrompt)
		if err != nil {
			return nil, nil, err
		}
		tempFiles = append(tempFiles, systemPromptPath)
		args = append(args, "--system-prompt-file", systemPromptPath)
	}

	// Project the system prompt into <workingDir>/CLAUDE.md (Claude
	// Code's project-instructions convention) with byte-restore on
	// cleanup. ON by default; operators that need to protect their own
	// CLAUDE.md can opt out with WithClaudeCodeWriteProjectInstructionFile(false).
	// The --system-prompt-file flag above already injects the prompt; this
	// also makes the prompt visible inside the workspace for debugging
	// and downstream tooling that reads project instructions.
	//
	// When the same flag is on AND an MCP config was provided via
	// WithMCPConfig, we also project the MCP servers into
	// <workingDir>/.mcp.json (Claude Code's project-scoped MCP
	// convention) with byte-restore on cleanup so operator-owned
	// .mcp.json content survives the session.
	if writeProjectInstructionFromOptions(opts) && opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		workingDir, _ := opts.Metadata.Custom[MetadataKeyWorkingDir].(string)
		if strings.TrimSpace(systemPrompt) != "" {
			if rulePath, err := writeClaudeCodeProjectInstructionFile(workingDir, systemPrompt); err != nil {
				// Best-effort: a write failure here must not block the
				// session. The primary --system-prompt-file injection
				// already succeeded; CLAUDE.md is purely additive.
				_ = err
			} else if rulePath != "" {
				tempFiles = append(tempFiles, rulePath)
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
				if mcpPath, err := writeClaudeCodeProjectMCPFile(workingDir, mcpConfig); err != nil {
					_ = err
				} else if mcpPath != "" {
					tempFiles = append(tempFiles, mcpPath)
				}
			}
		}
	}

	// Project attached skills into .claude/skills/ so Claude Code's
	// skill loader picks them up at startup. Independent of the
	// writeProjectInstructionFromOptions gate (which controls CLAUDE.md);
	// skills are useful even when the instruction-file projection is off.
	// Best-effort; matches the codex/gemini/cursor/agy/opencode pattern.
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

// writeClaudeCodeProjectInstructionFile installs the per-session system
// prompt at <workingDir>/CLAUDE.md (Claude Code's project-instructions
// convention). If a pre-existing CLAUDE.md is present its bytes are
// registered with claudeProjectFileRestores so removeFiles restores them
// on session cleanup; if absent, the written file is os.Remove'd. Returns
// the absolute path so the caller can append it to tempFiles for the
// existing cleanup flow.
//
// Returns "" with nil error when no work needs doing (empty workingDir);
// returns a non-empty path with non-nil error only when the write itself
// failed. The caller treats any error as best-effort and continues; the
// primary --system-prompt-file injection has already succeeded.
//
// Risk caveat: CLAUDE.md is a single-file convention; if the orchestrator
// process crashes between write and cleanup, the operator's pre-existing
// CLAUDE.md is destroyed. Callers that need to protect operator content
// can pass WithClaudeCodeWriteProjectInstructionFile(false).
func writeClaudeCodeProjectInstructionFile(workingDir, systemPrompt string) (string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return "", fmt.Errorf("ensure claude working dir: %w", err)
	}
	path := filepath.Join(workingDir, "CLAUDE.md")
	if prior, err := os.ReadFile(path); err == nil {
		claudeProjectFileRestores.Store(path, prior)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read pre-existing CLAUDE.md: %w", err)
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
	shellCommand := claudeExperimentalShellCommand(args, workingDir)
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

func claudeExperimentalShellCommand(args []string, workingDir string) string {
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

func claudeExperimentalStreamTmuxScreenEnabled() bool {
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
	promptWait := promptReadyTimeout()
	deadline, cancel := context.WithTimeout(ctx, promptWait)
	defer cancel()

	ticker := time.NewTicker(defaultTmuxPollInterval)
	defer ticker.Stop()
	resumePromptHandled := false
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	streamTerminalScreen := claudeExperimentalStreamTmuxScreenEnabled()

	for {
		select {
		case <-deadline.Done():
			captured, _ := captureTmuxPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out after %s waiting for Claude Code prompt; %s", promptWait, llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return fmt.Errorf("timed out after %s waiting for Claude Code prompt", promptWait)
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
					continue
				}
			}
			if hasReadyInputPrompt(captured) {
				return nil
			}
		}
	}
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
	if !(strings.Contains(normalized, "compress") || strings.Contains(normalized, "compact")) {
		return false
	}
	if !strings.Contains(normalized, "continue") {
		return false
	}
	return strings.Contains(normalized, "resume") ||
		strings.Contains(normalized, "conversation") ||
		strings.Contains(normalized, "context")
}

func sendPromptToTmux(ctx context.Context, sessionName, prompt string) error {
	bufferName := "mlp-claude-prompt-" + randomHex(6)
	prompt = strings.TrimRight(prompt, "\n")

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
	promptSubmitted, err := waitForPromptPaste(ctx, sessionName, paneBeforePaste)
	if err != nil {
		return err
	}
	if promptSubmitted {
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		preSubmitPane, _ := captureTmuxPane(ctx, sessionName)
		args := append([]string{"send-keys", "-t", sessionName}, claudeSubmitPromptKeys()...)
		if err := runCommand(ctx, nil, "tmux", args...); err != nil {
			return fmt.Errorf("failed to submit prompt to Claude Code tmux session: %w", err)
		}
		if err := waitForPromptAccepted(ctx, sessionName, preSubmitPane); err == nil {
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
	for attempt := 1; attempt <= 3; attempt++ {
		args := append([]string{"send-keys", "-t", sessionName}, claudeSubmitPromptKeys()...)
		if err := runCommand(ctx, nil, "tmux", args...); err != nil {
			return fmt.Errorf("failed to submit input to Claude Code tmux session: %w", err)
		}
		if err := waitForClaudeLiveInputSubmitted(ctx, sessionName, message); err == nil {
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
	if err := runCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-e", "C-u"); err != nil {
		return fmt.Errorf("failed to clear stale Claude Code prompt draft %q: %w", truncateClaudeDraftForError(draft, 120), err)
	}
	if err := waitForClaudePromptDraftCleared(ctx, sessionName); err != nil {
		return fmt.Errorf("failed to clear stale Claude Code prompt draft %q: %w", truncateClaudeDraftForError(draft, 120), err)
	}
	return nil
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

func interruptClaudeExperimentalSession(sessionName string, logger interfaces.Logger) error {
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

func waitForPromptAccepted(ctx context.Context, sessionName, preSubmitPane string) error {
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
			if hasClaudeActivity(captured) {
				return nil
			}
			if hasReadyEmptyInputPrompt(captured) && hasNewAssistantOutput(capturedAfterPaneBaseline(captured, preSubmitPane)) {
				return nil
			}
		}
	}
}

func waitForClaudeLiveInputSubmitted(ctx context.Context, sessionName, message string) error {
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
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
				if hasClaudeActivity(captured) {
					return nil
				}
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
	streamTerminalScreen := claudeExperimentalStreamTmuxScreenEnabled()

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
	var b, esc strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inEscape {
			esc.WriteByte(ch)
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				if ch == 'm' {
					b.WriteString(esc.String())
				}
				esc.Reset()
				inEscape = false
			}
			continue
		}
		if ch == 0x1b {
			esc.WriteByte(ch)
			inEscape = true
			continue
		}
		b.WriteByte(ch)
	}
	return paneview.CollapseBlankRuns(b.String())
}

func captureTmuxPaneForDisplay(ctx context.Context, sessionName string) (string, error) {
	var out bytes.Buffer
	// -e preserves ANSI SGR (color, bold, dim, etc.) so the frontend can
	// colorize the snapshot via ansi_up. Cursor positioning sequences are
	// stripped by stripClaudeANSIPreserveColors in streamClaudeTerminalSnapshot
	// before the snapshot leaves the adapter so they don't garble rendering.
	cmd := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", sessionName, "-p", "-e", "-S", "-"+defaultTmuxCaptureLines)
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

func persistentInteractiveIdleTimeout() time.Duration {
	seconds, ok := claudeDurationSecondsFromEnv(EnvClaudeTmuxIdleTimeoutSeconds, EnvClaudeExperimentalIdleTimeoutSeconds)
	if !ok || seconds <= 0 {
		return defaultPersistentIdleTimeout
	}
	return time.Duration(seconds) * time.Second
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

// CleanupClaudeCodeExperimentalSessions is kept for compatibility.
// Deprecated: use CleanupClaudeCodeTmuxSessions.
func CleanupClaudeCodeExperimentalSessions(ctx context.Context) error {
	return CleanupClaudeCodeTmuxSessions(ctx)
}

// CleanupClaudeCodeTmuxSessions cleans up registered Claude Code tmux sessions.
func CleanupClaudeCodeTmuxSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}

	sessions := activeClaudeExperimentalSessions()
	persistentSessions := drainClaudePersistentInteractiveSessions()
	for _, session := range persistentSessions {
		sessions = appendUniqueStrings(sessions, session.tmuxSessionName)
		unregisterClaudeExperimentalInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		removeFiles(session.tempFiles)
	}

	var failures []string
	for _, sessionName := range sessions {
		if err := killClaudeExperimentalSession(ctx, sessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up Claude Code tmux sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func activeClaudeExperimentalSessions() []string {
	claudeExperimentalSessionRegistry.Lock()
	defer claudeExperimentalSessionRegistry.Unlock()

	sessions := make([]string, 0, len(claudeExperimentalSessionRegistry.sessions))
	for sessionName := range claudeExperimentalSessionRegistry.sessions {
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

func claudeExperimentalSessionPrefix() string {
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
	prefix := claudeExperimentalSessionPrefix()
	return sanitizeTmuxSessionName(fmt.Sprintf("%s-%d-%s", prefix, time.Now().UnixNano(), randomHex(4)))
}

func registerClaudeExperimentalSession(sessionName string) {
	claudeExperimentalSessionRegistry.Lock()
	defer claudeExperimentalSessionRegistry.Unlock()
	claudeExperimentalSessionRegistry.sessions[sessionName] = struct{}{}
}

func unregisterClaudeExperimentalSession(sessionName string) {
	claudeExperimentalSessionRegistry.Lock()
	defer claudeExperimentalSessionRegistry.Unlock()
	delete(claudeExperimentalSessionRegistry.sessions, sessionName)
}

func registerClaudeExperimentalInteractiveSession(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if ownerSessionID == "" || tmuxSessionName == "" {
		return
	}
	claudeExperimentalInteractiveRegistry.Lock()
	defer claudeExperimentalInteractiveRegistry.Unlock()
	claudeExperimentalInteractiveRegistry.sessions[ownerSessionID] = tmuxSessionName
}

func unregisterClaudeExperimentalInteractiveSession(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return
	}
	claudeExperimentalInteractiveRegistry.Lock()
	defer claudeExperimentalInteractiveRegistry.Unlock()
	if current := claudeExperimentalInteractiveRegistry.sessions[ownerSessionID]; current == tmuxSessionName {
		delete(claudeExperimentalInteractiveRegistry.sessions, ownerSessionID)
	}
}

func activeClaudeExperimentalInteractiveSession(ownerSessionID string) (string, bool) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return "", false
	}
	claudeExperimentalInteractiveRegistry.RLock()
	defer claudeExperimentalInteractiveRegistry.RUnlock()
	sessionName, ok := claudeExperimentalInteractiveRegistry.sessions[ownerSessionID]
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
func (c *ClaudeCodeInteractiveAdapter) acquirePersistentInteractiveSession(ctx context.Context, ownerSessionID, nativeSessionID string, opts *llmtypes.CallOptions, systemPrompt string, workingDir string) (*claudeExperimentalPersistentSession, error) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return nil, fmt.Errorf("persistent Claude Code tmux session requires an owner session ID")
	}

	claudeExperimentalPersistentRegistry.Lock()
	if existing := claudeExperimentalPersistentRegistry.sessions[ownerSessionID]; existing != nil {
		existing.mu.Lock()
		if existing.initErr != nil {
			err := existing.initErr
			existing.mu.Unlock()
			claudeExperimentalPersistentRegistry.Unlock()
			return nil, err
		}
		if existing.idleTimer != nil {
			existing.idleTimer.Stop()
			existing.idleTimer = nil
		}
		existing.lastUsed = time.Now()
		claudeExperimentalPersistentRegistry.Unlock()
		return existing, nil
	}

	now := time.Now()
	sessionName := newTmuxSessionName()
	session := &claudeExperimentalPersistentSession{
		ownerSessionID:  ownerSessionID,
		tmuxSessionName: sessionName,
		nativeSessionID: nativeSessionID,
		workingDir:      strings.TrimSpace(workingDir),
		createdAt:       now,
		lastUsed:        now,
	}
	session.mu.Lock()
	claudeExperimentalPersistentRegistry.sessions[ownerSessionID] = session
	claudeExperimentalPersistentRegistry.Unlock()

	args, tempFiles, err := c.buildClaudeArgs(opts, nativeSessionID, systemPrompt)
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
	registerClaudeExperimentalSession(sessionName)
	registerClaudeExperimentalInteractiveSession(ownerSessionID, sessionName)
	return session, nil
}

func releaseClaudePersistentInteractiveSession(session *claudeExperimentalPersistentSession, logger interfaces.Logger) {
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

func closeClaudePersistentInteractiveSession(ownerSessionID, reason string, logger interfaces.Logger) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return
	}

	claudeExperimentalPersistentRegistry.Lock()
	session := claudeExperimentalPersistentRegistry.sessions[ownerSessionID]
	if session == nil {
		claudeExperimentalPersistentRegistry.Unlock()
		return
	}
	delete(claudeExperimentalPersistentRegistry.sessions, ownerSessionID)
	claudeExperimentalPersistentRegistry.Unlock()

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
	_ = killClaudeExperimentalSession(cleanupCtx, session.tmuxSessionName)
	unregisterClaudeExperimentalInteractiveSession(ownerSessionID, session.tmuxSessionName)
	unregisterClaudeExperimentalSession(session.tmuxSessionName)
	removeFiles(session.tempFiles)
}

func markClaudePersistentInteractiveSessionFailedLocked(session *claudeExperimentalPersistentSession, err error, logger interfaces.Logger) {
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

func cleanupFailedClaudePersistentInteractiveSession(session *claudeExperimentalPersistentSession) {
	if session == nil {
		return
	}
	removeClaudePersistentInteractiveSession(session.ownerSessionID, session)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killClaudeExperimentalSession(cleanupCtx, session.tmuxSessionName)
	unregisterClaudeExperimentalInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
	unregisterClaudeExperimentalSession(session.tmuxSessionName)
	removeFiles(session.tempFiles)
}

func removeClaudePersistentInteractiveSession(ownerSessionID string, session *claudeExperimentalPersistentSession) {
	claudeExperimentalPersistentRegistry.Lock()
	defer claudeExperimentalPersistentRegistry.Unlock()
	if current := claudeExperimentalPersistentRegistry.sessions[ownerSessionID]; current == session {
		delete(claudeExperimentalPersistentRegistry.sessions, ownerSessionID)
	}
}

func drainClaudePersistentInteractiveSessions() []*claudeExperimentalPersistentSession {
	claudeExperimentalPersistentRegistry.Lock()
	sessions := make([]*claudeExperimentalPersistentSession, 0, len(claudeExperimentalPersistentRegistry.sessions))
	for _, session := range claudeExperimentalPersistentRegistry.sessions {
		sessions = append(sessions, session)
	}
	claudeExperimentalPersistentRegistry.sessions = map[string]*claudeExperimentalPersistentSession{}
	claudeExperimentalPersistentRegistry.Unlock()

	for _, session := range sessions {
		stopClaudeIdleTimerIfAvailable(session)
	}
	return sessions
}

func stopClaudeIdleTimerIfAvailable(session *claudeExperimentalPersistentSession) {
	if session == nil || !session.mu.TryLock() {
		return
	}
	defer session.mu.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
}

// SendClaudeCodeExperimentalInput is kept for compatibility.
// Deprecated: use SendClaudeCodeInput.
func SendClaudeCodeExperimentalInput(ctx context.Context, ownerSessionID, message string) error {
	return SendClaudeCodeInput(ctx, ownerSessionID, message)
}

// SendClaudeCodeInput routes live input into a registered Claude Code tmux session.
func SendClaudeCodeInput(ctx context.Context, ownerSessionID, message string) error {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return fmt.Errorf("Claude Code owner session ID is required")
	}
	sessionName, ok := activeClaudeExperimentalInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Claude Code tmux session registered for owner session %s", ownerSessionID)
	}
	return sendInputToActiveTmux(ctx, sessionName, message)
}

func cleanupClaudeExperimentalSessionAfter(sessionName string, retention time.Duration) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if retention <= 0 {
				closeClaudeExperimentalSessionNow(sessionName)
				return
			}
			time.AfterFunc(retention, func() {
				closeClaudeExperimentalSessionNow(sessionName)
			})
		})
	}
}

func closeClaudeExperimentalSessionNow(sessionName string) {
	defer unregisterClaudeExperimentalSession(sessionName)
	killCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killClaudeExperimentalSession(killCtx, sessionName)
}

func killClaudeExperimentalSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return nil
	}
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

func claudeExperimentalSessionsFromTmuxList(out, prefix string) []string {
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
