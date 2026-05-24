package codexcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/manishiitg/multi-llm-provider-go/internal/shelllaunch"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

const (
	// Default to no provider-level turn timeout. Workflow/background callers own
	// their execution deadline; the adapter should not cancel a still-running tmux
	// coding agent before the outer workflow timeout.
	defaultCodexInteractiveTimeout     = 0
	defaultCodexInteractiveIdleTimeout = 20 * time.Minute
	defaultCodexInteractiveRetention   = 30 * time.Minute
	codexInteractiveStableWindow       = 1200 * time.Millisecond
	codexActivityScanNonEmptyLines     = 160

	EnvCodexInteractiveSessionPrefix      = "CODEX_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvCodexInteractiveTimeoutSeconds     = "CODEX_CLI_INTERACTIVE_TIMEOUT_SECONDS"
	EnvCodexInteractiveIdleTimeoutSeconds = "CODEX_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvCodexInteractivePromptWaitSeconds  = "CODEX_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvCodexInteractiveStreamTmuxScreen   = "CODEX_CLI_STREAM_TMUX_SCREEN"
)

type codexInteractiveSession struct {
	ownerSessionID       string
	tmuxSessionName      string
	systemPromptTempFile string
	// projectInstructionCleanup runs at session teardown when the
	// opt-in WithWriteProjectInstructionFile flag was set. It restores
	// any pre-existing operator AGENTS.md byte-for-byte. nil if the
	// flag wasn't enabled for this session.
	projectInstructionCleanup func()
	workingDir           string
	idleTimer            *time.Timer
	initErr              error
	createdAt            time.Time
	lastUsed             time.Time
	mu                   sync.Mutex
}

var codexInteractiveRegistry = struct {
	sync.RWMutex
	sessions map[string]string
}{
	sessions: map[string]string{},
}

var codexPersistentRegistry = struct {
	sync.Mutex
	sessions map[string]*codexInteractiveSession
}{
	sessions: map[string]*codexInteractiveSession{},
}

func (c *CodexCLIAdapter) generateContentInteractive(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; codex-cli interactive mode requires tmux")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return nil, fmt.Errorf("codex cli not found in PATH. Please install it first (npm install -g @openai/codex)")
	}

	ownerSessionID := codexInteractiveSessionIDFromOptions(opts)
	if ownerSessionID == "" {
		return nil, fmt.Errorf("codex-cli interactive mode requires an owner session ID")
	}
	persistent := codexPersistentInteractiveFromOptions(opts)

	turnStart := time.Now().UTC()

	// Inspector emitter — no-op when opts.InspectorSink is nil. Lifecycle
	// events (tmux acquiring/ready/prompt/captured) are what the unified
	// debug panel renders for tmux providers, since they have no
	// per-token stream to tap into.
	inspector := llmtypes.NewInspectorEmitter(opts.InspectorSink, "codex-cli", c.modelID)
	inspector.EmitRequest(map[string]interface{}{
		"transport":     "tmux",
		"message_count": len(messages),
		"persistent":    persistent,
	})

	callCtx, cancel := codexInteractiveCallContext(ctx)
	defer cancel()

	// On user-initiated cancellation, tear down the persistent tmux
	// session so the live pane closes alongside the workflow step.
	// Without this, "cancel step" leaves the codex process running
	// in tmux indefinitely and the UI keeps the terminal entry
	// listed as active. DeadlineExceeded is treated separately —
	// timeouts shouldn't kill a session a user might still want.
	defer func() {
		if ctx.Err() != context.Canceled {
			return
		}
		killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer killCancel()
		closeCodexPersistentSession(ownerSessionID, "workflow context canceled", c.logger)
		_ = killCtx
	}()

	systemPrompt, conversationMessages := splitCodexSystemPrompt(messages)
	historicalAssistantTexts := codexAssistantHistory(conversationMessages)

	acquireStart := time.Now()
	inspector.EmitEvent("tmux_session_acquiring", map[string]interface{}{
		"owner_session_id": ownerSessionID,
	})
	session, err := c.acquireCodexInteractiveSession(callCtx, ownerSessionID, opts, systemPrompt)
	if err != nil {
		inspector.EmitError(err, map[string]interface{}{
			"phase":      "tmux_session_acquire",
			"elapsed_ms": time.Since(acquireStart).Milliseconds(),
		})
		return nil, err
	}
	inspector.EmitEvent("tmux_session_ready", map[string]interface{}{
		"tmux_session_name": session.tmuxSessionName,
		"elapsed_ms":        time.Since(acquireStart).Milliseconds(),
	})
	releaseSession := true
	defer func() {
		if releaseSession && session != nil {
			if persistent {
				releaseCodexInteractiveSession(session, c.logger)
			} else {
				releaseCodexBoundedInteractiveSession(session, c.logger)
			}
		}
	}()

	if err := waitForCodexPrompt(callCtx, session.tmuxSessionName, opts.StreamChan); err != nil {
		markCodexInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedCodexInteractiveSession(failedSession)
		return nil, err
	}
	resetCodexPaneForTurn(callCtx, session.tmuxSessionName)
	if err := waitForCodexPrompt(callCtx, session.tmuxSessionName, opts.StreamChan); err != nil {
		markCodexInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedCodexInteractiveSession(failedSession)
		return nil, err
	}

	if llmtypes.CodingProviderLaunchOnlyFromOptions(opts) {
		var lastSnapshot string
		streamCodexTerminalSnapshot(callCtx, session.tmuxSessionName, opts.StreamChan, &lastSnapshot)
		additional := map[string]interface{}{
			"provider":                     "codex-cli",
			"codex_mode":                   "interactive",
			"codex_interactive_session":    session.tmuxSessionName,
			"codex_persistent_interactive": persistent,
			"codex_uses_exec_json":         false,
		}
		gi := &llmtypes.GenerationInfo{Additional: additional}
		handleModel := c.modelID
		if modelToUse := resolveCodexCLIModelID(c.modelID); modelToUse != "" {
			handleModel = modelToUse
		}
		llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
			Provider:        "codex-cli",
			Transport:       llmtypes.CodingProviderTransportTmux,
			NativeSessionID: codexResumeSessionIDFromOptions(opts),
			TmuxSession:     session.tmuxSessionName,
			WorkingDir:      session.workingDir,
			Model:           handleModel,
			Status:          llmtypes.CodingProviderSessionStatusIdle,
		})
		return &llmtypes.ContentResponse{
			Choices: []*llmtypes.ContentChoice{{
				Content:        "",
				GenerationInfo: gi,
			}},
		}, nil
	}

	prompt := buildCodexInteractivePrompt(conversationMessages)
	baseline, _ := captureCodexPane(callCtx, session.tmuxSessionName)
	c.logger.Infof("Executing Codex CLI interactive tmux session: %s", session.tmuxSessionName)
	promptSentAt := time.Now()
	if err := sendCodexPromptToTmux(callCtx, session.tmuxSessionName, prompt); err != nil {
		inspector.EmitError(err, map[string]interface{}{"phase": "tmux_prompt_send"})
		return nil, err
	}
	inspector.EmitEvent("tmux_prompt_sent", map[string]interface{}{
		"prompt_length": len(prompt),
	})

	captured, err := waitForCodexInteractiveResponse(callCtx, session.tmuxSessionName, baseline, opts.StreamChan)
	forcedComplete := errors.Is(err, tmuxcontrol.ErrForceComplete)
	if err != nil && !forcedComplete {
		inspector.EmitError(err, map[string]interface{}{
			"phase":      "tmux_wait_response",
			"elapsed_ms": time.Since(promptSentAt).Milliseconds(),
		})
		if ctx.Err() != nil {
			interruptCodexInteractiveSession(session.tmuxSessionName, c.logger)
		}
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	if err := codexPolicyInvalidPromptError(captured); err != nil {
		inspector.EmitError(err, map[string]interface{}{
			"phase":      "tmux_wait_response",
			"elapsed_ms": time.Since(promptSentAt).Milliseconds(),
		})
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	inspector.EmitEvent("tmux_response_captured", map[string]interface{}{
		"response_chars": len(captured),
		"elapsed_ms":     time.Since(promptSentAt).Milliseconds(),
	})

	content := parseCodexInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	if forcedComplete && strings.TrimSpace(content) == "" {
		content = forcedCodexInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
	}
	if err := codexPolicyInvalidPromptTextError(content); err != nil {
		inspector.EmitError(err, map[string]interface{}{
			"phase":      "tmux_parse_response",
			"elapsed_ms": time.Since(promptSentAt).Milliseconds(),
		})
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		return nil, err
	}
	// Trailing-capture grace window — see llmtypes.RunTrailingPaneCapture.
	llmtypes.RunTrailingPaneCapture(callCtx, opts.StreamChan,
		func(ctx context.Context) (string, error) {
			snap, err := captureCodexPane(ctx, session.tmuxSessionName)
			if err != nil {
				return "", err
			}
			return strings.TrimRight(stripCodexANSI(snap), "\n"), nil
		},
		map[string]interface{}{
			"tmux_session":              session.tmuxSessionName,
			"codex_interactive_session": session.tmuxSessionName,
		},
	)
	if opts.StreamChan != nil {
		close(opts.StreamChan)
	}

	additional := map[string]interface{}{
		"provider":                     "codex-cli",
		"codex_mode":                   "interactive",
		"codex_interactive_session":    session.tmuxSessionName,
		"codex_persistent_interactive": persistent,
		"codex_uses_exec_json":         false,
	}
	if !persistent {
		// terminal_retention_seconds intentionally not set — see cursor.
		additional["codex_interactive_retention_seconds"] = int(codexInteractiveRetention().Seconds())
	}

	// Best-effort usage from codex's local rollout JSONL — tmux mode
	// has no stdout JSON, but ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
	// records token_count event_msgs with per-turn token snapshots.
	gi := &llmtypes.GenerationInfo{Additional: additional}
	handleModel := c.modelID
	usage, effectiveModel, threadID := readCodexTranscriptUsage(turnStart, session.workingDir)
	if threadID != "" {
		additional["codex_thread_id"] = threadID
	}
	if effectiveModel != "" {
		handleModel = effectiveModel
		additional["codex_effective_model"] = effectiveModel
	}
	if usage != nil {
		gi.PromptTokens = usage.PromptTokens
		gi.CompletionTokens = usage.CompletionTokens
		gi.TotalTokens = usage.TotalTokens
		gi.CachedContentTokens = usage.CachedContentTokens
		gi.ReasoningTokens = usage.ReasoningTokens
		// Forward raw cache-token keys from the parser into the
		// adapter's local Additional map so they survive to the cost
		// ledger's extractCacheTokens (keyed off raw Anthropic-style
		// names rather than the typed CachedContentTokens field).
		for k, v := range usage.Additional {
			additional[k] = v
		}
	}
	if effectiveModel != "" {
		if meta, _ := c.GetModelMetadata(effectiveModel); meta != nil {
			if cost := llmtypes.ComputeUSDCostFromMetadata(meta, gi); cost > 0 {
				additional["cost_usd_estimated"] = cost
				additional["cost_model_id"] = effectiveModel
			}
		}
	}
	llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
		Provider:        "codex-cli",
		Transport:       llmtypes.CodingProviderTransportTmux,
		NativeSessionID: threadID,
		TmuxSession:     session.tmuxSessionName,
		WorkingDir:      session.workingDir,
		Model:           handleModel,
		Status:          llmtypes.CodingProviderSessionStatusIdle,
	})

	// Reconstruct the CLI's internal tool-use trail (assistant text,
	// function/custom tool calls + outputs) from the same rollout
	// JSONL we read tokens from, so workflow conversation logs can
	// splice the in-CLI turn-by-turn record into their persisted
	// history. Best-effort: empty when the rollout is missing or has
	// not been flushed yet.
	if sidecarMsgs := readCodexTranscriptMessages(turnStart, session.workingDir); len(sidecarMsgs) > 0 {
		llmtypes.AttachCodingProviderIntermediateMessages(gi, llmtypes.CodingProviderIntermediateMessages{
			Provider:  "codex-cli",
			Transport: llmtypes.CodingProviderTransportTmux,
			Messages:  sidecarMsgs,
		})
	}

	// Inspector: emit the completion envelope. Token counts are
	// best-effort from the rollout JSONL; cost may be unavailable on
	// runs that didn't write a rollout file yet.
	if inspector.Enabled() {
		completionMeta := map[string]interface{}{
			"stop_reason":   "tmux_response_captured",
			"duration_ms":   time.Since(turnStart).Milliseconds(),
			"content_chars": len(content),
		}
		if gi.PromptTokens != nil {
			completionMeta["prompt_tokens"] = *gi.PromptTokens
		}
		if gi.CompletionTokens != nil {
			completionMeta["completion_tokens"] = *gi.CompletionTokens
		}
		if cost, ok := additional["cost_usd_estimated"].(float64); ok {
			completionMeta["cost_usd_estimated"] = cost
		}
		if cm, ok := additional["cost_model_id"].(string); ok && cm != "" {
			completionMeta["cost_model_id"] = cm
		}
		inspector.EmitCompletion(completionMeta)
	}

	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{
			{
				Content:        content,
				GenerationInfo: gi,
			},
		},
	}, nil
}

