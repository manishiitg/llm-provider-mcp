package codexcli

import (
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
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/paneview"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/sessionregistry"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxexec"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
)

const (
	// Default to no provider-level turn timeout. Workflow/background callers own
	// their execution deadline; the adapter should not cancel a still-running tmux
	// coding agent before the outer workflow timeout.
	defaultCodexInteractiveTimeout     = 0
	defaultCodexInteractiveIdleTimeout = 3 * time.Hour
	defaultCodexInteractiveRetention   = 30 * time.Minute
	codexInteractiveStableWindow       = 1200 * time.Millisecond
	codexActivityScanNonEmptyLines     = 160

	// defaultCodexInteractiveStalePaneBackstop is a detection-independent backstop
	// for the response-wait loop: codexcli has no turn timeout, so if prompt
	// detection (hasCodexReadyPrompt) never recognizes completion AND the tmux
	// pane is byte-frozen after the turn produced activity, the loop would spin
	// forever. This bounds that case. Only ever consulted once sawActivity is set,
	// so it never trips a never-delivered prompt (that is the no-input case).
	defaultCodexInteractiveStalePaneBackstop = 120 * time.Second

	EnvCodexInteractiveSessionPrefix            = "CODEX_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvCodexInteractiveTimeoutSeconds           = "CODEX_CLI_INTERACTIVE_TIMEOUT_SECONDS"
	EnvCodexInteractiveIdleTimeoutSeconds       = "CODEX_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvCodexInteractivePromptWaitSeconds        = "CODEX_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvCodexInteractiveStreamTmuxScreen         = "CODEX_CLI_STREAM_TMUX_SCREEN"
	EnvCodexInteractiveStalePaneBackstopSeconds = "CODEX_CLI_INTERACTIVE_STALE_PANE_BACKSTOP_SECONDS"
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
	workingDir                string
	idleTimer                 *time.Timer
	initErr                   error
	createdAt                 time.Time
	lastUsed                  time.Time
	mu                        sync.Mutex
}

var codexInteractiveRegistry = sessionregistry.NewOwnerRegistry[string]()
var codexPersistentRegistry = sessionregistry.NewOwnerRegistry[*codexInteractiveSession]()

