package cursorcli

import (
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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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
	defaultCursorInteractiveTimeout     = 0
	defaultCursorInteractiveIdleTimeout = 3 * time.Hour
	defaultCursorInteractiveRetention   = 30 * time.Minute
	cursorInteractiveStableWindow       = 1200 * time.Millisecond
	cursorBootBannerPromptGrace         = 2 * time.Second
	// Hard cap on how long we wait for the CLI to show ANY activity after the
	// prompt is submitted. A live turn shows "Thinking…"/streaming within
	// seconds; if nothing appears within this window the input never reached the
	// pane (paste/Enter swallowed during launch), and without this cap the
	// response loop spins forever because every completion branch is gated
	// behind sawActivity and the call context has no deadline by default.
	defaultCursorInteractiveFirstActivityTimeout = 90 * time.Second
	// Detection-independent backstop: if the pane produced activity and then
	// froze (byte-identical) for longer than this after the turn finished, the
	// turn is over but completion detection (hasCursorReadyPrompt) failed to
	// recognize it. Rather than spin forever, extract whatever response is
	// present and return it (or fail cleanly if there is none).
	defaultCursorInteractiveStalePaneBackstop = 120 * time.Second

	EnvCursorInteractiveSessionPrefix               = "CURSOR_CLI_INTERACTIVE_SESSION_PREFIX"
	EnvCursorInteractiveTimeoutSeconds              = "CURSOR_CLI_INTERACTIVE_TIMEOUT_SECONDS"
	EnvCursorInteractiveIdleTimeoutSeconds          = "CURSOR_CLI_INTERACTIVE_IDLE_TIMEOUT_SECONDS"
	EnvCursorInteractivePromptWaitSeconds           = "CURSOR_CLI_INTERACTIVE_PROMPT_WAIT_SECONDS"
	EnvCursorInteractiveStreamTmuxScreen            = "CURSOR_CLI_STREAM_TMUX_SCREEN"
	EnvCursorInteractiveFirstActivityTimeoutSeconds = "CURSOR_CLI_INTERACTIVE_FIRST_ACTIVITY_TIMEOUT_SECONDS"
	EnvCursorInteractiveStalePaneBackstopSeconds    = "CURSOR_CLI_INTERACTIVE_STALE_PANE_BACKSTOP_SECONDS"
)

var cursorBridgeOnlyDeniedTools = []string{
	"Shell",
	"Read",
	"ListDir",
	"Glob",
	"Grep",
	"Search",
	"Edit",
	"Write",
	"Task",
	"Agent",
	"Subagent",
	"BackgroundAgent",
	"CloudAgent",
	"Delegate",
}

func cursorBridgeOnlyDeniedToolMatcher() string {
	return strings.Join(cursorBridgeOnlyDeniedTools, "|")
}

func cursorBridgeOnlyDeniedPermissionPatterns() []string {
	patterns := make([]string, 0, len(cursorBridgeOnlyDeniedTools))
	for _, tool := range cursorBridgeOnlyDeniedTools {
		patterns = append(patterns, tool+"(*)")
	}
	return patterns
}

func cursorBridgeOnlySystemPrompt(systemPrompt string, denyBuiltin bool) string {
	if !denyBuiltin {
		return systemPrompt
	}
	guidance := strings.TrimSpace(`Cursor bridge-only session rules:
- Do not start Cursor subagents, background agents, cloud agents, workers, delegated agents, or request a mode switch. Nested Cursor agents do not reliably inherit the api-bridge MCP config and can stall on interactive mode-switch prompts.
- Complete the task in this same Cursor session using the api-bridge MCP tools.
- Built-in filesystem, shell, edit, search, and delegation tools are intentionally denied by the orchestrator.`)
	if strings.TrimSpace(systemPrompt) == "" {
		return guidance
	}
	if strings.Contains(systemPrompt, guidance) {
		return systemPrompt
	}
	return strings.TrimRight(systemPrompt, "\n") + "\n\n" + guidance
}

type cursorInteractiveSession struct {
	ownerSessionID  string
	tmuxSessionName string
	workingDir      string
	persistent      bool
	cleanupFiles    func()
	idleTimer       *time.Timer
	initErr         error
	createdAt       time.Time
	lastUsed        time.Time
	mu              sync.Mutex
}

var cursorInteractiveRegistry = sessionregistry.NewOwnerRegistry[string]()
var cursorPersistentRegistry = sessionregistry.NewOwnerRegistry[*cursorInteractiveSession]()