// acquireCodexInteractiveSession returns with session.mu held. The caller must
// either releaseCodexInteractiveSession on normal completion or mark, unlock,
// and clean up the session on a startup/ready-prompt failure.
func (c *CodexCLIAdapter) acquireCodexInteractiveSession(ctx context.Context, ownerSessionID string, opts *llmtypes.CallOptions, systemPrompt string) (*codexInteractiveSession, error) {
	codexPersistentRegistry.Lock()
	if existing := codexPersistentRegistry.sessions[ownerSessionID]; existing != nil {
		existing.mu.Lock()
		if existing.initErr != nil {
			err := existing.initErr
			existing.mu.Unlock()
			codexPersistentRegistry.Unlock()
			return nil, err
		}
		if existing.idleTimer != nil {
			existing.idleTimer.Stop()
			existing.idleTimer = nil
		}
		existing.lastUsed = time.Now()
		codexPersistentRegistry.Unlock()
		return existing, nil
	}

	now := time.Now()
	workingDir := codexWorkingDirFromOptions(opts)
	session := &codexInteractiveSession{
		ownerSessionID:  ownerSessionID,
		tmuxSessionName: newCodexTmuxSessionName(),
		workingDir:      workingDir,
		createdAt:       now,
		lastUsed:        now,
	}
	session.mu.Lock()
	codexPersistentRegistry.sessions[ownerSessionID] = session
	codexPersistentRegistry.Unlock()

	args, systemPromptTempFile, err := c.buildCodexInteractiveArgs(opts, systemPrompt)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		removeCodexPersistentSession(ownerSessionID, session)
		return nil, err
	}
	session.systemPromptTempFile = systemPromptTempFile
	// Opt-in: project up to four artifacts into the working dir for
	// codex's project conventions: AGENTS.md (system prompt),
	// .codex/config.toml ([mcp_servers] tables), .codex/hooks.json
	// (PreToolUse deny entry), .codex/hooks/deny-builtin.sh (the deny
	// script itself). Off by default; the existing -c model_instructions_file
	// and -c mcp_servers.* overrides already inject equivalent
	// configuration. The workspace projection is additive and useful
	// when downstream tooling reads codex's on-disk conventions
	// directly, and the deny hook is the strong lever for forcing
	// MCP-only tool routing.
	if writeProjectInstructionFromOptions(opts) && workingDir != "" {
		mcpServersJSON := ""
		if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
			if v, ok := opts.Metadata.Custom[MetadataKeyMCPServers].(string); ok {
				mcpServersJSON = v
			}
		}
		if cleanup, perr := writeCodexProjectArtifacts(workingDir, systemPrompt, mcpServersJSON, true); perr == nil {
			session.projectInstructionCleanup = cleanup
		}
		// Best-effort: a failure here is not a session-killer. The
		// primary injection paths succeeded; the workspace projection
		// is purely additive belt-and-suspenders.
	}
	if err := startCodexTmuxSession(ctx, session.tmuxSessionName, args, workingDir); err != nil {
		session.initErr = err
		if systemPromptTempFile != "" {
			_ = os.Remove(systemPromptTempFile)
		}
		if session.projectInstructionCleanup != nil {
			session.projectInstructionCleanup()
			session.projectInstructionCleanup = nil
		}
		session.mu.Unlock()
		removeCodexPersistentSession(ownerSessionID, session)
		return nil, err
	}
	registerCodexInteractiveSession(ownerSessionID, session.tmuxSessionName)
	return session, nil
}

func (c *CodexCLIAdapter) buildCodexInteractiveArgs(opts *llmtypes.CallOptions, systemPrompt string) ([]string, string, error) {
	modelToUse := resolveCodexCLIModelID(c.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyCodexModel].(string); ok && model != "" {
			modelToUse = resolveCodexCLIModelID(model)
		}
	}

	resumeSessionID := codexResumeSessionIDFromOptions(opts)
	appendResumeSessionID := func(args []string) []string {
		if resumeSessionID != "" {
			args = append(args, resumeSessionID)
		}
		return args
	}

	args := []string{"codex"}
	if resumeSessionID != "" {
		args = append(args, "resume")
	}
	args = appendCodexDisableUpdateArgs(args)
	args = append(args, "--no-alt-screen")
	if modelToUse != "" && modelToUse != "codex-cli" {
		args = append(args, "--model", modelToUse)
	}
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		approvalPolicy := ""
		mcpServersWithApproval := map[string]bool{}
		mcpServersNeedingApproval := map[string]bool{}
		disabledFeatures := map[string]bool{}
		// Interactive tmux mode applies MetadataKeyProjectDirID as the process cwd
		// in startCodexTmuxSession. Do not rely on Codex's --cd flag here; the
		// TUI header and all child behavior should see the caller-provided cwd.
		if sandbox, ok := opts.Metadata.Custom[MetadataKeySandbox].(string); ok && strings.TrimSpace(sandbox) != "" {
			args = append(args, "--sandbox", sandbox)
		}
		if profile, ok := opts.Metadata.Custom[MetadataKeyConfigProfile].(string); ok && strings.TrimSpace(profile) != "" {
			args = append(args, "--profile", profile)
		}
		if disable, ok := opts.Metadata.Custom[MetadataKeyDisableShellTool].(bool); ok && disable {
			args = appendCodexDisabledFeatureArgs(args, disabledFeatures, codexBridgeOnlyDisabledFeatures...)
		}
		if features, ok := opts.Metadata.Custom[MetadataKeyDisableFeatures].(string); ok && strings.TrimSpace(features) != "" {
			args = appendCodexDisabledFeatureArgs(args, disabledFeatures, strings.Split(features, ",")...)
		}
		if features, ok := opts.Metadata.Custom[MetadataKeyEnableFeatures].(string); ok && strings.TrimSpace(features) != "" {
			args = appendCodexFeatureCSV(args, "--enable", features)
		}
		if policy, ok := opts.Metadata.Custom[MetadataKeyApprovalPolicy].(string); ok && strings.TrimSpace(policy) != "" {
			policy = strings.TrimSpace(policy)
			approvalPolicy = policy
			args = append(args, "--ask-for-approval", policy)
			args = append(args, "-c", fmt.Sprintf("approval_policy=%q", policy))
		}
		if effort, ok := opts.Metadata.Custom[MetadataKeyReasoningEffort].(string); ok && strings.TrimSpace(effort) != "" {
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
		}
		if overrides, ok := opts.Metadata.Custom[MetadataKeyConfigOverrides].([]string); ok {
			for _, override := range overrides {
				if strings.TrimSpace(override) != "" {
					args = append(args, "-c", override)
					if serverName := codexMCPServerNameFromConfigOverride(override); serverName != "" {
						if strings.Contains(override, ".default_tools_approval_mode") || strings.Contains(override, ".tools.") && strings.Contains(override, ".approval_mode") {
							mcpServersWithApproval[serverName] = true
						} else {
							mcpServersNeedingApproval[serverName] = true
						}
					}
				}
			}
		}
		if approvalPolicy == "never" {
			for serverName := range mcpServersNeedingApproval {
				if !mcpServersWithApproval[serverName] {
					args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.default_tools_approval_mode=%q", serverName, "approve"))
				}
			}
		}
	}
	if strings.TrimSpace(systemPrompt) != "" {
		systemPromptTempFile, err := writeCodexInteractiveSystemPromptFile(systemPrompt)
		if err != nil {
			return nil, "", err
		}
		override, err := codexStringConfigOverride("model_instructions_file", systemPromptTempFile)
		if err != nil {
			_ = os.Remove(systemPromptTempFile)
			return nil, "", err
		}
		args = append(args, "-c", override)
		return appendResumeSessionID(args), systemPromptTempFile, nil
	}
	return appendResumeSessionID(args), "", nil
}

func releaseCodexInteractiveSession(session *codexInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	session.lastUsed = time.Now()
	session.idleTimer = time.AfterFunc(codexInteractiveIdleTimeout(), func() {
		closeCodexPersistentSession(session.ownerSessionID, "idle timeout", logger)
	})
	session.mu.Unlock()
}