func (c *CodexCLIAdapter) generateContentInteractive(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (resp *llmtypes.ContentResponse, err error) {
	var tmuxSessionName string
	defer func() {
		if isCodexTmuxSessionLostError(err) {
			err = llmtypes.WrapCodingAgentTmuxSessionLostError(err, "codex-cli", tmuxSessionName, "tmux session lost")
		}
	}()

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
	c.logger.Debugf("codex interactive launch enter owner=%s persistent=%v workingDir=%q", ownerSessionID, persistent, codexWorkingDirFromOptions(opts))

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
	tmuxSessionName = session.tmuxSessionName
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

	c.logger.Debugf("codex interactive acquired owner=%s tmux=%s; waiting for first prompt", ownerSessionID, session.tmuxSessionName)
	if err := waitForCodexPrompt(callCtx, session.tmuxSessionName, opts.StreamChan); err != nil {
		markCodexInteractiveSessionFailedLocked(session, err, c.logger)
		releaseSession = false
		failedSession := session
		session.mu.Unlock()
		session = nil
		cleanupFailedCodexInteractiveSession(failedSession)
		return nil, err
	}
	c.logger.Debugf("codex interactive first prompt ready owner=%s tmux=%s; resetting pane", ownerSessionID, session.tmuxSessionName)
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
	c.logger.Debugf("codex interactive ready for prompt owner=%s tmux=%s", ownerSessionID, session.tmuxSessionName)

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
	c.logger.Debugf("codex interactive acquire enter owner=%s", ownerSessionID)
	now := time.Now()
	workingDir := codexWorkingDirFromOptions(opts)
	session, created, ok := codexPersistentRegistry.GetOrCreate(ownerSessionID, func() *codexInteractiveSession {
		session := &codexInteractiveSession{
			ownerSessionID:  ownerSessionID,
			tmuxSessionName: newCodexTmuxSessionName(),
			workingDir:      workingDir,
			createdAt:       now,
			lastUsed:        now,
		}
		session.mu.Lock()
		return session
	})
	if !ok {
		return nil, fmt.Errorf("codex-cli interactive mode requires an owner session ID")
	}
	if !created {
		// Release the GLOBAL registry lock BEFORE taking the per-session lock.
		// session.mu is held for the duration of a whole turn; acquiring it
		// while still holding the global map lock stalls every other codex
		// acquire behind a busy session (a lock-held-across-blocking-call
		// deadlock — the exact hang that froze background step orchestrators).
		// The two locks have different lifetimes: the registry lock guards the
		// map only; grab the pointer under it, release it, then take the
		// per-session lock. Holding a pointer keeps the session valid even if a
		// concurrent teardown removes it from the map (initErr guards that).
		session.mu.Lock()
		if session.initErr != nil {
			err := session.initErr
			session.mu.Unlock()
			return nil, err
		}
		if session.idleTimer != nil {
			session.idleTimer.Stop()
			session.idleTimer = nil
		}
		session.lastUsed = time.Now()
		c.logger.Debugf("codex interactive reusing session owner=%s tmux=%s", ownerSessionID, session.tmuxSessionName)
		return session, nil
	}

	// buildCodexInteractiveArgs also performs the opt-in workspace
	// artifact projection (AGENTS.md system prompt, .codex/config.toml
	// MCP tables) up front so it can skip the CLI-side
	// -c model_instructions_file injection when project-instruction-only
	// mode is on AND the AGENTS.md projection succeeded. The returned
	// cleanup (nil when nothing was projected) is stored on the session
	// and run at teardown. The projection is off by default; the existing
	// -c model_instructions_file and -c mcp_servers.* overrides already
	// inject equivalent configuration. The workspace projection is
	// additive and useful when downstream tooling reads codex's on-disk
	// conventions directly.
	c.logger.Debugf("codex interactive building args owner=%s tmux=%s workingDir=%q", ownerSessionID, session.tmuxSessionName, workingDir)
	args, systemPromptTempFile, projectCleanup, err := c.buildCodexInteractiveArgs(opts, systemPrompt)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		removeCodexPersistentSession(ownerSessionID, session)
		return nil, err
	}
	c.logger.Debugf("codex interactive built args owner=%s args=%d", ownerSessionID, len(args))
	session.systemPromptTempFile = systemPromptTempFile
	session.projectInstructionCleanup = projectCleanup

	// Project attached skills into .agents/skills/ so Codex's
	// repo-scoped skill loader picks them up at startup. Independent
	// of writeProjectInstructionFromOptions (which gates the
	// AGENTS.md + .codex/ artifact projection); skills are useful
	// even when the operator-instruction-file projection is off.
	// Best-effort.
	if attachedSkills := llmtypes.AttachedSkillsFromOptions(opts); len(attachedSkills) > 0 && workingDir != "" {
		_ = c.ProjectSkills(workingDir, attachedSkills)
	}
	c.logger.Debugf("codex interactive starting tmux owner=%s tmux=%s", ownerSessionID, session.tmuxSessionName)
	if err := startCodexTmuxSession(ctx, session.tmuxSessionName, args, workingDir); err != nil {
		c.logger.Errorf("codex interactive failed to start tmux owner=%s tmux=%s: %v", ownerSessionID, session.tmuxSessionName, err)
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
	c.logger.Debugf("codex interactive started owner=%s tmux=%s", ownerSessionID, session.tmuxSessionName)
	return session, nil
}

// buildCodexInteractiveArgs builds the codex TUI launch args. It also
// performs the opt-in workspace artifact projection (AGENTS.md, .codex/
// config.toml) up front so that the CLI-side -c model_instructions_file
// injection can be skipped when project-instruction-only mode is on AND the
// AGENTS.md projection succeeded. It returns the args, the temp
// system-prompt file path (empty when none was written), a cleanup for the
// projected artifacts (nil when nothing was projected), and an error.
func (c *CodexCLIAdapter) buildCodexInteractiveArgs(opts *llmtypes.CallOptions, systemPrompt string) ([]string, string, func(), error) {
	modelToUse := resolveCodexCLIModelID(c.modelID)
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if model, ok := opts.Metadata.Custom[MetadataKeyCodexModel].(string); ok && model != "" {
			modelToUse = resolveCodexCLIModelID(model)
		}
	}

	// Project the opt-in workspace artifacts (AGENTS.md system prompt,
	// .codex/config.toml MCP tables) FIRST so projection success is known
	// before deciding whether to skip the CLI-side -c model_instructions_file
	// injection below. ON by default via WithWriteProjectInstructionFile;
	// best-effort, a failure here is not a session-killer.
	//
	// projectedToInstructionFile records whether the AGENTS.md projection
	// actually succeeded. Used to gate the CLI injection in
	// project-instruction-only mode so the prompt is carried once, not
	// twice. The returned projectCleanup is stored on the session by the
	// caller and run at teardown.
	var projectCleanup func()
	projectedToInstructionFile := false
	workingDir := codexWorkingDirFromOptions(opts)
	if writeProjectInstructionFromOptions(opts) && strings.TrimSpace(workingDir) != "" && strings.TrimSpace(systemPrompt) != "" {
		mcpServersJSON := ""
		if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
			if v, ok := opts.Metadata.Custom[MetadataKeyMCPServers].(string); ok {
				mcpServersJSON = v
			}
		}
		if cleanup, perr := writeCodexProjectArtifacts(workingDir, systemPrompt, mcpServersJSON, true, restoreProjectFilesFromOptions(opts)); perr != nil {
			c.logger.Errorf("codex cli: project artifacts write failed (best-effort): %v", perr)
		} else if cleanup != nil {
			projectCleanup = cleanup
			projectedToInstructionFile = true
		}
	}

	resumeSessionID, err := codexValidatedResumeSessionIDFromOptions(opts)
	if err != nil {
		if projectCleanup != nil {
			projectCleanup()
		}
		return nil, "", nil, err
	}
	appendResumeSessionID := func(args []string) []string {
		if resumeSessionID != "" {
			args = append(args, resumeSessionID)
		}
		return args
	}

	args := []string{"codex"}
	// (The previous --dangerously-bypass-hook-trust wiring was removed
	// when the codex .codex/hooks.json projection was deleted from
	// writeCodexProjectArtifacts — see comment in that file. Codex's
	// --disable <feature> CLI flags via WithDisableShellTool /
	// WithDisableFeatures replace the need for a hooks.json drop, so
	// we never have hooks codex needs to trust-bypass for.)
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
	// Inject the system prompt via -c model_instructions_file UNLESS the
	// caller opted into project-instruction-only mode AND the AGENTS.md
	// projection above actually succeeded — in which case AGENTS.md is the
	// sole carrier (no doubled prompt / token cost). If the projection was
	// skipped (flag off / empty working dir) or failed,
	// projectedToInstructionFile is false and we still inject so the prompt
	// is never silently dropped.
	if strings.TrimSpace(systemPrompt) != "" && !(projectInstructionOnlyFromOptions(opts) && projectedToInstructionFile) {
		systemPromptTempFile, err := writeCodexInteractiveSystemPromptFile(systemPrompt)
		if err != nil {
			if projectCleanup != nil {
				projectCleanup()
			}
			return nil, "", nil, err
		}
		override, err := codexStringConfigOverride("model_instructions_file", systemPromptTempFile)
		if err != nil {
			_ = os.Remove(systemPromptTempFile)
			if projectCleanup != nil {
				projectCleanup()
			}
			return nil, "", nil, err
		}
		args = append(args, "-c", override)
		return appendResumeSessionID(args), systemPromptTempFile, projectCleanup, nil
	}
	return appendResumeSessionID(args), "", projectCleanup, nil
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