func (c *CursorCLIAdapter) generateContentTmux(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (resp *llmtypes.ContentResponse, err error) {
	var tmuxSessionName string
	defer func() {
		if isCursorTmuxSessionLostError(err) {
			err = llmtypes.WrapCodingAgentTmuxSessionLostError(err, "cursor-cli", tmuxSessionName, "tmux session lost")
		}
	}()

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
	resumeID := cursorResumeSessionIDFromOptions(opts)
	resume := resumeID != ""
	launchOnly := llmtypes.CodingProviderLaunchOnlyFromOptions(opts)
	prompt := buildCursorPrompt(conversationMessages, resume)
	// JSON Schema structured output: cursor-cli has no flag equivalent to
	// claude-code's --json-schema, so we append the schema to the prompt
	// with explicit instructions. Same prompt-appended fallback used by
	// claude-code's interactive adapter and the gemini / codex adapters.
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
	// Launch-only: boot tmux with --resume so the user can see the prior
	// cursor conversation in the pane without sending any prompt yet.
	// Mirrors what agy + claude-code experimental do; the chat-history
	// resumed-terminal path calls model.GenerateContent(ctx, nil, ...)
	// with WithCodingProviderLaunchOnly() expecting this contract.
	if !launchOnly && strings.TrimSpace(prompt) == "" {
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
	tmuxSessionName = session.tmuxSessionName
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
	// Launch-only path: cursor is up with --resume applied (if resumeID
	// was provided), the pane shows whatever cursor has restored from
	// its store.db, and we hand back a SessionHandle so the orchestrator
	// can rebind to this tmux session on subsequent turns instead of
	// spawning yet another one.
	if launchOnly {
		var lastSnapshot string
		streamCursorTerminalSnapshot(callCtx, session.tmuxSessionName, opts.StreamChan, &lastSnapshot)
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
		nativeSessionID := resumeID
		if nativeSessionID == "" {
			if _, storeDBPath := readCursorTranscriptMessagesAndStoreDB(turnStart, session.workingDir, ownerSessionID); storeDBPath != "" {
				nativeSessionID = cursorNativeSessionIDFromStoreDBPath(storeDBPath)
			}
		}
		additional := map[string]interface{}{
			"provider":                      "cursor-cli",
			"cursor_mode":                   "tmux",
			"cursor_interactive_session":    session.tmuxSessionName,
			"cursor_persistent_interactive": persistent,
			"cursor_uses_print_json":        false,
			"cursor_working_dir":            session.workingDir,
			"cursor_launch_only":            true,
		}
		if nativeSessionID != "" {
			additional["cursor_session_id"] = nativeSessionID
		}
		gi := &llmtypes.GenerationInfo{Additional: additional}
		llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
			Provider:        "cursor-cli",
			Transport:       llmtypes.CodingProviderTransportTmux,
			NativeSessionID: nativeSessionID,
			TmuxSession:     session.tmuxSessionName,
			WorkingDir:      session.workingDir,
			Model:           c.modelID,
		})
		return &llmtypes.ContentResponse{
			Choices: []*llmtypes.ContentChoice{{Content: "", GenerationInfo: gi}},
		}, nil
	}
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
	//
	// The same store.db path also carries cursor's native session ID
	// (the <agentId> dir name), which `cursor-agent --resume <id>`
	// accepts. Publishing it as additional[cursor_session_id] is what
	// lets mcpagent persist the ID across server restarts so a chat
	// can be resumed in a fresh tmux session. Without this, resume is
	// silently broken in tmux mode even though the rest of the
	// orchestrator wiring works end-to-end.
	sidecarMsgs, storeDBPath := readCursorTranscriptMessagesAndStoreDB(turnStart, session.workingDir, ownerSessionID)
	if len(sidecarMsgs) > 0 {
		llmtypes.AttachCodingProviderIntermediateMessages(genInfo, llmtypes.CodingProviderIntermediateMessages{
			Provider:  "cursor-cli",
			Transport: llmtypes.CodingProviderTransportTmux,
			Messages:  sidecarMsgs,
		})
	}
	nativeSessionID := cursorNativeSessionIDFromStoreDBPath(storeDBPath)
	if nativeSessionID == "" {
		// Fall back to a caller-supplied resume ID so a SessionHandle
		// still gets attached when cursor hasn't committed the store.db
		// yet (the trivial-turn race documented above).
		nativeSessionID = resumeID
	}
	if nativeSessionID != "" {
		additional["cursor_session_id"] = nativeSessionID
	}
	// Attach a SessionHandle so the orchestrator can reattach to this
	// same tmux pane on future turns (rebind path), and so the
	// resumed-terminal restore tier picks up the native session ID even
	// when the agent isn't kept in memory.
	llmtypes.AttachCodingProviderSessionHandle(genInfo, llmtypes.CodingProviderSessionHandle{
		Provider:        "cursor-cli",
		Transport:       llmtypes.CodingProviderTransportTmux,
		NativeSessionID: nativeSessionID,
		TmuxSession:     session.tmuxSessionName,
		WorkingDir:      session.workingDir,
		Model:           c.modelID,
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
	now := time.Now()
	session, created, ok := cursorPersistentRegistry.GetOrCreate(ownerSessionID, func() *cursorInteractiveSession {
		session := &cursorInteractiveSession{
			ownerSessionID:  ownerSessionID,
			tmuxSessionName: newCursorTmuxSessionName(),
			persistent:      persistent,
			createdAt:       now,
			lastUsed:        now,
		}
		session.mu.Lock()
		return session
	})
	if !ok {
		return nil, fmt.Errorf("cursor-cli tmux mode requires an owner session ID")
	}
	if !created {
		// The registry lock protects only the owner map. Take the per-session
		// lock after lookup so one busy Cursor turn does not block unrelated
		// session acquisition.
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
		return session, nil
	}

	args, env, workingDir, cleanupFiles, err := c.buildCursorInteractiveLaunch(opts, systemPrompt, ownerSessionID)
	if err != nil {
		session.initErr = err
		session.mu.Unlock()
		removeCursorPersistentSession(ownerSessionID, session)
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
		removeCursorPersistentSession(ownerSessionID, session)
		return nil, err
	}
	registerCursorInteractiveSession(ownerSessionID, session.tmuxSessionName)
	return session, nil
}

func (c *CursorCLIAdapter) buildCursorInteractiveLaunch(opts *llmtypes.CallOptions, systemPrompt string, ownerSessionID string) ([]string, []string, string, func(), error) {
	workingDir := cursorWorkingDirFromOptions(opts)
	if workingDir == "" {
		workingDir = cursorMustGetwd()
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, nil, "", nil, fmt.Errorf("failed to create Cursor CLI working directory: %w", err)
	}

	// Project attached skills into .cursor/skills/ so Cursor's native
	// Agent Skills loader picks them up at session start. Non-fatal:
	// the listing is also in the system prompt via mcpagent.
	if skills := llmtypes.AttachedSkillsFromOptions(opts); len(skills) > 0 {
		_ = c.ProjectSkills(workingDir, skills)
	}

	cleanupFiles, err := prepareCursorProjectFiles(workingDir, systemPrompt, opts, ownerSessionID)
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
		// NOTE: --force and --approve-mcps do NOT reliably suppress cursor's
		// per-tool-call "Run this MCP tool?" gate (verified repeatedly — they
		// affect server/command approval, not the per-call MCP prompt). Do not
		// remove the tmux-pane auto-allowlist in waitForCursorInteractiveResponse
		// (hasCursorMCPToolApprovalPrompt → send Tab) on the assumption these
		// flags handle it; that prompt-scrape is the only dependable mechanism.
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

func prepareCursorProjectFiles(workingDir, systemPrompt string, opts *llmtypes.CallOptions, ownerSessionID string) (func(), error) {
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

	denyBuiltin := false
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		denyBuiltin, _ = opts.Metadata.Custom[MetadataKeyDenyBuiltinTools].(bool)
	}
	systemPrompt = cursorBridgeOnlySystemPrompt(systemPrompt, denyBuiltin)

	// cursor-agent uses .git as the workspace-root marker for its
	// .cursor/mcp.json discovery — it walks UP from cwd looking for
	// .git and only loads .cursor/mcp.json next to the FIRST .git it
	// finds. When the workflow folder sits inside a parent repo (e.g.
	// builder-go's workspace-docs/), cursor walks past our workflow
	// folder and lands on the parent repo, which has no
	// .cursor/mcp.json — the api-bridge MCP server then never loads
	// in the session and the model reports "MCP not exposed to this
	// chat". Dropping an empty .git/ in the workflow folder makes
	// cursor treat it as its own workspace root and the .cursor/mcp.json
	// next to it is discovered. workspace-docs is gitignored upstream
	// so this marker has no side-effect on the parent repo. Cleanup
	// removes it on session end.
	gitMarkerDir := filepath.Join(workingDir, ".git")
	gitMarkerCreated := false
	if _, err := os.Stat(gitMarkerDir); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(gitMarkerDir, 0o755); mkErr == nil {
			gitMarkerCreated = true
			addCleanup(func() {
				if gitMarkerCreated {
					_ = os.Remove(gitMarkerDir)
				}
			})
		}
	}

	cursorDir := filepath.Join(workingDir, ".cursor")
	if strings.TrimSpace(systemPrompt) != "" {
		rulesDir := filepath.Join(cursorDir, "rules")
		if err := os.MkdirAll(rulesDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create Cursor rules dir: %w", err)
		}
		// Fixed filename — only one cursor chat owns a workflow folder
		// at a time, so no need to disambiguate via per-session hex.
		// The adapter's cleanup callback removes this file on session
		// end; if a session crashed and left it behind, the next
		// session overwrites it cleanly.
		rulePath := filepath.Join(rulesDir, "mlp-system.mdc")
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
			cleanup, err := writeCursorRestoredFile(filepath.Join(cursorDir, "cli.json"), []byte(configJSON), cursorRestoreProjectFilesFromOptions(opts))
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
			cleanup, err := writeCursorRestoredFile(filepath.Join(cursorDir, "mcp.json"), []byte(mcpJSON), cursorRestoreProjectFilesFromOptions(opts))
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
		}
		if denyBuiltin {
			cleanup, err := writeCursorDenyBuiltinHooks(cursorDir, cursorRestoreProjectFilesFromOptions(opts))
			if err != nil {
				cleanupAll()
				return nil, err
			}
			addCleanup(cleanup)
			// Hooks alone are not enough: cursor v2026.05.24+ evaluates
			// .cursor/cli.json permissions BEFORE prompting the user, and
			// only consults the hook AFTER the user has approved. Without
			// a permission-level deny, cursor shows "Run this command?
			// Not in allowlist: cat ..." every time the model wants to
			// shell out. Installing a deny-only cli.json forecloses the
			// built-in tools at the permission gate so cursor never
			// prompts and immediately tells the model to use the bridge.
			// Skip when caller supplied their own cli.json (MetadataKeyProjectConfig
			// was already written above) — caller's choices win.
			if _, callerSuppliedCLI := opts.Metadata.Custom[MetadataKeyProjectConfig].(string); !callerSuppliedCLI {
				cliCleanup, err := writeCursorDenyBuiltinPermissionsCLI(cursorDir, cursorRestoreProjectFilesFromOptions(opts))
				if err != nil {
					cleanupAll()
					return nil, err
				}
				addCleanup(cliCleanup)
			}
		}
	}

	// Final teardown: nuke the whole .cursor/ tree. Registered LAST so it
	// fires FIRST in LIFO order, making the earlier per-file restore
	// callbacks no-ops on already-gone files. The intent is a clean wipe
	// between sessions — orphaned hook scripts, denial logs, or cli.json
	// from a prior session whose cleanup callback didn't fire (orchestrator
	// killed before close) would otherwise leak. Trade-off: an operator's
	// own pre-existing content under .cursor/ is destroyed.
	if strings.TrimSpace(workingDir) != "" {
		addCleanup(func() { _ = os.RemoveAll(cursorDir) })
	}

	return cleanupAll, nil
}