func releaseCodexBoundedInteractiveSession(session *codexInteractiveSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	// Keep the real tmux pane alive for the shared bounded retention window so
	// the UI terminal remains inspectable/debuggable while it is visible.
	retention := llmtypes.TmuxKillDelay
	session.lastUsed = time.Now()
	if retention <= 0 {
		closeCodexSessionLocked(session, "bounded turn complete", logger)
		return
	}
	if logger != nil {
		logger.Debugf("Retaining completed Codex interactive session %s for owner %s for %s (then kill)", session.tmuxSessionName, session.ownerSessionID, retention)
	}
	session.idleTimer = time.AfterFunc(retention, func() {
		closeCodexPersistentSession(session.ownerSessionID, "bounded retention elapsed", logger)
	})
	session.mu.Unlock()
}

func closeCodexPersistentSession(ownerSessionID, reason string, logger interfaces.Logger) {
	codexPersistentRegistry.Lock()
	session := codexPersistentRegistry.sessions[ownerSessionID]
	if session == nil {
		codexPersistentRegistry.Unlock()
		return
	}
	delete(codexPersistentRegistry.sessions, ownerSessionID)
	codexPersistentRegistry.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Closing Codex interactive session %s for owner %s: %s", session.tmuxSessionName, ownerSessionID, reason)
	}
	closeCodexSessionLocked(session, reason, logger)
}

func closeCodexSessionLocked(session *codexInteractiveSession, reason string, logger interfaces.Logger) {
	if session == nil {
		return
	}
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	if logger != nil {
		logger.Debugf("Closing Codex interactive session %s for owner %s: %s", session.tmuxSessionName, session.ownerSessionID, reason)
	}
	removeCodexPersistentSession(session.ownerSessionID, session)
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = runCodexCommand(closeCtx, nil, "tmux", "send-keys", "-t", session.tmuxSessionName, "C-c")
	_ = killCodexTmuxSession(closeCtx, session.tmuxSessionName)
	if session.systemPromptTempFile != "" {
		_ = os.Remove(session.systemPromptTempFile)
		session.systemPromptTempFile = ""
	}
	if session.projectInstructionCleanup != nil {
		session.projectInstructionCleanup()
		session.projectInstructionCleanup = nil
	}
	unregisterCodexInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
}

// writeProjectInstructionFromOptions reads the opt-in feature flag for
// writing the per-session system prompt to AGENTS.md. Returns false on
// any malformed value so default behavior is unchanged.
func writeProjectInstructionFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile].(bool)
	return enabled
}

// writeCodexProjectAgentsFile writes the per-session system prompt to
// <workingDir>/AGENTS.md (codex's project-instructions convention). If
// a pre-existing AGENTS.md is present, its bytes are captured and the
// returned cleanup restores them on session teardown.
//
// Unlike claude code's .claude/rules/ subdirectory which lets us drop
// a unique session file alongside operator-owned content, AGENTS.md
// is a single conventional path. The byte-restore lifecycle keeps
// operator content safe across successful runs; a process crash
// between write and cleanup destroys the prior content (documented
// risk for the opt-in flag).
func writeCodexProjectAgentsFile(workingDir, systemPrompt string) (func(), error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure codex working dir: %w", err)
	}
	path := filepath.Join(workingDir, "AGENTS.md")
	previous, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read existing AGENTS.md: %w", readErr)
	}
	body := "<!-- mlp-session-instructions: orchestrator-generated per-session system prompt. Restored on cleanup. -->\n\n" + systemPrompt
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return nil, fmt.Errorf("write AGENTS.md: %w", err)
	}
	return func() {
		if existed {
			_ = os.WriteFile(path, previous, 0o600)
		} else {
			_ = os.Remove(path)
		}
	}, nil
}

func markCodexInteractiveSessionFailedLocked(session *codexInteractiveSession, err error, logger interfaces.Logger) {
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
		logger.Debugf("Discarding Codex interactive session %s for owner %s: %v", session.tmuxSessionName, session.ownerSessionID, err)
	}
}

func cleanupFailedCodexInteractiveSession(session *codexInteractiveSession) {
	if session == nil {
		return
	}
	removeCodexPersistentSession(session.ownerSessionID, session)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = killCodexTmuxSession(cleanupCtx, session.tmuxSessionName)
	unregisterCodexInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
	if session.systemPromptTempFile != "" {
		_ = os.Remove(session.systemPromptTempFile)
	}
}

func removeCodexPersistentSession(ownerSessionID string, session *codexInteractiveSession) {
	codexPersistentRegistry.Lock()
	defer codexPersistentRegistry.Unlock()
	if current := codexPersistentRegistry.sessions[ownerSessionID]; current == session {
		delete(codexPersistentRegistry.sessions, ownerSessionID)
	}
}

func CleanupCodexCLIInteractiveSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	codexPersistentRegistry.Lock()
	sessions := make([]*codexInteractiveSession, 0, len(codexPersistentRegistry.sessions))
	for _, session := range codexPersistentRegistry.sessions {
		sessions = append(sessions, session)
	}
	codexPersistentRegistry.sessions = map[string]*codexInteractiveSession{}
	codexPersistentRegistry.Unlock()

	var failures []string
	for _, session := range sessions {
		stopCodexIdleTimerIfAvailable(session)
		unregisterCodexInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		if session.systemPromptTempFile != "" {
			_ = os.Remove(session.systemPromptTempFile)
		}
		if err := killCodexTmuxSession(ctx, session.tmuxSessionName); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to clean up Codex interactive sessions: %s", strings.Join(failures, "; "))
	}
	return nil
}

func stopCodexIdleTimerIfAvailable(session *codexInteractiveSession) {
	if session == nil || !session.mu.TryLock() {
		return
	}
	defer session.mu.Unlock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
}

func registerCodexInteractiveSession(ownerSessionID, tmuxSessionName string) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if ownerSessionID == "" || tmuxSessionName == "" {
		return
	}
	codexInteractiveRegistry.Lock()
	defer codexInteractiveRegistry.Unlock()
	codexInteractiveRegistry.sessions[ownerSessionID] = tmuxSessionName
}

func unregisterCodexInteractiveSession(ownerSessionID, tmuxSessionName string) {
	codexInteractiveRegistry.Lock()
	defer codexInteractiveRegistry.Unlock()
	if current := codexInteractiveRegistry.sessions[ownerSessionID]; current == tmuxSessionName {
		delete(codexInteractiveRegistry.sessions, ownerSessionID)
	}
}

func activeCodexInteractiveSession(ownerSessionID string) (string, bool) {
	codexInteractiveRegistry.RLock()
	defer codexInteractiveRegistry.RUnlock()
	sessionName, ok := codexInteractiveRegistry.sessions[strings.TrimSpace(ownerSessionID)]
	return sessionName, ok && strings.TrimSpace(sessionName) != ""
}

func SendCodexInteractiveInput(ctx context.Context, ownerSessionID, message string) error {
	sessionName, ok := activeCodexInteractiveSession(ownerSessionID)
	if !ok {
		return fmt.Errorf("no active Codex interactive session registered for owner session %s", ownerSessionID)
	}
	return sendCodexInputToTmux(ctx, sessionName, message)
}

func codexInteractiveSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyInteractiveSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func codexPersistentInteractiveFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyPersistentInteractive].(bool)
	return ok && enabled
}

func codexResumeSessionIDFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		return strings.TrimSpace(sessionID)
	}
	return ""
}

func splitCodexSystemPrompt(messages []llmtypes.MessageContent) (string, []llmtypes.MessageContent) {
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

func buildCodexInteractivePrompt(messages []llmtypes.MessageContent) string {
	// Persistent tmux sessions keep prior turns in the native Codex TUI, so each
	// adapter call submits only the newest human message.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llmtypes.ChatMessageTypeHuman {
			return extractTextFromMessage(messages[i])
		}
	}
	return ""
}

func codexMCPServerNameFromConfigOverride(override string) string {
	override = strings.TrimSpace(override)
	if !strings.HasPrefix(override, "mcp_servers.") {
		return ""
	}
	rest := strings.TrimPrefix(override, "mcp_servers.")
	idx := strings.Index(rest, ".")
	if idx <= 0 {
		return ""
	}
	serverName := rest[:idx]
	for _, r := range serverName {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return serverName
}

func writeCodexInteractiveSystemPromptFile(systemPrompt string) (string, error) {
	tmpFile, err := os.CreateTemp("", "codex-interactive-system-*.md")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for Codex interactive system prompt: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.WriteString(systemPrompt); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write Codex interactive system prompt: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to close Codex interactive system prompt: %w", err)
	}
	return tmpPath, nil
}

func codexAssistantHistory(messages []llmtypes.MessageContent) []string {
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

func codexWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if dir, ok := opts.Metadata.Custom[MetadataKeyProjectDirID].(string); ok {
			if trimmed := strings.TrimSpace(dir); trimmed != "" {
				return trimmed
			}
		}
	}
	// Fallback to the process cwd. WITHOUT this, the sidecar
	// parsers (readCodexTranscriptUsage / readCodexTranscriptMessages)
	// get expectedWorkingDir="" and skip their session_meta.cwd
	// filter — which means they happily pick up the freshest
	// rollout from a parallel codex process (Codex Desktop / VS
	// Code Codex), leaking that other process's conversation +
	// tokens into this session. cursor's adapter has the same
	// fallback via cursorMustGetwd().
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func startCodexTmuxSession(ctx context.Context, sessionName string, args []string, workingDir string) error {
	shellCommand := codexInteractiveShellCommand(args, workingDir)
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runCodexCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start Codex interactive session %q: %w", sessionName, err)
	}
	_ = runCodexCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	return nil
}

func codexInteractiveShellCommand(args []string, workingDir string) string {
	return shelllaunch.Command(args, workingDir)
}