// CloseCodexCLIInteractiveSessionForOwner closes the persistent codex
// interactive session for the given owner. See agycli's equivalent
// CloseAgyCLIInteractiveSessionForOwner for the mid-chat-prompt-change
// motivation.
func CloseCodexCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	closeCodexPersistentSession(ownerSessionID, reason, nil)
}

// CloseCodexCLIInteractiveSessionByTmux closes the persistent codex
// interactive session whose backing tmux session matches tmuxSessionName,
// regardless of the owner key it was registered under. Teardown backstop for
// when the owning session ID is unknown or has drifted. Delegates to the
// owner-keyed close so the same graceful exit + cleanup runs. No-op when no
// live session matches.
func CloseCodexCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	name := strings.TrimSpace(tmuxSessionName)
	if name == "" {
		return
	}
	owner, _, ok := codexPersistentRegistry.Find(func(s *codexInteractiveSession) bool {
		return s != nil && s.tmuxSessionName == name
	})
	if !ok || owner == "" {
		return
	}
	closeCodexPersistentSession(owner, reason, nil)
}

func closeCodexPersistentSession(ownerSessionID, reason string, logger interfaces.Logger) {
	session, ok := codexPersistentRegistry.Delete(ownerSessionID)
	if !ok || session == nil {
		return
	}

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

// writeProjectInstructionFromOptions reads the feature flag for writing
// the per-session system prompt to <workingDir>/AGENTS.md. Defaults to
// true when the key is unset; callers can opt out by passing
// WithCodexWriteProjectInstructionFile(false).
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
// false when the key is unset: the default writes fresh and deletes on
// cleanup, never restoring pre-existing content. Callers opt into the
// legacy byte-restore behavior with WithRestoreProjectFiles(true).
func restoreProjectFilesFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyRestoreProjectFiles].(bool)
	return enabled
}

// projectInstructionOnlyFromOptions reads the OFF-by-default feature flag for
// carrying the system prompt only via the projected AGENTS.md file (skipping
// the CLI-side -c developer_instructions / -c model_instructions_file
// injection). Returns false when the key is unset. Callers opt in with
// WithProjectInstructionOnly(true).
func projectInstructionOnlyFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyProjectInstructionOnly].(bool)
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
func writeCodexProjectAgentsFile(workingDir, systemPrompt string, restorePrior bool) (func(), error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure codex working dir: %w", err)
	}
	path := filepath.Join(workingDir, "AGENTS.md")
	var previous []byte
	existed := false
	if restorePrior {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			previous, existed = data, true
		} else if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("read existing AGENTS.md: %w", readErr)
		}
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
	codexPersistentRegistry.DeleteIf(ownerSessionID, session)
}

func CleanupCodexCLIInteractiveSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	sessions := codexPersistentRegistry.Drain()

	var failures []string
	for _, session := range sessions {
		stopCodexIdleTimerIfAvailable(session)
		unregisterCodexInteractiveSession(session.ownerSessionID, session.tmuxSessionName)
		if session.systemPromptTempFile != "" {
			_ = os.Remove(session.systemPromptTempFile)
		}
		// Honor the WithWriteProjectInstructionFile byte-restore
		// promise: when the global cleanup path tears down sessions
		// without going through closeCodexPersistentSession (e.g.
		// process exit, test teardown, explicit
		// CleanupCodexCLIInteractiveSessions call), the project
		// artifact restore must still run. Without this, operator-
		// owned AGENTS.md / .codex/config.toml content stays
		// destroyed after persistent-session shutdown.
		if session.projectInstructionCleanup != nil {
			session.projectInstructionCleanup()
			session.projectInstructionCleanup = nil
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
	tmuxSessionName = strings.TrimSpace(tmuxSessionName)
	if tmuxSessionName == "" {
		return
	}
	codexInteractiveRegistry.Set(ownerSessionID, tmuxSessionName)
}

func unregisterCodexInteractiveSession(ownerSessionID, tmuxSessionName string) {
	codexInteractiveRegistry.DeleteIf(ownerSessionID, tmuxSessionName)
}

func activeCodexInteractiveSession(ownerSessionID string) (string, bool) {
	sessionName, ok := codexInteractiveRegistry.Get(ownerSessionID)
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
	sessionID, _ := codexValidatedResumeSessionIDFromOptions(opts)
	return sessionID
}

func codexValidatedResumeSessionIDFromOptions(opts *llmtypes.CallOptions) (string, error) {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return "", nil
	}
	if sessionID, ok := opts.Metadata.Custom[MetadataKeyResumeSessionID].(string); ok {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return "", nil
		}
		if !isCodexSessionID(sessionID) {
			return "", fmt.Errorf("invalid codex resume session id %q", sessionID)
		}
		return sessionID, nil
	}
	return "", nil
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
	if workingDir != "" {
		// Pre-trust workingDir in ~/.codex/config.toml so codex skips
		// its interactive "Do you trust the contents of this
		// directory?" prompt. Per
		// https://github.com/openai/codex/issues/14345, the trust
		// prompt fires even with --dangerously-bypass-approvals-and-sandbox
		// on v0.114+ unless the path is recorded in
		// projects."<path>".trust_level = "trusted". The pattern
		// mirrors preTrustClaudeWorkingDir which writes to
		// ~/.claude.json. Errors are silently ignored — the worst
		// case is the prompt appears and the in-tmux dismissCodexTrustPrompt
		// (already wired into waitForCodexPrompt) handles it
		// reactively.
		preTrustCodexWorkingDir(workingDir)
	}
	shellCommand := codexInteractiveShellCommand(args, workingDir)
	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runCodexCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		return fmt.Errorf("failed to start Codex interactive session %q: %w", sessionName, err)
	}
	_ = runCodexCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	if err := runCodexCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "history-limit", tmuxexec.DefaultHistoryLimit); err != nil {
		return fmt.Errorf("failed to configure Codex tmux history for session %q: %w", sessionName, err)
	}
	// Pin the window size to manual so the detached session keeps the size we
	// launched at instead of collapsing to default-size (80x24), which reflows
	// the TUI into half-width and makes the captured pane unreadable.
	_ = runCodexCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "window-size", "manual")
	_ = runCodexCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "focus-events", "on")
	return nil
}