// writeCursorDenyBuiltinHooks installs a .cursor/hooks.json + deny script
// that blocks cursor's built-in filesystem, shell, edit, search, and
// delegation tools via cursor's hook system
// (https://cursor.com/docs/hooks). The model is forced to call the MCP
// bridge instead (api-bridge.execute_shell_command, api-bridge.read_file)
// when the orchestrator has injected the bridge mcp.json.
//
// Cleanup restores any pre-existing hooks.json the operator had in their
// workspace and removes our deny script + the hooks/ subdir if we created
// them. Order matters: write-then-restore composes cleanly with the rest
// of prepareCursorProjectFiles's cleanup stack.
func writeCursorDenyBuiltinHooks(cursorDir string, restorePrior bool) (func(), error) {
	hooksDir := filepath.Join(cursorDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create cursor hooks dir: %w", err)
	}
	scriptPath := filepath.Join(hooksDir, "mlp-deny-builtin.sh")
	// Heredoc emits the deny JSON cursor expects on stdout per its hook
	// schema (https://cursor.com/docs/hooks). Exit code 0 + valid JSON
	// tells cursor to obey the permission verdict. Exit code 2 would
	// also deny, but emitting the verdict explicitly lets us include a
	// user_message that helps debug "why didn't cursor run my command".
	logPath := filepath.Join(hooksDir, "mlp-deny-builtin-denials.jsonl")
	script := `#!/bin/bash
# Installed by the multi-llm-provider-go cursor adapter when
# WithDenyBuiltinTools is enabled. Denies cursor's built-in filesystem,
# shell, edit, and delegation tools so the agent routes through the MCP bridge
# (api-bridge.*) instead.
input=$(cat)
printf '%s\n' "$input" >> ` + cursorShellQuote(logPath) + `
cat <<'JSON'
{"permission":"deny","user_message":"Built-in filesystem/shell/edit/search/delegation tools are disabled in this session by the orchestrator. Use the MCP bridge tools instead.","agent_message":"Built-in filesystem/shell/edit/search/delegation tools are DENIED. Do not start Cursor subagents, background agents, cloud agents, workers, delegated agents, or request a mode switch. You DO have full access via the api-bridge MCP server (your environment carries valid MCP_API_URL + MCP_API_TOKEN). Use these EXACT bridge tools — they cover everything you need:\n  • api-bridge.execute_shell_command(command, timeout?) — run any shell command (cat, ls, grep, find, jq, python3, curl, etc.). Output goes to stdout/stderr/exit_code.\n  • api-bridge.diff_patch_workspace_file(filepath, diff) — apply a unified diff to any workspace file. Use this INSTEAD of Edit/Write.\n  • api-bridge.get_api_spec(server_name, tool_name) — discover schemas for the other MCP servers (e.g., google_sheets, playwright) so you can call them via execute_shell_command + curl/python.\nDo NOT report 'no MCP server configuration' or 'no API tokens' — the bridge is configured and ready. Always pick a bridge tool over giving up."}
JSON
exit 0
`
	previousScript, scriptExisted := readPreviousCursorFileIf(restorePrior, scriptPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return nil, fmt.Errorf("failed to write cursor deny-builtin script: %w", err)
	}
	hooksConfig := `{
  "version": 1,
  "hooks": {
    "preToolUse": [{"command": "./.cursor/hooks/mlp-deny-builtin.sh", "matcher": "` + cursorBridgeOnlyDeniedToolMatcher() + `", "failClosed": true}],
    "beforeShellExecution": [{"command": "./.cursor/hooks/mlp-deny-builtin.sh", "failClosed": true}],
    "beforeReadFile": [{"command": "./.cursor/hooks/mlp-deny-builtin.sh", "failClosed": true}]
  }
}
`
	hooksPath := filepath.Join(cursorDir, "hooks.json")
	previousHooks, hooksExisted := readPreviousCursorFileIf(restorePrior, hooksPath)
	if err := os.WriteFile(hooksPath, []byte(hooksConfig), 0o600); err != nil {
		_ = os.Remove(scriptPath)
		if !scriptExisted {
			_ = os.Remove(hooksDir)
		}
		return nil, fmt.Errorf("failed to write cursor hooks.json: %w", err)
	}
	return func() {
		// Restore (or remove) hooks.json first so cursor stops obeying our
		// deny verdict immediately, then clean the script + dirs.
		if hooksExisted {
			_ = os.WriteFile(hooksPath, previousHooks, 0o600)
		} else {
			_ = os.Remove(hooksPath)
		}
		// logPath lives inside hooksDir, so remove it BEFORE attempting
		// to remove hooksDir — otherwise hooksDir stays (not empty),
		// which in turn keeps cursorDir non-empty and the final cleanup
		// fails. Best-effort: ignore the error if the file isn't there.
		_ = os.Remove(logPath)
		if scriptExisted {
			_ = os.WriteFile(scriptPath, previousScript, 0o755)
		} else {
			_ = os.Remove(scriptPath)
			_ = os.Remove(hooksDir)
		}
		_ = os.Remove(cursorDir)
	}, nil
}