func waitForCodexPrompt(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) error {
	deadline, cancel := context.WithTimeout(ctx, codexInteractivePromptWait())
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	dismissedRateReminder := false
	dismissedTrustPrompt := false
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	streamTerminalScreen := codexInteractiveStreamTmuxScreenEnabled()
	for {
		select {
		case <-deadline.Done():
			captured, _ := captureCodexPane(context.Background(), sessionName)
			if strings.TrimSpace(captured) != "" {
				return fmt.Errorf("timed out waiting for Codex CLI prompt; %s", llmtypes.CompactTerminalPaneForError(sessionName, captured))
			}
			return fmt.Errorf("timed out waiting for Codex CLI prompt")
		case <-ticker.C:
			captured, err := captureCodexPane(deadline, sessionName)
			if err != nil {
				continue
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamCodexTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if hasCodexTrustPrompt(captured) {
				if !dismissedTrustPrompt {
					_ = dismissCodexTrustPrompt(deadline, sessionName, captured)
					dismissedTrustPrompt = true
				}
				continue
			}
			if hasCodexRateLimitReminderModal(captured) {
				if !dismissedRateReminder {
					_ = dismissCodexRateLimitReminder(deadline, sessionName, captured)
					dismissedRateReminder = true
				}
				continue
			}
			if hasCodexReadyPrompt(captured) {
				return nil
			}
		}
	}
}

func sendCodexPromptToTmux(ctx context.Context, sessionName, prompt string) error {
	return sendCodexInputToTmux(ctx, sessionName, prompt)
}

func sendCodexInputToTmux(ctx context.Context, sessionName, message string) error {
	message = strings.TrimRight(message, "\r\n")
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("Codex interactive input is empty")
	}
	bufferName := "mlp-codex-input-" + codexRandomHex(6)
	tmp, err := os.CreateTemp("", "codex-tmux-input-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create Codex tmux input temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(message); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write Codex tmux input temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close Codex tmux input temp file: %w", err)
	}
	if err := runCodexCommand(ctx, nil, "tmux", "load-buffer", "-b", bufferName, tmpPath); err != nil {
		return fmt.Errorf("failed to load Codex input into tmux buffer: %w", err)
	}
	if err := runCodexCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-p", "-r", "-b", bufferName, "-t", sessionName); err != nil {
		return fmt.Errorf("failed to paste input into Codex interactive session: %w", err)
	}
	if err := runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-m"); err != nil {
		return fmt.Errorf("failed to submit input to Codex interactive session: %w", err)
	}
	return nil
}

func waitForCodexInteractiveResponse(ctx context.Context, sessionName, baseline string, streamChan chan<- llmtypes.StreamChunk) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var sawActivity bool
	var idleSince time.Time
	var lastCaptured string
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	var dismissedRateReminder bool
	streamTerminalScreen := codexInteractiveStreamTmuxScreenEnabled()
	for {
		select {
		case <-ctx.Done():
			captured, _ := captureCodexPane(context.Background(), sessionName)
			return captured, ctx.Err()
		case <-ticker.C:
			captured, err := captureCodexPane(ctx, sessionName)
			if err != nil {
				return "", err
			}
			delta := codexCapturedAfterBaseline(captured, baseline)
			if tmuxcontrol.ConsumeForceComplete(sessionName) {
				return captured, tmuxcontrol.ErrForceComplete
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamCodexTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			if hasCodexQueuedInput(captured) {
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if hasCodexRateLimitReminderModal(captured) {
				if !dismissedRateReminder {
					_ = dismissCodexRateLimitReminder(ctx, sessionName, captured)
					dismissedRateReminder = true
				}
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if hasCodexActivity(captured) {
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if strings.TrimSpace(delta) != "" {
				sawActivity = true
			}
			if !sawActivity || !hasCodexReadyPrompt(captured) {
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
			if time.Since(idleSince) >= codexInteractiveStableWindow {
				return captured, nil
			}
		}
	}
}

func parseCodexInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
	if hasCodexTrustPrompt(captured) {
		return ""
	}
	delta := codexCapturedAfterBaseline(captured, baseline)
	text := rawFramedCodexAnswer(delta)
	if strings.TrimSpace(text) == "" {
		return parseCodexInteractiveResponseSegmentFallback(delta, echoedUserPrompt, historicalAssistantTexts)
	}
	text = stripCodexEchoedUserPrompt(text, echoedUserPrompt)
	text = stripCodexHistoricalAssistantText(text, historicalAssistantTexts)
	if strings.TrimSpace(text) == "" {
		text = codexTerminalTailTextFallback(normalizeCodexPaneSnapshot(delta).Segments, 24)
		text = stripCodexEchoedUserPrompt(text, echoedUserPrompt)
		text = stripCodexHistoricalAssistantText(text, historicalAssistantTexts)
	}
	if isCodexLikelyQueuedUserEcho(text) {
		return ""
	}
	return strings.TrimSpace(text)
}

func forcedCodexInteractiveResponse(captured, baseline, echoedUserPrompt string, historicalAssistantTexts []string) string {
	delta := codexCapturedAfterBaseline(captured, baseline)
	text := parseCodexInteractiveResponseSegmentFallback(delta, echoedUserPrompt, historicalAssistantTexts)
	if strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	text = stripCodexEchoedUserPrompt(stripCodexANSI(delta), echoedUserPrompt)
	text = stripCodexHistoricalAssistantText(text, historicalAssistantTexts)
	return strings.TrimSpace(text)
}

func codexPolicyInvalidPromptError(captured string) error {
	if hasCodexPolicyInvalidPrompt(captured) {
		return errors.New("Codex CLI rejected the prompt as policy invalid")
	}
	return nil
}

func codexPolicyInvalidPromptTextError(text string) error {
	if hasCodexPolicyInvalidPromptText(text) {
		return errors.New("Codex CLI rejected the prompt as policy invalid")
	}
	return nil
}

func hasCodexPolicyInvalidPrompt(captured string) bool {
	return hasCodexPolicyInvalidPromptText(stripCodexANSI(captured))
}

func hasCodexPolicyInvalidPromptText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "invalid prompt") &&
		(strings.Contains(lower, "usage policy") || strings.Contains(lower, "violating our usage policy")) {
		return true
	}
	if strings.Contains(lower, "potentially violating our usage policy") {
		return true
	}
	// Codex sometimes leaves only the tail URL visible/extracted after the
	// invalid-prompt banner scrolls. Treat a short answer containing only that
	// docs pointer as the same fatal provider rejection, not a real answer.
	return strings.Contains(lower, "platform.openai.com/docs/guides/reasoning#advice-on-prompting") &&
		len([]rune(lower)) < 260
}

func rawFramedCodexAnswer(delta string) string {
	lines := normalizeCodexPaneLines(delta)
	ruleIndices := make([]int, 0, 4)
	for i, line := range lines {
		if isCodexHorizontalRuleLine(line) {
			ruleIndices = append(ruleIndices, i)
		}
	}
	if len(ruleIndices) >= 2 {
		for i := len(ruleIndices) - 1; i > 0; i-- {
			text := cleanCodexRawFinalLines(lines[ruleIndices[i-1]+1:ruleIndices[i]], false)
			if strings.TrimSpace(text) != "" && !isCodexLikelyToolReplayFinalText(text) {
				return text
			}
		}
	}
	if len(ruleIndices) == 1 {
		text := cleanCodexRawFinalLines(lines[ruleIndices[0]+1:], true)
		if isCodexLikelyToolReplayFinalText(text) {
			return ""
		}
		return text
	}
	return ""
}

func isCodexLikelyToolReplayFinalText(text string) bool {
	lines := nonEmptyCodexLines(text)
	if len(lines) == 0 {
		return true
	}
	if isCodexAssistantAnswerPathSegment(lines) {
		return false
	}
	toolish := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if isCodexToolReplayLine(trimmed) ||
			isCodexToolReplayContinuationLine(trimmed) ||
			isCodexShellScriptContinuationLine(trimmed) ||
			isCodexJSONToolOutputLine(trimmed) ||
			strings.Contains(lower, `,"timeout":`) ||
			strings.Contains(lower, `"timeout":`) && strings.Contains(lower, "})") ||
			strings.Contains(lower, "py\",\"timeout") ||
			strings.Contains(lower, "indent=2))") {
			toolish++
		}
	}
	return toolish == len(lines)
}

func cleanCodexRawFinalLines(lines []string, stopAtPrompt bool) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isCodexHorizontalRuleLine(trimmed) {
			continue
		}
		if stopAtPrompt && isCodexPromptBoundaryLine(trimmed) {
			break
		}
		out = append(out, trimmed)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isCodexPromptBoundaryLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return trimmed == "›" ||
		trimmed == ">" ||
		trimmed == "❯" ||
		strings.HasPrefix(trimmed, "› ") ||
		strings.HasPrefix(lower, "gpt-") && strings.Contains(lower, "·") ||
		strings.Contains(lower, "openai codex")
}

func parseCodexInteractiveResponseSegmentFallback(delta, echoedUserPrompt string, historicalAssistantTexts []string) string {
	snapshot := normalizeCodexPaneSnapshot(delta)
	text := framedAssistantTextFromCodexSegments(snapshot.Segments)
	if strings.TrimSpace(text) == "" {
		text = finalAssistantTextFromCodexSegments(snapshot.Segments)
	}
	if strings.TrimSpace(text) == "" {
		text = snapshot.AssistantText
	}
	if strings.TrimSpace(text) == "" {
		text = codexTerminalTailTextFallback(snapshot.Segments, 24)
	}
	text = stripCodexEchoedUserPrompt(text, echoedUserPrompt)
	text = stripCodexHistoricalAssistantText(text, historicalAssistantTexts)
	if isCodexLikelyQueuedUserEcho(text) {
		return ""
	}
	return strings.TrimSpace(text)
}