var preTrustCodexMu sync.Mutex

// preTrustCodexWorkingDir records workingDir as a trusted project in
// ~/.codex/config.toml so codex's "Do you trust the contents of this
// directory?" prompt does not fire when the tmux session launches.
// Per https://github.com/openai/codex/issues/14345, codex v0.114+
// shows this prompt EVEN WITH --dangerously-bypass-approvals-and-sandbox
// set, unless the path is pre-recorded via
//
//	[projects."<absolute-path>"]
//	trust_level = "trusted"
//
// in ~/.codex/config.toml. We append this section if the workingDir
// is not already trusted. On macOS /var is a symlink to /private/var,
// so we record both raw and EvalSymlinks-resolved paths to match
// however codex normalizes the path at trust-check time.
//
// Errors are silently ignored — the in-tmux dismissCodexTrustPrompt
// (already wired into waitForCodexPrompt) reactively handles the
// prompt if pre-trust fails. This is belt-and-suspenders, not a
// replacement.
func preTrustCodexWorkingDir(workingDir string) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return
	}
	paths := []string{workingDir}
	if resolved, err := filepath.EvalSymlinks(workingDir); err == nil && resolved != workingDir {
		paths = append(paths, resolved)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return
	}
	configPath := filepath.Join(configDir, "config.toml")

	preTrustCodexMu.Lock()
	defer preTrustCodexMu.Unlock()

	existing, _ := os.ReadFile(configPath)
	existingStr := string(existing)

	var toAppend strings.Builder
	for _, p := range paths {
		// Look for an existing [projects."<p>"] section. If present,
		// assume it's already trusted (the issue's workaround scripts
		// follow the same idempotency assumption). Codex parses TOML
		// quoted-keys with backslash escapes, but project paths from
		// macOS/Linux don't contain characters that need escaping.
		marker := fmt.Sprintf("[projects.%q]", p)
		if strings.Contains(existingStr, marker) {
			continue
		}
		// Ensure separation from any prior content.
		if toAppend.Len() == 0 && existingStr != "" && !strings.HasSuffix(existingStr, "\n") {
			toAppend.WriteString("\n")
		}
		toAppend.WriteString("\n")
		toAppend.WriteString(marker)
		toAppend.WriteString("\n")
		toAppend.WriteString(`trust_level = "trusted"`)
		toAppend.WriteString("\n")
	}
	if toAppend.Len() == 0 {
		return
	}

	updated := existingStr + toAppend.String()
	_ = os.WriteFile(configPath, []byte(updated), 0o600)
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
	// hookTrustDismissAttempts counts re-dismissal attempts rather
	// than using a binary flag because codex's hook trust UI is a
	// MULTI-STEP wizard: an initial 3-option menu, then an expanded
	// review table after "Trust all and continue", then the normal
	// ready prompt. Each step needs its own dismissal keystrokes.
	// We cap attempts to avoid infinite loops if codex enters an
	// unexpected state — 6 attempts at 200ms ticks gives codex ~1.2s
	// to advance, well within the 120s prompt-wait deadline.
	hookTrustDismissAttempts := 0
	const maxHookTrustDismissAttempts = 6
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
			// Hook trust review screen ("Press t to trust all; enter to
			// review hooks; esc to close") fires on launch whenever the
			// workspace has a .codex/hooks.json that codex hasn't seen
			// before — even with --dangerously-bypass-hook-trust set,
			// because the flag enables hook EXECUTION but does not
			// auto-dismiss the visual review screen on v0.131.0.
			// Sending "t" trusts all hooks for this invocation, which
			// is the same trust state the flag already implies.
			if hasCodexHookTrustReviewPrompt(captured) && hookTrustDismissAttempts < maxHookTrustDismissAttempts {
				_ = dismissCodexHookTrustReviewPrompt(deadline, sessionName)
				hookTrustDismissAttempts++
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
	// Codex 0.142's TUI accepts tmux's literal Enter key here, while C-m can
	// leave the pasted text sitting in the input buffer without starting a turn.
	if err := runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Enter"); err != nil {
		return fmt.Errorf("failed to submit input to Codex interactive session: %w", err)
	}
	return nil
}