// writeCursorDenyBuiltinPermissionsCLI installs a .cursor/cli.json
// whose permissions.deny rules block cursor's built-in filesystem,
// shell, edit, search, and delegation tools at the permission gate that
// cursor evaluates
// BEFORE prompting the user (and before the hooks.json hook runs).
//
// Why this is needed in addition to writeCursorDenyBuiltinHooks:
// cursor v2026.05.24+ shows "Run this command? Not in allowlist: cat ..."
// when a model invokes a built-in shell command that isn't pre-approved.
// The hook only runs after the user accepts that prompt. By denying at
// the permission layer here, cursor skips the prompt and immediately
// reports back to the model that the tool is denied, forcing it to
// pick the MCP bridge tool (api-bridge.execute_shell_command, etc.).
//
// Patterns are coarse wildcards. We don't try to model the full cursor
// permission grammar — just blanket-deny the built-ins we expect the
// bridge to substitute. Restoration on cleanup preserves any
// pre-existing operator cli.json byte-for-byte.
func writeCursorDenyBuiltinPermissionsCLI(cursorDir string, restorePrior bool) (func(), error) {
	if err := os.MkdirAll(cursorDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create cursor dir: %w", err)
	}
	cliPath := filepath.Join(cursorDir, "cli.json")
	previousCLI, cliExisted := readPreviousCursorFileIf(restorePrior, cliPath)
	denyConfig := struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}{}
	denyConfig.Permissions.Allow = []string{}
	denyConfig.Permissions.Deny = cursorBridgeOnlyDeniedPermissionPatterns()
	denyConfigBytes, err := json.MarshalIndent(denyConfig, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cursor cli.json deny config: %w", err)
	}
	denyConfigBytes = append(denyConfigBytes, '\n')
	if err := os.WriteFile(cliPath, denyConfigBytes, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write cursor cli.json: %w", err)
	}
	return func() {
		if cliExisted {
			_ = os.WriteFile(cliPath, previousCLI, 0o600)
		} else {
			_ = os.Remove(cliPath)
		}
		_ = os.Remove(cursorDir)
	}, nil
}

// readPreviousCursorFile reads an existing file or returns nil + false if
// it doesn't exist. Errors other than ENOENT are treated as "didn't exist"
// — cleanup must be best-effort; we never want a hook-restore step to fail
// session teardown.
func readPreviousCursorFile(path string) ([]byte, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return content, true
}

// readPreviousCursorFileIf gates byte-restore capture on the OFF-by-default
// restore flag. When restore is false (the default), it reports the file as
// non-existent so cleanup deletes whatever we wrote rather than resurrecting
// prior operator content.
func readPreviousCursorFileIf(restore bool, path string) ([]byte, bool) {
	if !restore {
		return nil, false
	}
	return readPreviousCursorFile(path)
}

func writeCursorRestoredFile(path string, content []byte, restorePrior bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create Cursor config dir: %w", err)
	}
	var previous []byte
	existed := false
	if restorePrior {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			previous, existed = data, true
		} else if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("failed to read existing Cursor config %s: %w", path, readErr)
		}
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
	session, ok := cursorPersistentRegistry.Delete(ownerSessionID)
	if !ok || session == nil {
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	closeCursorSessionLocked(session, reason, logger)
}

// CloseCursorCLIInteractiveSessionForOwner closes the persistent cursor
// interactive session for the given owner. See agycli's equivalent
// CloseAgyCLIInteractiveSessionForOwner for the mid-chat-prompt-change
// motivation.
func CloseCursorCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	closeCursorPersistentSession(ownerSessionID, reason, nil)
}

// CloseCursorCLIInteractiveSessionByTmux closes the persistent cursor
// interactive session whose backing tmux session matches tmuxSessionName,
// regardless of the owner key it was registered under. Teardown backstop for
// when the owning session ID is unknown or has drifted. Delegates to the
// owner-keyed close so the same graceful exit + cleanup runs. No-op when no
// live session matches.
func CloseCursorCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	name := strings.TrimSpace(tmuxSessionName)
	if name == "" {
		return
	}
	owner, _, ok := cursorPersistentRegistry.Find(func(session *cursorInteractiveSession) bool {
		return session != nil && session.tmuxSessionName == name
	})
	if !ok || owner == "" {
		return
	}
	closeCursorPersistentSession(owner, reason, nil)
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
	cursorPersistentRegistry.DeleteIf(ownerSessionID, session)
}

func CleanupCursorCLIInteractiveSessions(ctx context.Context) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil
	}
	sessions := cursorPersistentRegistry.Drain()

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
	cursorInteractiveRegistry.Set(ownerSessionID, tmuxSessionName)
}

func unregisterCursorInteractiveSession(ownerSessionID, tmuxSessionName string) {
	cursorInteractiveRegistry.DeleteIf(ownerSessionID, tmuxSessionName)
}