func codexTerminalTailTextFallback(segments []codexSegment, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 24
	}
	lines := make([]string, 0, maxLines)
	for _, segment := range segments {
		if segment.Kind != codexSegmentAssistant {
			continue
		}
		for _, line := range segment.Lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || isCodexPromptBoundaryLine(trimmed) || isCodexTUILine(trimmed) {
				continue
			}
			lines = append(lines, trimmed)
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isCodexLikelyQueuedUserEcho(text string) bool {
	lines := nonEmptyCodexLines(text)
	if len(lines) == 0 {
		return false
	}
	lower := strings.ToLower(strings.Join(lines, "\n"))
	if strings.Contains(lower, "pre-validation failed") &&
		strings.Contains(lower, "checks:") &&
		(strings.Contains(lower, "fix the specific issues") ||
			strings.Contains(lower, "validation failed") ||
			strings.Contains(lower, "must exist")) {
		return true
	}
	return false
}

func extractCodexVisibleAssistantText(delta string) string {
	return normalizeCodexPaneSnapshot(delta).AssistantText
}

type codexSegmentKind string

const (
	codexSegmentAssistant   codexSegmentKind = "assistant_text"
	codexSegmentToolStatus  codexSegmentKind = "tool_status"
	codexSegmentPlanStatus  codexSegmentKind = "plan_status"
	codexSegmentChrome      codexSegmentKind = "terminal_chrome"
	codexSegmentInputEcho   codexSegmentKind = "input_echo"
	codexSegmentErrorStatus codexSegmentKind = "error_status"
)

type codexSegment struct {
	Kind  codexSegmentKind
	Lines []string
}

type codexNormalizedSnapshot struct {
	AssistantText string
	Fingerprint   string
	Segments      []codexSegment
}

func normalizeCodexPaneSnapshot(raw string) codexNormalizedSnapshot {
	lines := normalizeCodexPaneLines(raw)
	segments := segmentCodexLines(lines)
	assistantText := assistantTextFromCodexSegments(segments)
	return codexNormalizedSnapshot{
		AssistantText: assistantText,
		Fingerprint:   codexSnapshotFingerprint(assistantText),
		Segments:      segments,
	}
}

func normalizeCodexPaneLines(raw string) []string {
	raw = normalizeCodexInlineTUIBoundaries(raw)
	raw = expandCodexEscapedToolNewlines(raw)
	rawLines := strings.Split(raw, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		normalized := normalizeCodexPaneLine(line)
		for _, splitLine := range splitCodexWorkingStatusPrefix(normalized) {
			if splitLine != "" && !isCodexBulletOnlyLine(splitLine) {
				lines = append(lines, splitLine)
			}
		}
	}
	return lines
}

func expandCodexEscapedToolNewlines(raw string) string {
	parts := strings.Split(raw, `\n`)
	if len(parts) == 1 {
		return raw
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for _, part := range parts[1:] {
		if isCodexEscapedToolNewlinePrefix(part) {
			b.WriteByte('\n')
		} else {
			b.WriteString(`\n`)
		}
		b.WriteString(part)
	}
	return b.String()
}

func isCodexEscapedToolNewlinePrefix(s string) bool {
	trimmed := strings.TrimSpace(s)
	return strings.HasPrefix(trimmed, "total ") ||
		codexShellListingPattern.MatchString(trimmed) ||
		strings.HasPrefix(trimmed, "./") ||
		strings.HasPrefix(trimmed, `"stdout"`) ||
		strings.HasPrefix(trimmed, `"stderr"`) ||
		strings.HasPrefix(trimmed, `"exit_code"`) ||
		strings.HasPrefix(trimmed, `"execution_time_ms"`) ||
		strings.Contains(trimmed, " &&") ||
		strings.Contains(trimmed, " |")
}

func normalizeCodexPaneLine(line string) string {
	line = strings.TrimSpace(stripCodexANSI(line))
	line = strings.TrimSpace(strings.TrimPrefix(line, "• "))
	return line
}

var codexWorkingStatusPrefixPattern = regexp.MustCompile(`^(Working \([0-9]+s\)?)\s+(.+)$`)

func splitCodexWorkingStatusPrefix(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	matches := codexWorkingStatusPrefixPattern.FindStringSubmatch(line)
	if len(matches) != 3 {
		return []string{line}
	}
	return []string{strings.TrimSpace(matches[1]), strings.TrimSpace(matches[2])}
}

func segmentCodexLines(lines []string) []codexSegment {
	segments := make([]codexSegment, 0, len(lines))
	current := codexSegment{}
	flush := func() {
		if len(current.Lines) > 0 {
			segments = append(segments, current)
		}
		current = codexSegment{}
	}

	for i, line := range lines {
		nextLine := ""
		if i+1 < len(lines) {
			nextLine = lines[i+1]
		}
		if current.Kind == codexSegmentToolStatus &&
			(isCodexToolContinuationLine(line) || isCodexContextualToolContinuationLine(current.Lines, line, nextLine)) {
			current.Lines = append(current.Lines, line)
			continue
		}
		if current.Kind == codexSegmentPlanStatus && isCodexPlanContinuationLine(line) {
			current.Lines = append(current.Lines, line)
			continue
		}
		if current.Kind == codexSegmentAssistant && isCodexAssistantAnswerContinuationLine(current.Lines, line) {
			current.Lines = append(current.Lines, line)
			continue
		}

		kind := classifyCodexLine(line)
		if len(current.Lines) == 0 {
			current = codexSegment{Kind: kind, Lines: []string{line}}
			continue
		}
		if current.Kind != kind {
			flush()
			current = codexSegment{Kind: kind, Lines: []string{line}}
			continue
		}
		current.Lines = append(current.Lines, line)
	}
	flush()
	return segments
}

func classifyCodexLine(line string) codexSegmentKind {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	switch {
	case isCodexInputEchoLine(trimmed), isCodexQueuedInputLine(trimmed):
		return codexSegmentInputEcho
	case strings.HasPrefix(lower, "updated plan"):
		return codexSegmentPlanStatus
	case isCodexToolStatusLine(trimmed),
		isCodexToolReplayLine(trimmed),
		strings.HasPrefix(lower, "searching the web"),
		strings.HasPrefix(lower, "searched http"),
		strings.HasPrefix(lower, "spawned "),
		strings.HasPrefix(lower, "waiting for "),
		strings.HasPrefix(lower, "finished waiting"),
		strings.HasPrefix(lower, "working ("),
		strings.HasPrefix(trimmed, "└"),
		strings.HasPrefix(trimmed, "├"),
		strings.HasPrefix(trimmed, "│"):
		return codexSegmentToolStatus
	case strings.HasPrefix(trimmed, "✔"),
		strings.HasPrefix(trimmed, "✓"),
		strings.HasPrefix(trimmed, "□"),
		strings.HasPrefix(trimmed, "☐"):
		return codexSegmentPlanStatus
	case isCodexErrorStatusLine(trimmed):
		return codexSegmentErrorStatus
	case isCodexTUILine(trimmed):
		return codexSegmentChrome
	default:
		return codexSegmentAssistant
	}
}

func assistantTextFromCodexSegments(segments []codexSegment) string {
	out := make([]string, 0, len(segments))
	for i, segment := range segments {
		if segment.Kind == codexSegmentAssistant {
			var prevKind, nextKind codexSegmentKind
			if i > 0 {
				prevKind = segments[i-1].Kind
			}
			if i+1 < len(segments) {
				nextKind = segments[i+1].Kind
			}
			if isCodexLikelyToolReplayAssistantSegment(segment.Lines, prevKind, nextKind) {
				continue
			}
			out = append(out, segment.Lines...)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func finalAssistantTextFromCodexSegments(segments []codexSegment) string {
	for i := len(segments) - 1; i >= 0; i-- {
		segment := segments[i]
		if segment.Kind != codexSegmentAssistant {
			continue
		}
		var prevKind, nextKind codexSegmentKind
		if i > 0 {
			prevKind = segments[i-1].Kind
		}
		if i+1 < len(segments) {
			nextKind = segments[i+1].Kind
		}
		if isCodexLikelyToolReplayAssistantSegment(segment.Lines, prevKind, nextKind) {
			continue
		}
		return strings.TrimSpace(strings.Join(segment.Lines, "\n"))
	}
	return ""
}

func framedAssistantTextFromCodexSegments(segments []codexSegment) string {
	for i := len(segments) - 1; i >= 0; i-- {
		segment := segments[i]
		if segment.Kind != codexSegmentAssistant {
			continue
		}
		var prev, next codexSegment
		var prevKind, nextKind codexSegmentKind
		if i > 0 {
			prev = segments[i-1]
			prevKind = prev.Kind
		}
		if i+1 < len(segments) {
			next = segments[i+1]
			nextKind = next.Kind
		}
		if isCodexLikelyToolReplayAssistantSegment(segment.Lines, prevKind, nextKind) {
			continue
		}
		if isCodexHorizontalRuleSegment(prev) && isCodexHorizontalRuleSegment(next) {
			return strings.TrimSpace(strings.Join(segment.Lines, "\n"))
		}
	}
	return ""
}

func isCodexHorizontalRuleSegment(segment codexSegment) bool {
	if segment.Kind != codexSegmentChrome {
		return false
	}
	for _, line := range segment.Lines {
		if isCodexHorizontalRuleLine(line) {
			return true
		}
	}
	return false
}

func isCodexHorizontalRuleLine(line string) bool {
	trimmed := strings.TrimSpace(stripCodexANSI(line))
	if trimmed == "" {
		return false
	}
	dashCount := 0
	otherCount := 0
	for _, r := range trimmed {
		switch r {
		case '─', '━', '╌', '╍':
			dashCount++
		case ' ':
		default:
			otherCount++
		}
	}
	return dashCount >= 20 && otherCount == 0
}

func codexSnapshotFingerprint(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

func isCodexToolContinuationLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "└") ||
		strings.HasPrefix(trimmed, "├") ||
		strings.HasPrefix(trimmed, "│") ||
		isCodexToolReplayContinuationLine(trimmed) ||
		isCodexErrorStatusLine(trimmed)
}

func isCodexPlanContinuationLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "└") ||
		strings.HasPrefix(trimmed, "├") ||
		strings.HasPrefix(trimmed, "│") ||
		strings.HasPrefix(trimmed, "✔") ||
		strings.HasPrefix(trimmed, "✓") ||
		strings.HasPrefix(trimmed, "□") ||
		strings.HasPrefix(trimmed, "☐")
}

func isCodexErrorStatusLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "error: resources/") ||
		strings.HasPrefix(lower, "error: tools/") ||
		strings.HasPrefix(lower, "error: ") && strings.Contains(lower, "mcp server")
}

func isCodexInputEchoLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "sent immediately") ||
		trimmed == "immediately)" ||
		strings.HasPrefix(trimmed, "↳ ")
}

func isCodexQueuedInputLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(stripCodexANSI(line)))
	return strings.Contains(lower, "messages to be submitted after next tool call") ||
		strings.Contains(lower, "press esc to interrupt and send immediately")
}

var codexInlineTUIBoundaryPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(^|\s+)(Working \([0-9]+s\)?)`),
	regexp.MustCompile(`\s+•+\s*`),
	regexp.MustCompile(`\s+(Called|Calling) [A-Za-z0-9_-]+[\w.\-]*(?:\(|\{|\s|$)`),
	regexp.MustCompile(`\s+(Updated Plan|Searching the web|Searched https?://|Spawned [A-Z][^ ]*|Waiting for [A-Z][^ ]*|Finished waiting)(?:\s|$)`),
	regexp.MustCompile(`\s+(Error: (resources|tools)/[^ ]+ failed:|Error: .*unknown MCP server)`),
	regexp.MustCompile(`\s+([└├│✔✓□☐])\s+`),
}

func normalizeCodexInlineTUIBoundaries(text string) string {
	out := text
	for _, pattern := range codexInlineTUIBoundaryPatterns {
		out = pattern.ReplaceAllStringFunc(out, func(match string) string {
			return "\n" + strings.TrimLeft(match, " \t\r\n")
		})
	}
	return out
}

func isCodexTUILine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if isCodexBulletOnlyLine(trimmed) {
		return true
	}
	if isCodexTrustPromptLine(trimmed) {
		return true
	}
	if isCodexRateLimitReminderLine(trimmed) {
		return true
	}
	if line == "›" || line == ">" || line == "❯" || strings.HasPrefix(line, "› ") ||
		strings.EqualFold(trimmed, "Codex") ||
		(strings.Contains(lower, "codex") && strings.ContainsAny(line, "▐▛▜▝▘▌█")) {
		return true
	}
	if strings.Contains(lower, "esc to interrupt") ||
		strings.Contains(lower, "ctrl+c") ||
		strings.Contains(lower, "ctrl+l is disabled") ||
		strings.Contains(lower, "ctrl+o") ||
		strings.Contains(lower, "pasted text") ||
		strings.Contains(lower, "conversation compacted") ||
		strings.Contains(lower, "openai codex") ||
		strings.Contains(lower, "chatgpt.com/codex") ||
		strings.Contains(lower, "app-landing-page=true") ||
		strings.Contains(lower, "shift+tab") ||
		strings.HasPrefix(lower, "tip:") ||
		isCodexToolStatusLine(trimmed) ||
		strings.HasPrefix(lower, "updated plan") ||
		strings.HasPrefix(lower, "searching the web") ||
		strings.HasPrefix(lower, "searched http") ||
		strings.HasPrefix(lower, "spawned ") ||
		strings.HasPrefix(lower, "waiting for ") ||
		strings.HasPrefix(lower, "finished waiting") ||
		strings.HasPrefix(lower, "working (") ||
		strings.Contains(lower, "directory:") ||
		strings.HasPrefix(lower, "field ") ||
		strings.HasPrefix(lower, "allow the ") && strings.Contains(lower, "mcp server") ||
		strings.HasPrefix(lower, "enter to submit") ||
		strings.Contains(lower, "run the tool and remember this choice") ||
		strings.Contains(lower, "calling api-bridge") ||
		strings.Contains(lower, "called api-bridge") ||
		isCodexQueuedInputLine(trimmed) ||
		strings.Contains(lower, "sent immediately") ||
		trimmed == "immediately)" ||
		strings.HasPrefix(trimmed, "↳ ") ||
		strings.Contains(lower, "tmux focus-events") ||
		strings.Contains(lower, "codex") && strings.Contains(lower, "model") ||
		strings.HasPrefix(lower, "gpt-") && strings.Contains(lower, "·") ||
		isCodexTokenStatusLine(trimmed) ||
		isCodexActiveStatusLine(line) {
		return true
	}
	return isCodexBoxDrawingLine(line)
}

func isCodexTokenStatusLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.Contains(lower, "↑") && strings.Contains(lower, "tokens") ||
		strings.Contains(lower, "↓") && strings.Contains(lower, "tokens") ||
		strings.Contains(lower, "thinking with") && strings.Contains(lower, "tokens") ||
		strings.Contains(lower, "thought for") && strings.Contains(lower, "tokens")
}

func isCodexBulletOnlyLine(line string) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		if r == '•' || r == ' ' || r == '\t' {
			continue
		}
		return false
	}
	return true
}

func isCodexToolStatusLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "• "))
	lower := strings.ToLower(trimmed)
	if trimmed == "Called" || trimmed == "Calling" {
		return true
	}
	if strings.HasPrefix(lower, "called ") || strings.HasPrefix(lower, "calling ") {
		rest := strings.TrimSpace(trimmed[strings.Index(trimmed, " ")+1:])
		return strings.Contains(rest, "(") ||
			strings.Contains(rest, ".") ||
			strings.Contains(rest, "_") ||
			strings.Contains(strings.ToLower(rest), "api-bridge")
	}
	return false
}

var (
	codexToolJSONReplayPattern = regexp.MustCompile(`(^|[{\s,])"(stdout|stderr|exit_code|execution_time_ms)"\s*:`)
	codexShellListingPattern   = regexp.MustCompile(`^(total [0-9]+|[-dlcbsp][rwx-]{9}[@+]? +[0-9]+ +\S+)`)
	codexTruncatedLSLine       = regexp.MustCompile(`^[0-9]+ +[A-Za-z0-9_.-]+.*\.\.\.$`)
	codexTruncatedPathLine     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]*\.\.\.$`)
	codexToolCommandPattern    = regexp.MustCompile(`(?i)(\$MCP_API_URL|https?://127\.0\.0\.1:[0-9]+/s/|/tools/(custom|mcp)/|authorization: bearer|content-type: application/json|curl -sS|requests\.post|json\.dumps|headers=\{|base='?https?://|python3 - <<|cat <<json|payload=|for m in |mkdir -p |touch [^ ]|ls -l|jq |\.read_mcp_resource\(|\.list_mcp_resources\(|\.list_mcp_resource_templates\(|\.get_api_spec\(|bridge\.get_api_spec\()`)
	codexBareNumberPattern     = regexp.MustCompile(`^[0-9]+$`)
	codexShortSentencePattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,24}\.$`)
)

func isCodexToolReplayLine(line string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "• "))
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if codexToolJSONReplayPattern.MatchString(trimmed) ||
		codexToolCommandPattern.MatchString(trimmed) ||
		codexShellListingPattern.MatchString(trimmed) ||
		codexTruncatedLSLine.MatchString(trimmed) ||
		codexTruncatedPathLine.MatchString(trimmed) {
		return true
	}
	if strings.HasPrefix(trimmed, "# List supported and currently usable") {
		return true
	}
	if strings.HasPrefix(lower, "base: http://") ||
		strings.HasPrefix(lower, "auth: bearer") ||
		strings.HasPrefix(lower, "post /tools/") {
		return true
	}
	if strings.Contains(lower, "/api/llm-config/models/metadata") ||
		strings.Contains(lower, "provider: string (required)") ||
		strings.Contains(lower, "catalog the frontend uses") {
		return true
	}
	if strings.Contains(lower, "workspace-relative output_path") ||
		strings.Contains(lower, "relative output_path") ||
		strings.Contains(lower, "generated image files should be stored") ||
		strings.Contains(lower, "supports optional provider override") ||
		strings.Contains(lower, "aspect ratio, resolution") ||
		strings.Contains(lower, "aspect_ratio") ||
		strings.Contains(lower, "number_of_images") {
		return true
	}
	if strings.Contains(lower, "custom tool execution failed") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "permissionerror") ||
		strings.Contains(lower, "this step's allowed folders") ||
		strings.Contains(lower, "allowed folders") ||
		strings.Contains(lower, "cannot read from") ||
		strings.Contains(lower, "underlying check") ||
		strings.Contains(lower, "absolute host path") ||
		strings.Contains(lower, "allowed workspace roots") ||
		strings.Contains(lower, "writable folders") ||
		strings.Contains(lower, "did you mean:") {
		return true
	}
	if strings.Contains(lower, "unsupported image generation model") ||
		strings.Contains(lower, "supported image providers") ||
		strings.Contains(lower, "set_provider_auth(provider=") ||
		strings.Contains(lower, "output_path directory is outside") ||
		strings.Contains(lower, "prompt is required") {
		return true
	}
	if strings.HasPrefix(lower, "echo \"generating with") ||
		strings.HasPrefix(lower, "generating with model:") ||
		strings.HasPrefix(lower, "trying model=") {
		return true
	}
	if strings.HasSuffix(trimmed, `"})`) && (strings.Contains(trimmed, "&&") ||
		strings.Contains(trimmed, "{") ||
		strings.Contains(trimmed, "}") ||
		strings.Contains(trimmed, "PY")) {
		return true
	}
	if strings.Contains(lower, "indent=2))") ||
		strings.Contains(lower, "py\",\"timeout") ||
		strings.Contains(lower, `"timeout":`) && strings.Contains(lower, "})") {
		return true
	}
	if isCodexLikelyWrappedToolOutputLine(trimmed) {
		return true
	}
	return false
}

func isCodexToolReplayContinuationLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if isCodexToolReplayLine(trimmed) || codexShellListingPattern.MatchString(trimmed) {
		return true
	}
	if strings.HasPrefix(trimmed, "...") ||
		strings.HasPrefix(trimmed, `", "stderr"`) ||
		strings.HasPrefix(trimmed, `"stderr"`) ||
		strings.HasPrefix(trimmed, `"exit_code"`) ||
		strings.HasPrefix(trimmed, `"execution_time_ms"`) ||
		isCodexJSONToolOutputLine(trimmed) ||
		isCodexShellScriptContinuationLine(trimmed) {
		return true
	}
	if strings.Contains(lower, "exit_code") && strings.Contains(lower, "execution_time_ms") {
		return true
	}
	return false
}

func isCodexContextualToolContinuationLine(currentLines []string, line, nextLine string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if isCodexToolReplaySegment(currentLines) && !isCodexAssistantAnswerStartLine(trimmed) {
		return isCodexLikelyWrappedToolOutputLine(trimmed) ||
			isCodexToolReplayContinuationLine(trimmed) ||
			isCodexToolReplayLine(trimmed)
	}
	if codexBareNumberPattern.MatchString(trimmed) && codexShellListingPattern.MatchString(strings.TrimSpace(nextLine)) {
		return true
	}
	if isCodexAPISpecToolSegment(currentLines) {
		lower := strings.ToLower(trimmed)
		return strings.HasPrefix(trimmed, "# ") ||
			strings.Contains(lower, "supported and currently usable") ||
			strings.Contains(lower, "requires a workspace-") ||
			strings.Contains(lower, "relative output_path") ||
			strings.Contains(lower, "frontend uses") ||
			strings.Contains(lower, "provider: string") ||
			strings.Contains(lower, "provider/model") ||
			strings.Contains(lower, "capability")
	}
	if isCodexShellScriptSegment(currentLines) && isCodexShellScriptContinuationLine(trimmed) {
		return true
	}
	if isCodexJSONToolOutputSegment(currentLines) && isCodexJSONToolOutputLine(trimmed) {
		return true
	}
	if codexShortSentencePattern.MatchString(trimmed) && isCodexPlanMarkerLine(nextLine) {
		return true
	}
	return false
}

func isCodexToolReplaySegment(lines []string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isCodexToolReplayLine(trimmed) ||
			isCodexToolReplayContinuationLine(trimmed) ||
			codexShellListingPattern.MatchString(trimmed) {
			return true
		}
	}
	return false
}

func isCodexAssistantAnswerStartLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "here's ") ||
		strings.HasPrefix(lower, "here’s ") ||
		strings.HasPrefix(lower, "here are ") ||
		strings.HasPrefix(lower, "here is ") ||
		strings.HasPrefix(lower, "the current ") ||
		strings.HasPrefix(lower, "i found ") ||
		strings.HasPrefix(lower, "i can ") ||
		strings.HasPrefix(lower, "done") ||
		strings.HasPrefix(lower, "all set")
}

func isCodexAssistantAnswerContinuationLine(currentLines []string, line string) bool {
	if len(currentLines) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if !codexAssistantLinesIntroducePath(currentLines) {
		return false
	}
	return isCodexAssistantPathLine(trimmed) ||
		strings.HasPrefix(lower, "(equivalent relative path") ||
		strings.HasPrefix(lower, "equivalent relative path") ||
		strings.HasPrefix(lower, "relative path") ||
		strings.Contains(lower, "workspace root:")
}