func waitForCodexInteractiveResponse(ctx context.Context, sessionName, baseline string, streamChan chan<- llmtypes.StreamChunk) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	stalePaneBackstop := codexInteractiveStalePaneBackstop()
	var sawActivity bool
	var idleSince time.Time
	var lastCaptured string
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	var dismissedRateReminder bool
	// Stale-pane backstop tracking: the raw capture from the previous tick and
	// the time it last changed. Tracked at the top of every tick, independent of
	// all the branch logic below, so a prompt-detection bug that keeps the loop
	// in a "not ready" branch can never suppress it.
	var backstopPrevCapture string
	var paneUnchangedSince time.Time
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
			// Stale-pane backstop. Independent of hasCodexReadyPrompt and every
			// branch below: if the pane has produced activity and then frozen
			// (byte-identical) for longer than the backstop, the turn is over but
			// completion detection failed to recognize it. Return the pane the same
			// way the ready-prompt success path does (return captured, nil) and let
			// the caller extract — codexcli's response extraction needs the prompt
			// and history which the success path also defers to the caller. Only
			// armed once sawActivity is set, so a never-delivered prompt (pane never
			// changed from baseline) never trips it.
			if captured != backstopPrevCapture {
				backstopPrevCapture = captured
				paneUnchangedSince = time.Now()
			} else if sawActivity && stalePaneBackstop > 0 && !paneUnchangedSince.IsZero() &&
				time.Since(paneUnchangedSince) >= stalePaneBackstop {
				return captured, nil
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
	lines, wrapEligible := normalizeCodexPaneLinesWithMeta(raw)
	segments := segmentCodexLines(lines, wrapEligible)
	assistantText := assistantTextFromCodexSegments(segments)
	return codexNormalizedSnapshot{
		AssistantText: assistantText,
		Fingerprint:   codexSnapshotFingerprint(assistantText),
		Segments:      segments,
	}
}

func normalizeCodexPaneLines(raw string) []string {
	lines, _ := normalizeCodexPaneLinesWithMeta(raw)
	return lines
}

// normalizeCodexPaneLinesWithMeta is normalizeCodexPaneLines plus a parallel
// wrapEligible flag per emitted line. A line is wrap-eligible when its raw
// source line was indented and carried NO "•" event-block bullet — i.e. it is a
// candidate continuation of a wrapped "›" prompt that the TUI split across
// terminal rows. normalizeCodexPaneLine trims the leading indentation and the
// bullet, destroying that signal, so we capture it here before normalization.
func normalizeCodexPaneLinesWithMeta(raw string) ([]string, []bool) {
	raw = normalizeCodexInlineTUIBoundaries(raw)
	raw = expandCodexEscapedToolNewlines(raw)
	rawLines := strings.Split(raw, "\n")
	lines := make([]string, 0, len(rawLines))
	wrapEligible := make([]bool, 0, len(rawLines))
	for _, line := range rawLines {
		eligible := isCodexWrapEligibleRawLine(line)
		normalized := normalizeCodexPaneLine(line)
		for _, splitLine := range splitCodexWorkingStatusPrefix(normalized) {
			if splitLine != "" && !isCodexBulletOnlyLine(splitLine) {
				lines = append(lines, splitLine)
				wrapEligible = append(wrapEligible, eligible)
			}
		}
	}
	return lines, wrapEligible
}