func activeCursorInteractiveSession(ownerSessionID string) (string, bool) {
	sessionName, ok := cursorInteractiveRegistry.Get(ownerSessionID)
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
	shellCommand := "cd " + cursorShellQuote(workingDir) + " && exec " + cursorShellJoin(args)
	var cleanupLaunchScript func()
	if len(env) > 0 {
		var err error
		shellCommand, cleanupLaunchScript, err = shelllaunch.CommandWithEnv(args, workingDir, env)
		if err != nil {
			return fmt.Errorf("failed to prepare Cursor launch environment: %w", err)
		}
	} else {
		cleanupLaunchScript = func() {}
	}

	tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
	tmuxArgs = append(tmuxArgs, tmuxsize.Args()...)
	tmuxArgs = append(tmuxArgs, shellCommand)
	if err := runCursorCommand(ctx, nil, "tmux", tmuxArgs...); err != nil {
		cleanupLaunchScript()
		return fmt.Errorf("failed to start Cursor interactive session %q: %w", sessionName, err)
	}
	_ = runCursorCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "remain-on-exit", "on")
	if err := runCursorCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "history-limit", tmuxexec.DefaultHistoryLimit); err != nil {
		return fmt.Errorf("failed to configure Cursor tmux history for session %q: %w", sessionName, err)
	}
	// Pin the window size to manual so the detached session keeps the size we
	// launched at instead of collapsing to default-size (80x24), which reflows
	// the TUI into half-width and makes the captured pane unreadable.
	_ = runCursorCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "window-size", "manual")
	_ = runCursorCommand(ctx, nil, "tmux", "set-option", "-t", sessionName, "focus-events", "on")
	return nil
}