func codexAssistantAnswerIntroducesPath(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.Contains(lower, "saved at") ||
		strings.Contains(lower, "stored at") ||
		strings.Contains(lower, "written to") ||
		strings.Contains(lower, "generated at") ||
		strings.Contains(lower, "files/folders in") ||
		strings.Contains(lower, "analyzed this image") ||
		strings.Contains(lower, "analysed this image") ||
		strings.Contains(lower, "read this image") ||
		strings.Contains(lower, "selected image") ||
		strings.Contains(lower, "image:")
}

func codexAssistantLinesIntroducePath(lines []string) bool {
	for _, line := range lines {
		if codexAssistantAnswerIntroducesPath(line) {
			return true
		}
	}
	return false
}

func isCodexAssistantAnswerPathSegment(lines []string) bool {
	if len(lines) == 0 || !codexAssistantLinesIntroducePath(lines) {
		return false
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if isCodexAssistantPathLine(trimmed) ||
			strings.HasPrefix(lower, "(equivalent relative path") ||
			strings.HasPrefix(lower, "equivalent relative path") ||
			strings.HasPrefix(lower, "relative path") ||
			strings.Contains(lower, "workspace root:") {
			return true
		}
	}
	return false
}

func isCodexAssistantPathLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(trimmed, "/") ||
		strings.HasPrefix(trimmed, "~/") ||
		strings.HasPrefix(trimmed, "_users/") ||
		strings.HasPrefix(trimmed, "chats/") ||
		strings.Contains(lower, "/workspace-docs/") ||
		strings.Contains(lower, "_users/default/chats/")
}

var codexWrappedLSDatePattern = regexp.MustCompile(`\b[0-9]{1,2} [A-Za-z]{3} ([0-9]{2}:[0-9]{2}|[0-9]{4})\b`)

func isCodexLikelyWrappedToolOutputLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "./") ||
		strings.HasPrefix(trimmed, "../") ||
		codexTruncatedPathLine.MatchString(trimmed) ||
		strings.HasPrefix(trimmed, "&& ") ||
		strings.Contains(trimmed, "&& find ") ||
		strings.Contains(trimmed, "find . -maxdepth") ||
		strings.Contains(trimmed, "2>/dev/null") ||
		strings.Contains(trimmed, "| sort") ||
		strings.Contains(trimmed, "| head") ||
		strings.Contains(trimmed, "ls -la") {
		return true
	}
	if strings.Contains(lower, "default/chat_history/") ||
		strings.Contains(lower, "default/chats/") ||
		strings.Contains(lower, "cannot read from") ||
		strings.Contains(lower, "allowed folders") ||
		strings.Contains(lower, "writable folders") ||
		strings.Contains(lower, "underlying check") ||
		strings.Contains(lower, "operation not permitted") {
		return true
	}
	if strings.Contains(lower, " staff ") && codexWrappedLSDatePattern.MatchString(trimmed) {
		return true
	}
	return false
}

func isCodexAPISpecToolSegment(lines []string) bool {
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(lower, "get_api_spec(") ||
			strings.HasPrefix(lower, "base: http://") ||
			strings.HasPrefix(lower, "post /tools/") {
			return true
		}
	}
	return false
}

func isCodexPlanMarkerLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "✔") ||
		strings.HasPrefix(trimmed, "✓") ||
		strings.HasPrefix(trimmed, "□") ||
		strings.HasPrefix(trimmed, "☐")
}

func isCodexShellScriptSegment(lines []string) bool {
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(lower, "curl -ss") ||
			strings.Contains(lower, "payload=") ||
			strings.Contains(lower, "cat <<json") ||
			strings.Contains(lower, "for m in ") {
			return true
		}
	}
	return false
}

func isCodexShellScriptContinuationLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "payload=") ||
		strings.HasPrefix(lower, "echo ") ||
		strings.HasPrefix(lower, "done") ||
		strings.HasPrefix(lower, "json") ||
		strings.HasPrefix(lower, "try:") ||
		strings.HasPrefix(lower, "except ") ||
		strings.HasPrefix(lower, "print(") ||
		strings.HasPrefix(lower, "r=requests.post") ||
		strings.HasPrefix(lower, "for p in ") ||
		strings.HasPrefix(lower, "for m in ") ||
		strings.HasPrefix(lower, "-h ") ||
		strings.HasPrefix(lower, "-d ") ||
		strings.HasPrefix(trimmed, "{") ||
		strings.HasPrefix(trimmed, "}") ||
		strings.HasPrefix(trimmed, `"provider"`) ||
		strings.HasPrefix(trimmed, `"model_id"`) ||
		strings.HasPrefix(trimmed, `"prompt"`) ||
		strings.HasPrefix(trimmed, `"aspect_ratio"`) ||
		strings.HasPrefix(trimmed, `"resolution"`) ||
		strings.HasPrefix(trimmed, `"number_of_images"`) ||
		strings.HasPrefix(trimmed, `"output_path"`)
}

func isCodexJSONToolOutputSegment(lines []string) bool {
	for _, line := range lines {
		if codexToolJSONReplayPattern.MatchString(line) ||
			strings.Contains(line, "list_provider_models") ||
			strings.Contains(line, "/tools/custom/") {
			return true
		}
	}
	return false
}

func isCodexJSONToolOutputLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(trimmed, "{") ||
		strings.HasPrefix(trimmed, "}") ||
		strings.HasPrefix(trimmed, "[") ||
		strings.HasPrefix(trimmed, "]") ||
		strings.HasPrefix(trimmed, `"count"`) ||
		strings.HasPrefix(trimmed, `"models"`) ||
		strings.HasPrefix(trimmed, `"model_id"`) ||
		strings.HasPrefix(trimmed, `"context_window"`) ||
		strings.HasPrefix(trimmed, `"input_cost_per_1m"`) ||
		strings.HasPrefix(trimmed, `"output_cost_per_1m"`) ||
		strings.HasPrefix(trimmed, `"reasoning_cost"`) ||
		strings.HasPrefix(trimmed, `"supports"`) ||
		strings.Contains(lower, `\"model_id\"`) ||
		strings.Contains(lower, `\"context_window\"`) ||
		strings.Contains(lower, `\"input_cost_per_1m\"`) ||
		strings.Contains(lower, `\"output_cost_per_1m\"`)
}

func isCodexLikelyToolReplayAssistantSegment(lines []string, prevKind, nextKind codexSegmentKind) bool {
	if len(lines) == 0 {
		return false
	}
	if isCodexAssistantAnswerPathSegment(lines) {
		return false
	}
	nearTool := prevKind == codexSegmentToolStatus || nextKind == codexSegmentToolStatus ||
		prevKind == codexSegmentPlanStatus || nextKind == codexSegmentPlanStatus ||
		prevKind == codexSegmentErrorStatus || nextKind == codexSegmentErrorStatus
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if isCodexToolReplayLine(trimmed) {
			return true
		}
		if strings.EqualFold(trimmed, "environment.") ||
			strings.HasPrefix(trimmed, "# List supported and currently usable") {
			return true
		}
		if nearTool && (isCodexJSONToolOutputLine(trimmed) ||
			strings.Contains(lower, "model_id") ||
			strings.Contains(lower, "output_path") ||
			strings.Contains(lower, "aspect_ratio") ||
			strings.Contains(lower, "workspace-docs") ||
			strings.Contains(lower, "mcp_api_token") ||
			strings.Contains(lower, "access denied") ||
			strings.Contains(lower, "writable folders") ||
			strings.Contains(trimmed, `\"`) ||
			codexBareNumberPattern.MatchString(trimmed) ||
			codexTruncatedLSLine.MatchString(trimmed) ||
			codexTruncatedPathLine.MatchString(trimmed)) {
			return true
		}
	}
	return false
}

func hasCodexActivity(captured string) bool {
	if hasCodexQueuedInput(captured) {
		return true
	}
	lines := strings.Split(stripCodexANSI(captured), "\n")
	seenNonEmpty := 0
	seenReadyPrompt := false
	seenPromptWithInput := false
	for i := len(lines) - 1; i >= 0 && seenNonEmpty < codexActivityScanNonEmptyLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seenNonEmpty++
		lower := strings.ToLower(line)
		if line == "›" || line == ">" || line == "❯" || strings.HasPrefix(line, "› ") || strings.HasSuffix(line, "›") {
			seenReadyPrompt = true
			if isCodexPromptWithInputLine(line) {
				seenPromptWithInput = true
			}
			continue
		}
		if seenReadyPrompt && isCodexCompletedStatusLine(line) {
			return false
		}
		if strings.Contains(lower, "esc to interrupt") || isCodexActiveStatusLine(line) {
			if seenPromptWithInput {
				continue
			}
			return true
		}
	}
	return false
}

func hasCodexReadyPrompt(captured string) bool {
	if hasCodexTrustPrompt(captured) || hasCodexRateLimitReminderModal(captured) || hasCodexQueuedInput(captured) {
		return false
	}
	lines := strings.Split(stripCodexANSI(captured), "\n")
	seenNonEmpty := 0
	for i := len(lines) - 1; i >= 0 && seenNonEmpty < 12; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seenNonEmpty++
		if line == "›" || line == ">" || line == "❯" || strings.HasPrefix(line, "› ") || strings.HasSuffix(line, "›") {
			return !hasCodexActivity(captured)
		}
	}
	return false
}

func hasCodexQueuedInput(captured string) bool {
	lines := strings.Split(stripCodexANSI(captured), "\n")
	seenNonEmpty := 0
	seenLaterPromptWithInput := false
	seenLaterCompletion := false
	for i := len(lines) - 1; i >= 0 && seenNonEmpty < 80; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seenNonEmpty++
		if isCodexPromptWithInputLine(line) {
			seenLaterPromptWithInput = true
			continue
		}
		if isCodexCompletedStatusLine(line) {
			seenLaterCompletion = true
			continue
		}
		if isCodexQueuedInputLine(line) {
			return !seenLaterPromptWithInput && !seenLaterCompletion
		}
	}
	return false
}

func isCodexPromptWithInputLine(line string) bool {
	trimmed := strings.TrimSpace(stripCodexANSI(line))
	if len(trimmed) <= len("› ") {
		return false
	}
	for _, prefix := range []string{"› ", "❯ ", "> "} {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)) != ""
		}
	}
	return false
}