// isCodexWrapEligibleRawLine reports whether a raw pane line (ANSI stripped) is
// indented and has no leading "•" bullet. Codex marks each new event/message
// block with a "•" bullet; a genuine wrapped-prompt tail has neither a "›"
// marker nor a bullet, only the wrap indentation.
func isCodexWrapEligibleRawLine(line string) bool {
	stripped := stripCodexANSI(line)
	if strings.TrimSpace(stripped) == "" {
		return false
	}
	if stripped[0] != ' ' && stripped[0] != '\t' {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(stripped), "•")
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

func segmentCodexLines(lines []string, wrapEligible []bool) []codexSegment {
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
		lineWrapEligible := i < len(wrapEligible) && wrapEligible[i]
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
		if current.Kind == codexSegmentChrome && lineWrapEligible &&
			isCodexPromptWrapContinuationLine(current.Lines, line) {
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
		strings.Contains(lower, "edit last queued message") ||
		trimmed == "immediately)" ||
		strings.HasPrefix(trimmed, "↳ ") ||
		strings.Contains(lower, "tmux focus-events") ||
		// codex 0.142.3 injects a "• You have N usage limit resets available.
		// Run /usage to use one." system notice as its own event bullet. It is a
		// codex notification, not model output, so treat it as chrome to keep it
		// out of the extracted assistant answer.
		strings.Contains(lower, "usage limit resets available") ||
		strings.Contains(lower, "run /usage to use one") ||
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

// isCodexPromptWrapContinuationLine reports whether line is a wrapped
// continuation of a "›" user prompt that the TUI split across terminal lines.
// Codex renders only the FIRST line of a long prompt with the "›" marker; the
// continuation lines are indented with no marker, so classifyCodexLine falls
// through to its default (assistant_text) case and the wrapped tail leaks into
// the extracted answer. We absorb such a line into the prompt's chrome segment,
// but only when the chrome segment we are extending is itself a "›" prompt and
// the candidate line would otherwise be plain assistant text — keeping this
// narrow so real assistant replies are never swallowed.
func isCodexPromptWrapContinuationLine(currentLines []string, line string) bool {
	if len(currentLines) == 0 {
		return false
	}
	// The chrome segment must be a "›" user-prompt block. codex 0.142.3 renders
	// a multi-line user prompt (or a terminal-wrapped one) as the "›" marker line
	// FOLLOWED by SEVERAL indented, marker-less continuation lines. The previous
	// implementation only matched when the marker was the IMMEDIATELY preceding
	// line, so it absorbed just the first continuation; the remaining echoed
	// prompt lines leaked into an assistant segment and corrupted final-answer
	// extraction (e.g. a 4-line prompt echo leaving "first"/"second" lines that
	// merged into the real answer). Match whenever this chrome segment already
	// contains a prompt-marker line so EVERY continuation of a multi-line prompt
	// echo is absorbed. Absorption naturally stops at the answer because codex
	// renders the answer's first line with a "•" bullet (not wrap-eligible), which
	// flushes the chrome segment.
	if !codexChromeSegmentHasPromptMarker(currentLines) {
		return false
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	// Only absorb lines that would otherwise be classified as assistant text;
	// anything with its own structural classification (tool/plan/chrome/etc.)
	// must keep it so the existing handling applies.
	return classifyCodexLine(trimmed) == codexSegmentAssistant
}

// codexChromeSegmentHasPromptMarker reports whether a chrome segment's lines
// include a rendered "›" user-prompt marker line, identifying the segment as a
// prompt-echo block whose marker-less indented continuations should be absorbed
// rather than leaked into the extracted assistant answer.
func codexChromeSegmentHasPromptMarker(lines []string) bool {
	for _, l := range lines {
		if isCodexPromptMarkerLine(l) {
			return true
		}
	}
	return false
}

// isCodexPromptMarkerLine reports whether line is a rendered "›" user-prompt
// line (the marker plus the first wrapped chunk of the prompt text).
func isCodexPromptMarkerLine(line string) bool {
	trimmed := strings.TrimSpace(stripCodexANSI(line))
	return trimmed == "›" || trimmed == ">" || trimmed == "❯" ||
		strings.HasPrefix(trimmed, "› ") ||
		strings.HasPrefix(trimmed, "❯ ")
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
	if hasCodexTrustPrompt(captured) || hasCodexHookTrustReviewPrompt(captured) || hasCodexRateLimitReminderModal(captured) || hasCodexQueuedInput(captured) {
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
		// A later "›" prompt — whether it carries typed input OR shows the
		// rotating ghost placeholder of an EMPTY composer — means the composer
		// returned to its interactive/ready state below this banner, so an
		// earlier "messages queued after next tool call" banner is historical.
		// (During an active queue the live "• Working" line keeps the pane
		// active via hasCodexActivity, so not flagging queued here is safe.)
		if isCodexPromptWithInputLine(line) || isCodexGhostPlaceholderPromptLine(line) {
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
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			// codex 0.142.3+ renders a rotating dim "ghost" example prompt
			// inside the EMPTY composer (e.g. "› Implement {feature}").
			// That is a placeholder hint, NOT user-typed/queued input, so it
			// must not be mistaken for an active prompt-with-input — doing so
			// masks the "• Working"/"• Calling" activity lines that sit ABOVE
			// the composer in the new TUI layout.
			if isCodexGhostPlaceholderText(body) {
				return false
			}
			return body != ""
		}
	}
	return false
}

// codexGhostPlaceholders is the set of rotating example prompts codex
// 0.142.3 shows as dim ghost text inside the empty composer. Sourced from
// the codex binary's string table (the composer placeholder rotation).
// When one of these appears after the "›" marker the composer is EMPTY.
var codexGhostPlaceholders = map[string]struct{}{
	"explain this codebase":                               {},
	"summarize recent commits":                            {},
	"implement {feature}":                                 {},
	"find and fix a bug in @filename":                     {},
	"write tests for @filename":                           {},
	"improve documentation in @filename":                  {},
	"run /review on my current changes":                   {},
	"use /skills to list available skills":                {},
	"check recently modified functions for compatibility": {},
	"how many files have been modified?":                  {},
	"will this algorithm scale well?":                     {},
}

// isCodexGhostPlaceholderText reports whether body (the text after the "›"
// composer marker) is one of codex's rotating empty-composer ghost
// placeholders rather than real user input.
func isCodexGhostPlaceholderText(body string) bool {
	_, ok := codexGhostPlaceholders[strings.ToLower(strings.TrimSpace(body))]
	return ok
}

// isCodexGhostPlaceholderPromptLine reports whether line is a "›" composer
// prompt whose body is a rotating ghost placeholder — i.e. an EMPTY composer.
func isCodexGhostPlaceholderPromptLine(line string) bool {
	trimmed := strings.TrimSpace(stripCodexANSI(line))
	for _, prefix := range []string{"› ", "❯ ", "> "} {
		if strings.HasPrefix(trimmed, prefix) {
			return isCodexGhostPlaceholderText(strings.TrimPrefix(trimmed, prefix))
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

// hasCodexHookTrustReviewPrompt detects EITHER of codex's two hook
// trust prompts. Both are distinct from the workspace-trust prompt
// (hasCodexTrustPrompt) and the rate-limit reminder. Both fire when
// the workspace has a .codex/hooks.json that codex hasn't seen this
// invocation, even with --dangerously-bypass-hook-trust on the
// command line (the flag enables hook EXECUTION but does not
// auto-dismiss the visual prompts on v0.131.0).
//
// Form 1 — initial menu (this is what fresh sessions see):
//
//	Hooks need review
//	1 hook is new or changed.
//	Hooks can run outside the sandbox after you trust them.
//
//	› 1. Review hooks
//	  2. Trust all and continue
//	  3. Continue without trusting (hooks won't run)
//
//	Press enter to confirm or esc to go back
//
// Form 2 — expanded review table (reached via "1. Review hooks"):
//
//	Hooks
//	⚠ N hook(s) need review before [they|it] can run.
//	Event             Installed   Active   Review   Description
//	PreToolUse        1           0        1        ...
//	Press t to trust all; enter to review hooks; esc to close
//
// We match on distinctive bottom-line directives that are stable
// across hook-count variations. Either form returns true.
func hasCodexHookTrustReviewPrompt(captured string) bool {
	// captureCodexPane includes a deep scrollback window. Anchor text from a dismissed prompt
	// stays in scrollback indefinitely, so a naïve substring match
	// over the full buffer reports the prompt as "still showing" long
	// after we dismissed it — which then makes waitForCodexPrompt
	// loop forever in the dismissed branch and never reach
	// hasCodexReadyPrompt. Only inspect the tail of the buffer so
	// detection reflects what's currently RENDERED, not what was
	// ever shown.
	stripped := stripCodexANSI(captured)
	tail := lastNCodexLines(stripped, 40)
	// Form 1 — initial menu. We saw in live capture that tmux's
	// capture-pane sometimes omits intermediate lines from this menu
	// (e.g. the "Trust all and continue" row may be missing while the
	// header + option-1 row are present). Match on the distinctive
	// header AND the option-1 row — both are stable across captures
	// AND specific enough to never false-positive against codex's
	// other prompts.
	if strings.Contains(tail, "Hooks need review") &&
		strings.Contains(tail, "1. Review hooks") {
		return true
	}
	// Form 2 — expanded review table. The "Press t to trust all" line
	// is the bottom-line directive and is the most distinctive anchor.
	if strings.Contains(tail, "Press t to trust all") &&
		strings.Contains(tail, "esc to close") {
		return true
	}
	return false
}

// lastNCodexLines returns the last n lines of s, joined with newlines.
// Used to scope substring matches to the currently-rendered portion of
// a tmux capture-pane output (which includes scrollback we don't want
// to false-positive on).
func lastNCodexLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// dismissCodexHookTrustReviewPrompt sends the right key sequence for
// whichever hook trust prompt is currently showing. The two forms have
// disjoint key contracts so we must branch — sending "Down/Enter" to
// the expanded review form leaves stray input, and sending "t" to the
// menu form is interpreted as a non-existent option-letter and stays
// stuck. We detect by looking for each form's unique anchor text.
//
// Menu form ("Hooks need review" + numbered options): send Down
// (move from default "1. Review hooks" to "2. Trust all and
// continue") then Enter (confirm). Trust all + the
// --dangerously-bypass-hook-trust flag give equivalent runtime trust;
// either way the hooks fire on tool calls.
//
// Expanded review form (full hooks table + "Press t to trust all"):
// send "t" (trust all) then Escape (close the resulting "Press enter
// to view hooks; esc to close" follow-up modal).
//
// Short sleeps between keystrokes prevent tmux from coalescing the
// pair so codex sees discrete presses.
func dismissCodexHookTrustReviewPrompt(ctx context.Context, sessionName string) error {
	captured, err := captureCodexPane(ctx, sessionName)
	if err != nil {
		// Fall back to menu-form dismissal — it's the form fresh sessions
		// hit, so it's the safer default when capture races the prompt.
		captured = ""
	}
	stripped := stripCodexANSI(captured)

	if strings.Contains(stripped, "Press t to trust all") &&
		strings.Contains(stripped, "esc to close") {
		// Expanded review form: t → Escape.
		if err := runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "t"); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
		return runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Escape")
	}

	// Default: menu form. Down → Enter selects "Trust all and
	// continue" (codex registers the choice and shows "Trusting
	// hooks..."). Codex does NOT auto-close the modal after the
	// trust applies — the menu re-renders without the selected
	// option and the user is expected to dismiss with Escape. We
	// pause briefly to let the trust action settle, then send
	// Escape to fully close the modal so waitForCodexPrompt's next
	// poll sees the normal ready prompt.
	if err := runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Down"); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	if err := runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Enter"); err != nil {
		return err
	}
	time.Sleep(400 * time.Millisecond)
	return runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Escape")
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

var (
	codexStatusLineStreamMu sync.Mutex
	codexStatusLineStreamed = make(map[string]string) // sessionName -> last streamed token signature
)

// codexWorkingDirForSession maps a tmux session name to its working dir via the
// persistent registry, so the statusline reader can locate the right rollout.
func codexWorkingDirForSession(sessionName string) string {
	_, sess, ok := codexPersistentRegistry.Find(func(sess *codexInteractiveSession) bool {
		return sess != nil && sess.tmuxSessionName == sessionName
	})
	if ok && sess != nil {
		return sess.workingDir
	}
	return ""
}

func codexIntFromRef(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// streamCodexStatusLine emits a generic StatusLine chunk built from codex's local
// rollout JSONL (~/.codex/sessions/.../rollout-*.jsonl). Codex in tmux mode has
// no statusline command hook (unlike agy/claude) and no stdout JSON, so the
// rollout's token_count snapshot is the structured source — see
// readCodexTranscriptUsage. Token counts refresh per completed turn, not
// sub-second; the dedup below suppresses no-op re-emits.
func streamCodexStatusLine(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) bool {
	if streamChan == nil {
		return false
	}
	status := buildCodexStatusLine(sessionName, codexWorkingDirForSession(sessionName))
	if status == nil {
		return false
	}

	sig := fmt.Sprintf("%d:%d:%d:%s", status.InputTokens, status.OutputTokens, status.CacheReadInputTokens, status.Model)
	codexStatusLineStreamMu.Lock()
	if codexStatusLineStreamed[sessionName] == sig {
		codexStatusLineStreamMu.Unlock()
		return false
	}
	codexStatusLineStreamed[sessionName] = sig
	codexStatusLineStreamMu.Unlock()

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

// buildCodexStatusLine reads the freshest rollout for workingDir and assembles a
// generic StatusLine (codex-cli, real model, input/output/cached tokens) tagged
// with the owning tmux session. Returns nil when no usage is available yet.
func buildCodexStatusLine(tmuxSession, workingDir string) *llmtypes.StatusLine {
	// Require the session's working dir: readCodexTranscriptUsage with an empty
	// dir would match ANY freshest rollout under ~/.codex/sessions (a different
	// project/session, or none belonging to us), so without it we'd attribute
	// unrelated telemetry — and could starve the terminal chunk by emitting
	// spuriously into the stream channel.
	if strings.TrimSpace(workingDir) == "" {
		return nil
	}
	gi, model, _ := readCodexTranscriptUsage(time.Time{}, workingDir)
	if gi == nil {
		return nil
	}
	input := codexIntFromRef(gi.PromptTokens)
	output := codexIntFromRef(gi.CompletionTokens)
	cached := codexIntFromRef(gi.CachedContentTokens)
	if input == 0 && output == 0 && cached == 0 {
		return nil
	}
	meta := map[string]interface{}{}
	if strings.TrimSpace(tmuxSession) != "" {
		meta["tmux_session"] = tmuxSession
	}
	status := &llmtypes.StatusLine{
		Provider:             "codex-cli",
		Model:                model,
		InputTokens:          input,
		OutputTokens:         output,
		CacheReadInputTokens: cached,
		TotalInputTokens:     input + cached,
		TotalOutputTokens:    output,
		Metadata:             meta,
	}
	// Expose plan rate-limit usage (parsed from the rollout's token_count
	// rate_limits) as generic statusline extras, same contract as other CLIs.
	if gi.Additional != nil {
		if extras, ok := gi.Additional[llmtypes.StatusExtrasMetaKey].([]string); ok {
			status.SetStatusExtras(extras)
		}
	}
	return status
}

// GetStatusLine returns a snapshot of the current statusline for the session,
// satisfying llmtypes.StatusLineProvider. Codex sources it from the local
// rollout JSONL — see streamCodexStatusLine for the tmux-mode rationale.
func (c *CodexCLIAdapter) GetStatusLine(ctx context.Context, sessionID string) (*llmtypes.StatusLine, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	var tmuxSession, workingDir string
	if sess, ok := codexPersistentRegistry.Get(sessionID); ok && sess != nil {
		tmuxSession = sess.tmuxSessionName
		workingDir = sess.workingDir
	} else {
		_, sess, _ := codexPersistentRegistry.Find(func(sess *codexInteractiveSession) bool {
			return sess != nil && sess.tmuxSessionName == sessionID
		})
		if sess != nil && (sess.ownerSessionID == sessionID || sess.tmuxSessionName == sessionID) {
			tmuxSession = sess.tmuxSessionName
			workingDir = sess.workingDir
		}
	}

	status := buildCodexStatusLine(tmuxSession, workingDir)
	if status == nil {
		return nil, fmt.Errorf("no statusline usage available for codex session %q", sessionID)
	}
	return status, nil
}

func streamCodexTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	streamCodexStatusLine(ctx, sessionName, streamChan)
	snapshot, err := captureCodexPaneForDisplay(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripCodexANSIPreserveColors(snapshot), "\n")
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
	// If the matched run covers the ENTIRE extracted answer, this is not a leaked
	// prompt echo — segmentation already routes true prompt echoes to chrome, so
	// reaching here with a full-text match means the model's answer legitimately
	// reproduces (quoted) prompt content verbatim (e.g. "reply with exactly these
	// lines" tasks). Stripping it would yield an empty, failed extraction, so keep
	// the answer intact.
	if bestStart == 0 && bestLen == len(lines) {
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
	_ = runCodexCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "C-l")
	_ = runCodexCommand(ctx, nil, "tmux", "clear-history", "-t", sessionName)
}

func captureCodexPane(ctx context.Context, sessionName string) (string, error) {
	return tmuxexec.CapturePane(ctx, sessionName, tmuxexec.DefaultScrollbackLines)
}

func captureCodexPaneForDisplay(ctx context.Context, sessionName string) (string, error) {
	// -e preserves ANSI SGR (color, bold, dim, etc.) so the frontend can
	// colorize the snapshot via ansi_up. Cursor positioning sequences are
	// stripped by stripCodexANSIPreserveColors before the snapshot leaves
	// the adapter so they don't garble the rendered output.
	// -J joins wrapped lines so the frontend can handle wrapping natively without
	// hard splitting words mid-line.
	return tmuxexec.CapturePaneANSI(ctx, sessionName, tmuxexec.DefaultScrollbackLines)
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
	// Reap the pane process trees (CLI + spawned MCP node subprocesses) before
	// killing the session — kill-session only SIGHUPs the pane process, so the
	// children would otherwise orphan and leak.
	tmuxcontrol.ReapSessionProcessTree(ctx, sessionName)
	if err := runCodexCommand(ctx, nil, "tmux", "kill-session", "-t", sessionName); err != nil {
		if isCodexTmuxSessionLostError(err) {
			return nil
		}
		return err
	}
	return nil
}

func isCodexTmuxSessionLostError(err error) bool {
	if err == nil {
		return false
	}
	if llmtypes.IsCodingAgentTmuxSessionLostError(err) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no server running") ||
		strings.Contains(lower, "failed to connect to server") ||
		strings.Contains(lower, "can't find pane") ||
		strings.Contains(lower, "can't find session") ||
		strings.Contains(lower, "target pane not found") ||
		strings.Contains(lower, "no current target")
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

// codexInteractiveStalePaneBackstop bounds how long the response-wait loop will
// tolerate a byte-frozen pane after the turn has produced activity but no ready
// prompt was detected. Detection-independent backstop against a silent hang.
func codexInteractiveStalePaneBackstop() time.Duration {
	return codexDurationFromEnv(EnvCodexInteractiveStalePaneBackstopSeconds, defaultCodexInteractiveStalePaneBackstop)
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
	return tmuxexec.RunCommandOutput(ctx, stdin, nil, name, args...)
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

// stripCodexANSIPreserveColors strips ANSI cursor positioning / clear-screen
// sequences but preserves SGR (Select Graphic Rendition: color, bold, dim,
// underline, etc., terminated with `m`). The frontend feeds this output
// through ansi_up to colorize the rendered pane snapshot. Cursor positioning
// is dropped because ansi_up does not emulate VT100 movement.
func stripCodexANSIPreserveColors(s string) string {
	return paneview.StripANSIPreserveColors(s)
}