func waitForCursorPrompt(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk) error {
	deadline, cancel := context.WithTimeout(ctx, cursorInteractivePromptWait())
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var trustSubmitted bool
	var lastMCPServerApprovalAt time.Time
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	// Debounce the ready signal: require it to hold across two
	// consecutive captures (~400ms). Single-tick readiness has
	// false-positived on cursor's cold-start banner where the
	// placeholder paints before the input field is interactive.
	var consecutiveReadyTicks int
	var bootBannerReadySince time.Time
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
				consecutiveReadyTicks = 0
				continue
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamCursorTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			visible := cursorVisiblePaneText(captured)
			if hasCursorTrustPrompt(visible) && !trustSubmitted {
				_ = runCursorCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, cursorTrustPromptResponse(visible))
				trustSubmitted = true
				consecutiveReadyTicks = 0
				bootBannerReadySince = time.Time{}
				continue
			}
			if hasCursorMCPServerApprovalPrompt(visible) {
				if lastMCPServerApprovalAt.IsZero() || time.Since(lastMCPServerApprovalAt) >= time.Second {
					if err := runCursorCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, cursorMCPServerApprovalResponse(visible)); err == nil {
						lastMCPServerApprovalAt = time.Now()
					}
				}
				consecutiveReadyTicks = 0
				bootBannerReadySince = time.Time{}
				continue
			}
			cleaned := strings.ToLower(stripCursorANSI(visible))
			if cursorBootBannerAcceptableAfterGrace(cleaned) {
				consecutiveReadyTicks = 0
				if bootBannerReadySince.IsZero() {
					bootBannerReadySince = time.Now()
					continue
				}
				if time.Since(bootBannerReadySince) >= cursorBootBannerPromptGrace {
					return nil
				}
				continue
			}
			bootBannerReadySince = time.Time{}
			if hasCursorReadyPrompt(captured) {
				consecutiveReadyTicks++
				if consecutiveReadyTicks >= 2 {
					return nil
				}
				continue
			}
			consecutiveReadyTicks = 0
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
	// Raw paste (no -p): -p enables bracketed paste, which Cursor collapses to an
	// opaque "[Pasted text #N]" block in the pane. -r preserves embedded LFs as
	// literal newlines (not CR), so multi-line input stays in the draft without
	// submitting early, while the actual text stays readable in the terminal.
	if err := runCursorCommand(ctx, nil, "tmux", "paste-buffer", "-d", "-r", "-b", bufferName, "-t", sessionName); err != nil {
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
	// Verify-then-recover: return as soon as the draft leaves the input field
	// (checked immediately, then every 50ms — the old single 150ms-delayed probe
	// added a fixed 150ms to every send). If the draft is still sitting there
	// after a grace period (the follow-ups menu swallowed the first Enter), send
	// one recovery Enter and keep verifying until it clears or the deadline hits.
	const recoveryGrace = 250 * time.Millisecond
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	started := time.Now()
	recovered := false
	for {
		captured, err := captureCursorPane(deadline, sessionName)
		if err == nil {
			if !cursorPaneShowsPromptDraft(captured, message) {
				return
			}
			if !recovered && time.Since(started) >= recoveryGrace {
				recovered = true
				_ = runCursorCommand(deadline, nil, "tmux", "send-keys", "-t", sessionName, "C-m")
			}
		}
		select {
		case <-deadline.Done():
			return
		case <-ticker.C:
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
		captured, err := captureCursorPane(deadline, sessionName)
		if err == nil && cursorPaneShowsPromptDraft(captured, message) {
			return
		}
		select {
		case <-deadline.Done():
			return
		case <-ticker.C:
		}
	}
}

func waitForCursorInteractiveResponse(ctx context.Context, sessionName, baseline, prompt string, historicalAssistantTexts []string, streamChan chan<- llmtypes.StreamChunk, autoApproveWebSearch bool) (string, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	waitStartedAt := time.Now()
	firstActivityTimeout := cursorInteractiveFirstActivityTimeout()
	stalePaneBackstop := cursorInteractiveStalePaneBackstop()
	var sawActivity bool
	var idleSince time.Time
	var readyWithoutContentSince time.Time
	var submitRetryCount int
	var lastSubmitRetryAt time.Time
	var lastCaptured string
	var lastTerminalSnapshot string
	var lastTerminalStreamedAt time.Time
	var lastWebSearchApprovalAt time.Time
	var lastMCPToolApprovalAt time.Time
	var lastModeSwitchRejectAt time.Time
	// Stale-pane backstop tracking: the raw capture from the previous tick and
	// the time it last changed. This is tracked at the top of every tick,
	// independent of all the branch logic below, so a prompt-detection bug that
	// keeps the loop in a "not ready" branch can never suppress it.
	var backstopPrevCapture string
	var paneUnchangedSince time.Time
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
			// Stale-pane backstop. Independent of hasCursorReadyPrompt and every
			// branch below: if the pane has produced activity and then frozen
			// (byte-identical) for longer than the backstop, the turn is over but
			// completion detection failed to recognize it (e.g. a stale status
			// line or leftover spinner frame holding the pane "not ready"). Extract
			// whatever response is present and return it rather than hang forever.
			// Gated on sawActivity so a never-delivered prompt (pane frozen at
			// baseline) falls through to the first-activity timeout instead.
			if captured != backstopPrevCapture {
				backstopPrevCapture = captured
				paneUnchangedSince = time.Now()
			} else if sawActivity && stalePaneBackstop > 0 && !paneUnchangedSince.IsZero() &&
				time.Since(paneUnchangedSince) >= stalePaneBackstop {
				content := parseCursorInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
				if strings.TrimSpace(content) == "" {
					content = forcedCursorInteractiveResponse(captured, baseline, prompt, historicalAssistantTexts)
				}
				if strings.TrimSpace(content) != "" {
					return captured, nil
				}
				return captured, fmt.Errorf("Cursor Agent CLI pane went unchanged for %s after activity but no ready prompt or visible assistant output was detected; latest pane:\n%s", stalePaneBackstop, captured)
			}
			if streamChan != nil && streamTerminalScreen {
				if time.Since(lastTerminalStreamedAt) >= time.Second && streamCursorTerminalSnapshot(ctx, sessionName, streamChan, &lastTerminalSnapshot) {
					lastTerminalStreamedAt = time.Now()
				}
			}
			// Cursor can ask to switch into another agent mode when it tries
			// nested delegation and finds the child session's built-in tools
			// blocked. In bridge-only sessions, do not approve that transition:
			// child Cursor agents do not reliably inherit our scoped MCP bridge
			// config. Reject it so the parent session can continue or fail
			// through the normal denied-tool path instead of hanging.
			if hasCursorModeSwitchPrompt(captured) {
				if lastModeSwitchRejectAt.IsZero() || time.Since(lastModeSwitchRejectAt) >= time.Second {
					if err := runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "n"); err == nil {
						lastModeSwitchRejectAt = time.Now()
					}
				}
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			// Auto-allowlist MCP tool calls. Cursor gates each MCP tool call
			// behind a "Run this MCP tool?" prompt, and --force/--approve-mcps do
			// NOT reliably suppress it (see buildCursorInteractiveLaunch), so this
			// pane-scrape is the only dependable way through. In an orchestrated
			// bridge session the tools must run, so press Tab (Allowlist MCP Tool)
			// — observed to clear the current gate; cursor re-prompts per call, so
			// this fires again on each reappearance (rate-limited). Without it the
			// turn stalls here forever, and the "→ Run (once)" line otherwise looks
			// like a ready prompt, so the loop could even report a bogus completion.
			if hasCursorMCPToolApprovalPrompt(captured) {
				if lastMCPToolApprovalAt.IsZero() || time.Since(lastMCPToolApprovalAt) >= time.Second {
					if err := runCursorCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "Tab"); err == nil {
						lastMCPToolApprovalAt = time.Now()
					}
				}
				sawActivity = true
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if autoApproveWebSearch && hasCursorWebAccessApprovalPrompt(captured) {
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
			if !sawActivity {
				// The CLI has shown nothing since the prompt was submitted.
				// Every completion/failsafe path below requires sawActivity, so
				// without this cap a never-delivered prompt spins here forever
				// (the call context has no deadline by default). Fail cleanly so
				// the step surfaces an error instead of hanging.
				if firstActivityTimeout > 0 && time.Since(waitStartedAt) >= firstActivityTimeout {
					captured, _ := captureCursorPane(context.Background(), sessionName)
					return captured, fmt.Errorf("Cursor Agent CLI produced no activity within %s of submitting the prompt — the input was likely not delivered to the tmux pane; latest pane:\n%s", firstActivityTimeout, captured)
				}
				idleSince = time.Time{}
				lastCaptured = captured
				continue
			}
			if !hasCursorReadyPrompt(captured) {
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
		roleLine := normalizeCursorPaneLineWithoutAssistantLabel(line)
		trimmed := normalizeCursorAssistantTurnLabel(roleLine)
		if isCursorPromptBoundaryLine(trimmed) {
			break
		}
		// "User: …" marks the start of a user turn. Everything previously
		// collected is from an older assistant turn and must be discarded —
		// otherwise a multi-turn pane (where baseline-diff falls back to
		// line-prefix mode and leaves prior turns in the delta) leaks the
		// stale reply into the new turn's extracted text.
		if isCursorUserTurnHeader(roleLine) {
			out = out[:0]
			continue
		}
		if assistantLine, ok := stripCursorAssistantTurnHeader(roleLine); ok {
			out = out[:0]
			trimmed = assistantLine
			if trimmed == "" {
				continue
			}
		}
		if trimmed == "" {
			// Preserve blank lines as paragraph-break markers (collapse runs),
			// but never start the response with a blank.
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		// A line beginning with a Braille spinner glyph is the CLI's live/stale
		// generation status (e.g. "⣾ Generating…"); never assistant content, so
		// skip it — otherwise the loop can return "Generating…" as the answer.
		if cursorLineStartsWithSpinner(strings.TrimSpace(trimmed)) {
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

func normalizeCursorPaneLineWithoutAssistantLabel(line string) string {
	line = strings.TrimSpace(stripCursorANSI(line))
	line = strings.TrimPrefix(line, "│")
	line = strings.TrimSuffix(line, "│")
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.TrimPrefix(line, "• "))
	return line
}

func normalizeCursorAssistantTurnLabel(line string) string {
	// Cursor labels each assistant turn with a literal "Assistant:" header. Strip
	// it so the kept response reads as plain prose (and matches what the user
	// sees in the chat panel for Claude/Gemini, which have no such label).
	line = strings.TrimSpace(strings.TrimPrefix(line, "Assistant:"))
	return line
}

func stripCursorAssistantTurnHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "assistant:") {
		return "", false
	}
	if len(trimmed) == len("Assistant:") {
		return "", true
	}
	next := trimmed[len("Assistant:")]
	if next != ' ' && next != '\t' {
		return "", false
	}
	return strings.TrimSpace(trimmed[len("Assistant:"):]), true
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
	// A "historical reply" leaks into the new turn's extracted text as a
	// complete leading chunk that ends at a line boundary (because each
	// turn renders on its own line in cursor's pane). A prefix match that
	// runs INTO more characters on the same line is not a leak — it's
	// legitimate new content that happens to start with the same tokens
	// as a prior reply. Without this boundary check, a reply like
	// "WIDGET_A47, WIDGET_B23" got eaten down to ", WIDGET_B23" because
	// "WIDGET_A47" matched a prior turn (observed in
	// TestMultiTurnChatE2E_Cursor before this guard).
	if strings.HasPrefix(text, historical) {
		remainder := text[len(historical):]
		if remainder == "" || strings.HasPrefix(remainder, "\n") {
			return remainder, true
		}
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
			remainder := text[len(suffix):]
			if remainder == "" || strings.HasPrefix(remainder, "\n") {
				return remainder, true
			}
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
	visible := cursorVisiblePaneText(captured)
	if hasCursorTrustPrompt(visible) {
		return false
	}
	if hasCursorWebAccessApprovalPrompt(visible) {
		return false
	}
	// The MCP-tool approval prompt renders a "→ Run (once)" line whose arrow
	// otherwise looks like a ready input marker — never treat it as ready, or the
	// loop reports a bogus completion while the tool call is still gated.
	if hasCursorMCPToolApprovalPrompt(visible) {
		return false
	}
	if hasCursorMCPServerApprovalPrompt(visible) {
		return false
	}
	if hasCursorModeSwitchPrompt(visible) {
		return false
	}
	cleaned := strings.ToLower(stripCursorANSI(visible))
	if !hasCursorReadyMarker(cleaned) {
		return false
	}
	// Cold-start welcome banner (Cursor v2026.05.24+ Composer 2.5)
	// paints "→ Plan, search, build anything" before the input field
	// is actually interactive. Submitting at that point silently drops
	// the keystrokes. Treat the welcome banner as not-ready; the wait
	// loop continues until the banner state passes.
	if hasCursorBootBanner(cleaned) {
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
	if hasCursorActivity(visible) && !strings.Contains(cleaned, "→") {
		return false
	}
	return true
}

// hasCursorBootBanner detects the Composer 2.5 welcome screen that
// cursor-agent paints at process start. Its placeholder line contains
// the same "→" + "plan, search, build anything" tokens our ready
// marker keys off, so without this guard `hasCursorReadyPrompt` would
// fire prematurely and the first user prompt would land on a dead
// input field. The banner is distinguished by the persistent header
// trio "cursor agent" + version line + "/skills" tagline, which only
// appear before any conversation has scrolled them off.
func hasCursorBootBanner(cleaned string) bool {
	if !strings.Contains(cleaned, "cursor agent") {
		return false
	}
	if !strings.Contains(cleaned, "use /skills") {
		return false
	}
	// Composer 2.5 ships its version line right under the banner.
	// Either the version line OR the "plan, search, build anything"
	// placeholder confirms we're still on the welcome screen rather
	// than a post-conversation "Add a follow-up" pane.
	return strings.Contains(cleaned, "plan, search, build anything")
}

func cursorBootBannerAcceptableAfterGrace(cleaned string) bool {
	return hasCursorBootBanner(cleaned) &&
		hasCursorReadyMarker(cleaned) &&
		!hasCursorLiveGenerationActivity(cleaned)
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
	cleaned := strings.ToLower(stripCursorANSI(cursorVisiblePaneText(captured)))
	if strings.Contains(cleaned, "trusting workspace") {
		return false
	}
	return strings.Contains(cleaned, "workspace trust required") ||
		strings.Contains(cleaned, "do you trust the contents of this directory") ||
		strings.Contains(cleaned, "trust") && strings.Contains(cleaned, "workspace") &&
			(strings.Contains(cleaned, "y/n") || strings.Contains(cleaned, "yes") ||
				strings.Contains(cleaned, "[a]") || strings.Contains(cleaned, "[w]"))
}

func hasCursorModeSwitchPrompt(captured string) bool {
	cleaned := strings.ToLower(stripCursorANSI(cursorVisiblePaneText(captured)))
	return strings.Contains(cleaned, "switch to agent mode") &&
		(strings.Contains(cleaned, "approve mode switch") ||
			strings.Contains(cleaned, "reject") ||
			strings.Contains(cleaned, "mode switch")) &&
		(strings.Contains(cleaned, "built-in shell/read tools are blocked") ||
			strings.Contains(cleaned, "subagent session") ||
			strings.Contains(cleaned, "mcp bridge"))
}

func hasCursorWebSearchApprovalPrompt(captured string) bool {
	return hasCursorWebAccessApprovalPrompt(captured)
}

func hasCursorWebAccessApprovalPrompt(captured string) bool {
	cleaned := strings.ToLower(stripCursorANSI(cursorVisiblePaneText(captured)))
	return strings.Contains(cleaned, "allow this web search") ||
		strings.Contains(cleaned, "allow search (y)") ||
		strings.Contains(cleaned, "web search:") && strings.Contains(cleaned, "allow") ||
		strings.Contains(cleaned, "allow this web fetch") ||
		strings.Contains(cleaned, "web fetch:") && strings.Contains(cleaned, "allow") ||
		strings.Contains(cleaned, "fetch (y)") && strings.Contains(cleaned, "web fetch") ||
		strings.Contains(cleaned, "open this url") ||
		strings.Contains(cleaned, "open url") && strings.Contains(cleaned, "(y)") ||
		strings.Contains(cleaned, "allow this url") ||
		strings.Contains(cleaned, "allow opening") && strings.Contains(cleaned, "url") ||
		strings.Contains(cleaned, "open link") && strings.Contains(cleaned, "(y)") ||
		hasCursorOpenURLCommandApprovalText(cleaned)
}

func hasCursorOpenURLCommandApprovalText(cleaned string) bool {
	hasURL := strings.Contains(cleaned, "https://") || strings.Contains(cleaned, "http://")
	if !hasURL || !strings.Contains(cleaned, "run this command?") {
		return false
	}
	return strings.Contains(cleaned, "$ open ") ||
		strings.Contains(cleaned, "not in allowlist: open") ||
		strings.Contains(cleaned, "shell(open)")
}

// hasCursorMCPToolApprovalPrompt detects Cursor's per-tool-call approval gate:
//
//	Run this MCP tool?
//	 → Run (once) (y)
//	   Allowlist MCP Tool (tab)
//	   Reject & propose changes (p)
//	   Skip (esc or n)
//
// It appears for every MCP tool call unless cursor is launched with --force.
func hasCursorMCPToolApprovalPrompt(captured string) bool {
	cleaned := strings.ToLower(stripCursorANSI(cursorVisiblePaneText(captured)))
	return strings.Contains(cleaned, "run this mcp tool") ||
		strings.Contains(cleaned, "allowlist mcp tool")
}

// hasCursorMCPServerApprovalPrompt detects startup MCP server gates. These
// happen before the user's message is submitted; if they are left unresolved,
// Composer may build the turn's tool list without api-bridge.
func hasCursorMCPServerApprovalPrompt(captured string) bool {
	cleaned := strings.ToLower(stripCursorANSI(cursorVisiblePaneText(captured)))
	if strings.Contains(cleaned, "run this mcp tool") ||
		strings.Contains(cleaned, "allowlist mcp tool") {
		return false
	}
	if strings.Contains(cleaned, "mcp server") &&
		(strings.Contains(cleaned, "approve") ||
			strings.Contains(cleaned, "allow") ||
			strings.Contains(cleaned, "enable") ||
			strings.Contains(cleaned, "trust")) {
		return true
	}
	if strings.Contains(cleaned, "api-bridge") &&
		strings.Contains(cleaned, "mcp") &&
		(strings.Contains(cleaned, "approve") ||
			strings.Contains(cleaned, "allow") ||
			strings.Contains(cleaned, "enable") ||
			strings.Contains(cleaned, "[y]") ||
			strings.Contains(cleaned, "(y)")) {
		return true
	}
	return false
}

func cursorMCPServerApprovalResponse(captured string) string {
	cleaned := strings.ToLower(stripCursorANSI(captured))
	if strings.Contains(cleaned, "[a]") || strings.Contains(cleaned, "all mcp") || strings.Contains(cleaned, "enable all") {
		return "a"
	}
	return "y"
}

func cursorTrustPromptResponse(captured string) string {
	cleaned := strings.ToLower(stripCursorANSI(captured))
	if strings.Contains(cleaned, "[a]") || strings.Contains(cleaned, "trust this workspace, but don't enable all mcp servers") {
		return "a"
	}
	return "y"
}

func hasCursorActivity(captured string) bool {
	for _, line := range strings.Split(stripCursorANSI(cursorVisiblePaneText(captured)), "\n") {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		// A Braille spinner glyph at the start of a line means the CLI is
		// mid-animation (e.g. "⣾ Generating…"); treat it as activity directly.
		if cursorLineStartsWithSpinner(lower) {
			return true
		}
		// Match status words even when prefixed by a spinner/bullet glyph.
		keyword := cursorActivityKeyword(lower)
		if strings.Contains(lower, "esc to interrupt") ||
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

func cursorVisiblePaneText(captured string) string {
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

func streamCursorTerminalSnapshot(ctx context.Context, sessionName string, streamChan chan<- llmtypes.StreamChunk, lastTerminalSnapshot *string) bool {
	snapshot, err := captureCursorPaneForDisplay(ctx, sessionName)
	if err != nil {
		return false
	}
	snapshot = strings.TrimRight(stripCursorANSIPreserveColors(snapshot), "\n")
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
	// Preserve tmux scrollback for browser/UI history. We intentionally do NOT
	// send C-l (0x0C) anymore: Cursor's raw-mode TUI
	// catches that keystroke as "clear display", which wipes the visible
	// chat history the operator is watching in the browser terminal pane.
	// Memory is bounded by the session history-limit, and per-turn parsing is
	// anchored to the captured baseline.
	// Baseline-diff logic in cursorCapturedAfterBaseline tolerates an
	// already-populated pane via LastIndex(captured, baseline).
}

func captureCursorPane(ctx context.Context, sessionName string) (string, error) {
	return tmuxexec.CapturePane(ctx, sessionName, tmuxexec.DefaultScrollbackLines)
}

func captureCursorPaneForDisplay(ctx context.Context, sessionName string) (string, error) {
	// -e preserves ANSI SGR (color, bold, dim, etc.) so the frontend can
	// colorize the snapshot via ansi_up. Cursor positioning sequences are
	// stripped by stripCursorANSIPreserveColors before the snapshot leaves
	// the adapter so they don't garble the rendered output.
	// -J joins wrapped lines so the frontend can handle wrapping natively without
	// hard splitting words mid-line.
	return tmuxexec.CapturePaneANSI(ctx, sessionName, tmuxexec.DefaultScrollbackLines)
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
	// Reap the pane process trees (CLI + spawned MCP node subprocesses) before
	// killing the session — kill-session only SIGHUPs the pane process, so the
	// children would otherwise orphan and leak.
	tmuxcontrol.ReapSessionProcessTree(ctx, sessionName)
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
	if llmtypes.IsCodingAgentTmuxSessionLostError(err) {
		return true
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

// cursorInteractiveFirstActivityTimeout bounds the wait for the CLI's first sign
// of activity after the prompt is submitted. Always > 0 so a lost-input hang
// fails cleanly even when caller and provider both run without a turn deadline.
func cursorInteractiveFirstActivityTimeout() time.Duration {
	return cursorDurationFromEnv(EnvCursorInteractiveFirstActivityTimeoutSeconds, defaultCursorInteractiveFirstActivityTimeout)
}

// cursorInteractiveStalePaneBackstop bounds how long the response-wait loop will
// tolerate a byte-frozen pane after activity before returning, independent of
// prompt detection. Guards against a hang when hasCursorReadyPrompt never trips.
func cursorInteractiveStalePaneBackstop() time.Duration {
	return cursorDurationFromEnv(EnvCursorInteractiveStalePaneBackstopSeconds, defaultCursorInteractiveStalePaneBackstop)
}

// cursorBrailleSpinner reports whether r is one of the CLI's animated spinner
// glyphs (Unicode Braille Patterns, U+2800–U+28FF), e.g. the leading glyph in
// "⣾ Generating…". A line starting with one is a reliable "actively generating"
// signal that completed markers (▸ ● ○) are not.
func cursorBrailleSpinner(r rune) bool { return r >= 0x2800 && r <= 0x28FF }

// cursorLineStartsWithSpinner reports whether the trimmed, lowercased line begins
// with an animated Braille spinner glyph.
func cursorLineStartsWithSpinner(lower string) bool {
	for _, r := range lower {
		return cursorBrailleSpinner(r)
	}
	return false
}

// cursorActivityKeyword strips any leading spinner glyph / bullet / punctuation
// so a status word matches even when the live spinner prefixes it, e.g.
// "⣾ generating…" → "generating…".
func cursorActivityKeyword(lower string) string {
	return strings.TrimLeftFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) })
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
	return tmuxexec.RunCommandOutput(ctx, stdin, nil, name, args...)
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

// intPtrFromInt returns a *int for non-zero values, nil for zero.
// Used when populating GenerationInfo numeric fields; nil leaves the
// field unset rather than recording a misleading zero.
func intPtrFromInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
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

// stripCursorANSIPreserveColors strips ANSI cursor positioning / clear-screen
// sequences but preserves SGR (Select Graphic Rendition: color, bold, dim,
// underline, etc., terminated with `m`). The frontend feeds this output
// through ansi_up to colorize the rendered pane snapshot. Cursor positioning
// is dropped because ansi_up does not emulate VT100 movement.
func stripCursorANSIPreserveColors(s string) string {
	return paneview.StripANSIPreserveColors(s)
}