func hasCodexTrustPrompt(captured string) bool {
	lines := strings.Split(stripCodexANSI(captured), "\n")
	lastTrustQuestion := -1
	lastSelectedTrustOption := -1
	lastLaterInputPrompt := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "do you trust the contents of this directory") {
			lastTrustQuestion = i
			continue
		}
		if strings.HasPrefix(trimmed, "› ") {
			option := strings.TrimSpace(strings.TrimPrefix(trimmed, "› "))
			optionLower := strings.ToLower(option)
			if strings.HasPrefix(optionLower, "1. yes, continue") || strings.HasPrefix(optionLower, "2. no, quit") {
				lastSelectedTrustOption = i
				continue
			}
			lastLaterInputPrompt = i
		}
	}
	return lastTrustQuestion >= 0 &&
		lastSelectedTrustOption > lastTrustQuestion &&
		lastLaterInputPrompt < lastTrustQuestion
}

func isCodexTrustPromptLine(line string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "› "))
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "do you trust the contents of this directory") ||
		strings.Contains(lower, "working with untrusted contents") ||
		strings.Contains(lower, "trusting the directory allows") ||
		strings.HasPrefix(lower, "1. yes, continue") ||
		strings.HasPrefix(lower, "2. no, quit") ||
		strings.Contains(lower, "press enter to continue")
}

func hasCodexRateLimitReminderModal(captured string) bool {
	lower := strings.ToLower(stripCodexANSI(captured))
	return strings.Contains(lower, "approaching rate limits") &&
		strings.Contains(lower, "switch to ") &&
		strings.Contains(lower, "keep current model") &&
		strings.Contains(lower, "press enter to confirm")
}

func isCodexRateLimitReminderLine(line string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "› "))
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "heads up") && strings.Contains(lower, "limit left") ||
		strings.Contains(lower, "approaching rate limits") ||
		strings.Contains(lower, "switch to ") && strings.Contains(lower, "credit usage") ||
		strings.HasPrefix(lower, "1. switch to ") ||
		strings.HasPrefix(lower, "2. keep current model") ||
		strings.HasPrefix(lower, "3. keep current model") ||
		strings.Contains(lower, "hide future rate limit reminders") ||
		strings.Contains(lower, "press enter to confirm or esc to go back")
}

func dismissCodexTrustPrompt(ctx context.Context, sessionName, captured string) error {
	selected := selectedCodexTrustPromptOption(captured)
	keys := make([]string, 0, 2)
	if selected == 2 {
		keys = append(keys, "Up")
	}
	keys = append(keys, "C-m")
	args := []string{"send-keys", "-t", sessionName}
	args = append(args, keys...)
	return runCodexCommand(ctx, nil, "tmux", args...)
}

func selectedCodexTrustPromptOption(captured string) int {
	lines := strings.Split(stripCodexANSI(captured), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "› ") {
			continue
		}
		option := strings.TrimSpace(strings.TrimPrefix(trimmed, "› "))
		switch {
		case strings.HasPrefix(option, "1."):
			return 1
		case strings.HasPrefix(option, "2."):
			return 2
		}
	}
	return 0
}

func dismissCodexRateLimitReminder(ctx context.Context, sessionName, captured string) error {
	selected := selectedCodexRateLimitReminderOption(captured)
	if selected < 1 || selected > 3 {
		selected = 1
	}
	keys := make([]string, 0, 4)
	for selected < 3 {
		keys = append(keys, "Down")
		selected++
	}
	keys = append(keys, "C-m")
	args := []string{"send-keys", "-t", sessionName}
	args = append(args, keys...)
	return runCodexCommand(ctx, nil, "tmux", args...)
}

func selectedCodexRateLimitReminderOption(captured string) int {
	lines := strings.Split(stripCodexANSI(captured), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "› ") {
			continue
		}
		option := strings.TrimSpace(strings.TrimPrefix(trimmed, "› "))
		switch {
		case strings.HasPrefix(option, "1."):
			return 1
		case strings.HasPrefix(option, "2."):
			return 2
		case strings.HasPrefix(option, "3."):
			return 3
		}
	}
	return 0
}

func streamCodexTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	snapshot, err := captureCodexPaneForDisplay(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripCodexANSI(snapshot), "\n")
	if strings.TrimSpace(snapshot) == "" || snapshot == *lastTerminalSnapshot {
		return false
	}
	select {
	case streamChan <- llmtypes.StreamChunk{
		Type:    llmtypes.StreamChunkTypeTerminal,
		Content: snapshot,
		Metadata: map[string]interface{}{
			"tmux_session":              sessionName,
			"codex_interactive_session": sessionName,
		},
	}:
		*lastTerminalSnapshot = snapshot
		return true
	default:
		return false
	}
}

func stripCodexEchoedUserPrompt(text, prompt string) string {
	text = strings.TrimSpace(text)
	prompt = strings.TrimSpace(prompt)
	if text == "" || prompt == "" {
		return text
	}

	lines := nonEmptyCodexLines(text)
	promptLines := nonEmptyCodexLines(prompt)
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
				codexPromptLinesEqual(lines[start+matchLen], promptLines[promptStart+matchLen]) {
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

func codexPromptLinesEqual(a, b string) bool {
	a = normalizeCodexPromptLine(a)
	b = normalizeCodexPromptLine(b)
	return a != "" && a == b
}

func normalizeCodexPromptLine(line string) string {
	line = strings.TrimSpace(stripCodexANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, ">")
	line = strings.TrimSpace(line)
	return line
}

func stripCodexHistoricalAssistantText(text string, historicalAssistantTexts []string) string {
	text = strings.TrimSpace(text)
	if text == "" || len(historicalAssistantTexts) == 0 {
		return text
	}
	for i := len(historicalAssistantTexts) - 1; i >= 0; i-- {
		historical := strings.TrimSpace(historicalAssistantTexts[i])
		if historical == "" {
			continue
		}
		if stripped, ok := stripCodexHistoricalPrefix(text, historical); ok {
			text = strings.TrimSpace(stripped)
			i = len(historicalAssistantTexts)
		}
	}
	return text
}

func stripCodexHistoricalPrefix(text, historical string) (string, bool) {
	if text == historical {
		return "", true
	}
	if strings.HasPrefix(text, historical) {
		return text[len(historical):], true
	}

	historicalLines := nonEmptyCodexLines(historical)
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

func nonEmptyCodexLines(text string) []string {
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

func isCodexActiveStatusLine(line string) bool {
	trimmed := strings.TrimSpace(stripCodexANSI(line))
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "generating") ||
		strings.HasPrefix(lower, "working (") ||
		strings.HasPrefix(lower, "thinking") ||
		strings.HasPrefix(lower, "processing") ||
		strings.HasPrefix(lower, "running") ||
		strings.HasPrefix(lower, "executing") ||
		strings.HasPrefix(lower, "calling ") ||
		strings.HasPrefix(lower, "calling api-bridge") ||
		strings.Contains(lower, "ctrl+l is disabled while a task is in progress") ||
		strings.Contains(lower, "esc to interrupt")
}

func isCodexCompletedStatusLine(line string) bool {
	trimmed := strings.TrimSpace(stripCodexANSI(line))
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "esc to interrupt") ||
		strings.Contains(lower, "still thinking") ||
		strings.Contains(lower, "thinking with") ||
		strings.Contains(lower, "working (") {
		return false
	}
	if !strings.Contains(lower, " for ") {
		return false
	}
	return strings.HasPrefix(trimmed, "✻ ") ||
		strings.HasPrefix(trimmed, "✽ ") ||
		strings.HasPrefix(trimmed, "✳ ") ||
		strings.HasPrefix(trimmed, "✶ ") ||
		strings.HasPrefix(trimmed, "✢ ") ||
		strings.HasPrefix(trimmed, "· ")
}

func isCodexBoxDrawingLine(line string) bool {
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

func interruptCodexInteractiveSession(sessionName string, logger interfaces.Logger) {
	interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runCodexCommand(interruptCtx, nil, "tmux", "send-keys", "-t", sessionName, "Escape"); err != nil && logger != nil {
		logger.Debugf("Failed to send Escape to Codex interactive session %s: %v", sessionName, err)
	}
}

func resetCodexPaneForTurn(ctx context.Context, sessionName string) {
	_ = runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-u")
}

func captureCodexPane(ctx context.Context, sessionName string) (string, error) {
	return runCodexCommandOutput(ctx, nil, "tmux", "capture-pane", "-p", "-J", "-S", "-3000", "-t", sessionName)
}

func captureCodexPaneForDisplay(ctx context.Context, sessionName string) (string, error) {
	return runCodexCommandOutput(ctx, nil, "tmux", "capture-pane", "-p", "-S", "-3000", "-t", sessionName)
}

func codexCapturedAfterBaseline(captured, baseline string) string {
	if baseline != "" {
		if idx := strings.LastIndex(captured, baseline); idx >= 0 {
			return captured[idx+len(baseline):]
		}
	}
	return captured
}

func killCodexTmuxSession(ctx context.Context, sessionName string) error {
	if strings.TrimSpace(sessionName) == "" {
		return nil
	}
	if err := runCodexCommand(ctx, nil, "tmux", "kill-session", "-t", sessionName); err != nil {
		if strings.Contains(err.Error(), "can't find session") || strings.Contains(err.Error(), "no server running") {
			return nil
		}
		return err
	}
	return nil
}

func codexInteractiveSessionPrefix() string {
	prefix := strings.TrimSpace(os.Getenv(EnvCodexInteractiveSessionPrefix))
	if prefix == "" {
		prefix = "mlp-codex-cli-int"
	}
	return sanitizeCodexTmuxSessionName(prefix)
}

func newCodexTmuxSessionName() string {
	return sanitizeCodexTmuxSessionName(fmt.Sprintf("%s-%d-%s", codexInteractiveSessionPrefix(), time.Now().UnixNano(), codexRandomHex(4)))
}

func codexInteractiveTimeout() time.Duration {
	return codexDurationFromEnvAllowZero(EnvCodexInteractiveTimeoutSeconds, defaultCodexInteractiveTimeout)
}

func codexInteractiveCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := codexInteractiveTimeout()
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func codexInteractiveIdleTimeout() time.Duration {
	return codexDurationFromEnv(EnvCodexInteractiveIdleTimeoutSeconds, defaultCodexInteractiveIdleTimeout)
}

func codexInteractiveRetention() time.Duration {
	return tmuxlaunch.Retention(defaultCodexInteractiveRetention)
}

func codexInteractivePromptWait() time.Duration {
	return tmuxlaunch.PromptWait(EnvCodexInteractivePromptWaitSeconds)
}

func codexInteractiveStreamTmuxScreenEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvCodexInteractiveStreamTmuxScreen))) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func codexDurationFromEnv(key string, fallback time.Duration) time.Duration {
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

func codexDurationFromEnvAllowZero(key string, fallback time.Duration) time.Duration {
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

func runCodexCommand(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	_, err := runCodexCommandOutput(ctx, stdin, name, args...)
	return err
}

func runCodexCommandOutput(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
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

func codexRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func sanitizeCodexTmuxSessionName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "codex"
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

func stripCodexANSI(s string) string {
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
