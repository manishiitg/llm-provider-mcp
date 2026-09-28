package agycli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/shelllaunch"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"google.golang.org/protobuf/encoding/protowire"
)

// Persistent-turn execution inside the TUI sidecar. Builder chats set the
// persistent-interactive option: their turns run in the sidecar's own TUI
// conversation (paste prompt, await completion, extract reply), while
// steps/workflows stay on the headless exec lane. Verified live against
// agy 1.2.7:
//
//   - multiline prompts ride tmux load-buffer + paste-buffer -p (bracketed
//     paste); Enter submits. Single-line send-keys is only for short
//     follow-ups.
//   - MCP servers mounted after boot ARE visible to the running TUI, but
//     TUI tool calls need approval. Bridge tools are pre-approved with
//     permissions.allow entries of the form mcp(<mount>/*) — full-segment
//     wildcard only (mcp(*) and mcp(server/*) work; mcp(prefix-*) does
//     not). Permission edits are only trusted at boot, so the sidecar
//     reboots when the mount set changes.
//   - the TUI conversation id cannot be chosen (--conversation with an
//     unknown id warns and starts fresh), so it is discovered by content
//     match after the first turn and recorded for usage attribution; a
//     caller-supplied resume id instead attaches the boot via
//     --conversation (session-loss rebirth / cross-process restore).
//   - per-turn token usage comes from the conversation .db: type-15 steps
//     carry field 5.9 with input/output/thinking token counts (verified
//     equal to exec JSON usage on live turns). Tool steps carry no usage.

const (
	// agySidecarTurnTimeout bounds one sidecar turn; ctx cancels earlier.
	agySidecarTurnTimeout = 15 * time.Minute
	// agyApprovalPollInterval bounds how long a turn may sit on an
	// unexpected approval prompt before failing loudly.
	agyApprovalPollInterval = 2 * time.Second
)

// agyPaneNativeApprovalMarkers are TUI approval prompts that must never
// appear mid-turn in a wired session: bridge tools are pre-approved via
// permissions.allow, so anything asking here is a native tool the lane
// refuses to auto-answer.
var agyPaneNativeApprovalMarkers = []string{
	"Allow calling this tool?",
	"Allow access to this file?",
	agyPaneApprovalMarker,
}

// agyMountFingerprint identifies a sidecar's tool surface: the MCP config
// document, or "unmounted" when the turn carries no bridge.
func agyMountFingerprint(mcpJSON string) string {
	if strings.TrimSpace(mcpJSON) == "" {
		return "unmounted"
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(mcpJSON)))
	return "mounted-" + hex.EncodeToString(sum[:8])
}

// ensureAgyInteractiveSessionForTurn returns the owner's sidecar, rebooting
// it when the working dir, model, or mount set changed: MCP permissions
// are only trusted at boot, so a changed tool surface needs a fresh TUI.
// A dead tmux session rebirths (same-process session loss); an explicit
// resume conversation the live sidecar is not serving reboots with
// --conversation (cross-process restore). Mounts and permission entries
// are installed before boot and owned by the session until Close/Cleanup.
func ensureAgyInteractiveSessionForTurn(ctx context.Context, ownerSessionID, workingDir, model, mcpJSON, resumeConversation, toolMode string, opts *llmtypes.CallOptions) (*agyInteractiveSession, error) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	workingDir = strings.TrimSpace(workingDir)
	model = strings.TrimSpace(model)
	resumeConversation = strings.TrimSpace(resumeConversation)
	fingerprint := agyToolModeFingerprint(mcpJSON, toolMode) + ":" + llmtypes.CodingAgentScopeFingerprint(opts)
	if session, ok := agyReserveTurnSession(ownerSessionID); ok {
		if agySidecarReusable(ctx, session, workingDir, model, fingerprint, resumeConversation) {
			return session, nil
		}
		session.turnLeases.Add(-1)
		CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, "sidecar tool surface changed")
	}
	var releaseMounts func()
	privateHome := ""
	if strings.TrimSpace(mcpJSON) != "" {
		servers, err := agyParseMCPServers(mcpJSON)
		if err != nil {
			return nil, err
		}
		// Each sidecar owns a private AGY home, so distinct users never
		// share MCP credentials or block each other's turns.
		privateHome, releaseMounts, err = agyIsolatedHome(servers, workingDir)
		if err != nil {
			return nil, err
		}
		// Re-check after acquiring: a concurrent turn for the same
		// owner may have rebooted while this one waited.
		if session, ok := agyReserveTurnSession(ownerSessionID); ok {
			if agySidecarReusable(ctx, session, workingDir, model, fingerprint, resumeConversation) {
				releaseMounts()
				return session, nil
			}
			session.turnLeases.Add(-1)
			CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, "sidecar tool surface changed")
		}
	}
	if privateHome == "" {
		var err error
		privateHome, releaseMounts, err = agyIsolatedHome(nil, workingDir)
		if err != nil {
			return nil, err
		}
	}
	releaseToolHook, err := agyHoldToolModeHook(workingDir, toolMode)
	if err != nil {
		if releaseMounts != nil {
			releaseMounts()
		}
		return nil, err
	}
	session, err := bootAgyInteractiveSession(ctx, ownerSessionID, workingDir, model, resumeConversation, privateHome, opts)
	if err != nil {
		releaseToolHook()
		if releaseMounts != nil {
			releaseMounts()
		}
		return nil, err
	}
	session.model = model
	session.mountFingerprint = fingerprint
	session.releaseMounts = releaseMounts
	session.releaseToolHook = releaseToolHook
	return session, nil
}

func agyReserveTurnSession(owner string) (*agyInteractiveSession, bool) {
	agyInteractiveRegistry.Lock()
	defer agyInteractiveRegistry.Unlock()
	session, ok := agyInteractiveRegistry.sessions[owner]
	if ok {
		session.turnLeases.Add(1)
	}
	return session, ok
}

// agySidecarReusable reports whether the registered sidecar can serve the
// turn as-is: same dir/model/mounts, tmux alive, and already serving the
// requested resume conversation (a running TUI cannot attach to a
// conversation, so a mismatch reboots with --conversation).
func agySidecarReusable(ctx context.Context, session *agyInteractiveSession, workingDir, model, fingerprint, resumeConversation string) bool {
	if session == nil {
		return false
	}
	if session.workingDir != workingDir || session.model != model || session.mountFingerprint != fingerprint {
		return false
	}
	if resumeConversation != "" && session.getConversationID() != resumeConversation {
		return false
	}
	return agyTmuxSessionAlive(ctx, session.tmuxSessionName)
}

// agySidecarKeyEnvVars are passed explicitly into every sidecar boot. A
// sidecar inherits the tmux SERVER's environment (frozen at server start),
// not the launcher's — so a key exported after the server started would
// otherwise never reach the TUI. Both names ride together to preserve
// agy's own precedence (GOOGLE_API_KEY wins when both are set).
var agySidecarKeyEnvVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

// bootAgyInteractiveSession launches a sidecar TUI and waits for readiness.
// A non-empty resumeConversation attaches the boot to that native
// conversation (--conversation); otherwise the TUI starts fresh and the
// conversation id is discovered by content match after the first turn.
// Callers own mounts/permissions; this only starts the process.
func bootAgyInteractiveSession(ctx context.Context, ownerSessionID, workingDir, model, resumeConversation, privateHome string, opts *llmtypes.CallOptions) (*agyInteractiveSession, error) {
	tmuxName := agySanitizeTmuxName(ownerSessionID)
	args := []string{"new-session", "-d", "-s", tmuxName, "-x", "200", "-y", "50", "-c", workingDir}
	baseEnv := []string{"HOME=" + privateHome}
	if len(llmtypes.ProviderAccountEnvironment(opts)) == 0 {
		for _, key := range agySidecarKeyEnvVars {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				baseEnv = append(baseEnv, key+"="+value)
			}
		}
	}
	scopedEnv, unsetEnv := llmtypes.ScopedCodingAgentEnvironmentPlan(os.Environ(), baseEnv, opts)
	var scrub *shelllaunch.ScopeScrub
	if llmtypes.CodingAgentScopeDeclared(opts) {
		keep := make([]string, 0, len(baseEnv)+len(scopedEnv))
		for _, entry := range append(append([]string(nil), baseEnv...), scopedEnv...) {
			if key, _, ok := strings.Cut(entry, "="); ok {
				keep = append(keep, key)
			}
		}
		scrub = &shelllaunch.ScopeScrub{
			Prefixes: llmtypes.ScopedCredentialPrefixes(),
			Names:    llmtypes.ScopedCredentialNames(),
			Keep:     keep,
		}
	}
	cli := []string{"agy"}
	// Always explicit: the CLI default DIFFERS by auth mode (subscription:
	// Gemini 3.8 Flash high; API key: Gemini 3.1 Pro low — verified live by
	// boot-only probes), so eliding --model for DefaultModelID silently
	// certified the wrong model under key mode.
	if model != "" {
		cli = append(cli, "--model", model)
	}
	if resumeConversation = strings.TrimSpace(resumeConversation); resumeConversation != "" {
		cli = append(cli, "--conversation", resumeConversation)
	}
	// The private AGY home must win over a provider-account HOME overlay;
	// account credentials still reach the child through their own variables.
	launchEnv := append(append([]string(nil), scopedEnv...), baseEnv...)
	command, cleanupScript, err := shelllaunch.CommandWithScopedEnv(cli, workingDir, launchEnv, unsetEnv, scrub)
	if err != nil {
		return nil, fmt.Errorf("prepare agy sidecar environment: %w", err)
	}
	args = append(args, command)
	launch := exec.CommandContext(ctx, "tmux", args...)
	if out, err := launch.CombinedOutput(); err != nil {
		cleanupScript()
		return nil, fmt.Errorf("tmux new-session %s: %w\n%s", tmuxName, err, out)
	}
	session := &agyInteractiveSession{
		ownerSessionID:  ownerSessionID,
		tmuxSessionName: tmuxName,
		workingDir:      workingDir,
		createdAt:       time.Now(),
		conversationID:  resumeConversation,
	}
	session.turnLeases.Store(1)
	agyInteractiveRegistry.Lock()
	agyInteractiveRegistry.sessions[ownerSessionID] = session
	agyInteractiveRegistry.Unlock()
	if _, err := waitAgyPaneReady(ctx, tmuxName, 90*time.Second); err != nil {
		cleanupScript()
		CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, "boot failed")
		return nil, err
	}
	cleanupScript()
	return session, nil
}

// agyTmuxSessionAlive reports whether the tmux session still exists.
func agyTmuxSessionAlive(ctx context.Context, tmuxName string) bool {
	if strings.TrimSpace(tmuxName) == "" {
		return false
	}
	return exec.CommandContext(ctx, "tmux", "has-session", "-t", tmuxName).Run() == nil
}

// runAgyInteractiveTurn executes one prompt in the owner's sidecar
// conversation and returns the extracted reply, the turn's token usage
// summed from the conversation .db, and the turn's tool invocations in
// completion order. An unexpected approval prompt fails loudly: bridge
// tools are pre-approved, so anything asking is native.
func runAgyInteractiveTurn(ctx context.Context, ownerSessionID, prompt string) (string, llmtypes.Usage, []agyTurnToolCall, error) {
	session, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok {
		return "", llmtypes.Usage{}, nil, fmt.Errorf("no agy interactive session for owner %q", ownerSessionID)
	}
	prompt = strings.Trim(prompt, "\n")
	if strings.TrimSpace(prompt) == "" {
		return "", llmtypes.Usage{}, nil, fmt.Errorf("agy interactive turn needs a non-empty prompt")
	}
	idxBefore := agyConversationMaxIdx(session.getConversationID())
	if err := agyPasteToSidecar(ctx, session.tmuxSessionName, prompt); err != nil {
		return "", llmtypes.Usage{}, nil, err
	}
	if err := agyTmuxSendKeys(ctx, session.tmuxSessionName, "Enter"); err != nil {
		return "", llmtypes.Usage{}, nil, fmt.Errorf("submit sidecar prompt: %w", err)
	}
	// The pane can echo a draft before Enter, so only the conversation's
	// matching user step proves that this prompt was taken in.
	userIdx, err := agyWaitTurnIntake(ctx, session, idxBefore, prompt)
	if err != nil {
		return "", llmtypes.Usage{}, nil, err
	}
	if err := agyWaitTurnAnswer(ctx, session, userIdx); err != nil {
		return "", llmtypes.Usage{}, nil, err
	}
	// Only the structured record supplies the answer. The pane includes
	// prompt echo, thoughts and tool renderings, so it cannot repair an
	// unattributable or missing assistant step.
	conversationID := session.getConversationID()
	reply := agyTurnReplySince(conversationID, userIdx)
	if reply == "" {
		return "", llmtypes.Usage{}, nil, fmt.Errorf("sidecar turn produced no recorded assistant reply")
	}
	usage := agyTurnUsageSince(conversationID, userIdx)
	toolCalls := agyTurnToolCallsSince(conversationID, userIdx)
	return reply, usage, toolCalls, nil
}

// agyTurnRecord is a single snapshot of the CLI's own SQLite steps. A turn
// starts at its matching user row; rows before that receipt cannot satisfy
// answer or completion, even in a resumed conversation.
type agyTurnRecord struct {
	userIdx     int
	lastIdx     int
	lastType    int
	lastStatus  int
	answer      string
	finalAnswer string
}

func agyReadTurnRecord(conversationID string, sinceIdx int, prompt string) (agyTurnRecord, error) {
	record := agyTurnRecord{userIdx: -1, lastIdx: -1}
	if prompt == "" {
		record.userIdx = sinceIdx
	}
	if conversationID == "" {
		return record, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return record, err
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "conversations", conversationID+".db")
	tmpPath, cleanup, err := agyCopyDBFile(path)
	if os.IsNotExist(err) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	defer cleanup()
	db, err := sql.Open("sqlite", "file:"+tmpPath)
	if err != nil {
		return record, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), `SELECT idx, step_type, status, step_payload FROM steps WHERE idx > ? ORDER BY idx`, sinceIdx)
	if err != nil {
		return record, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var idx, stepType, status int
		var payload []byte
		if err := rows.Scan(&idx, &stepType, &status, &payload); err != nil {
			return record, err
		}
		if record.userIdx < 0 {
			if stepType != agyStepUser {
				continue
			}
			stored, _, ok := agyTranscriptStepText(stepType, payload)
			if !ok || strings.TrimSpace(stored) != strings.TrimSpace(prompt) {
				continue
			}
			record.userIdx = idx
		}
		record.lastIdx, record.lastType, record.lastStatus = idx, stepType, status
		if stepType == agyStepAssistant {
			record.finalAnswer = ""
			if part, _, ok := agyTranscriptStepText(stepType, payload); ok && strings.TrimSpace(part) != "" {
				record.finalAnswer = strings.TrimSpace(part)
				if record.answer != "" {
					record.answer += "\n\n"
				}
				record.answer += strings.TrimSpace(part)
			}
		}
	}
	return record, rows.Err()
}

func agyWaitTurnIntake(ctx context.Context, session *agyInteractiveSession, sinceIdx int, prompt string) (int, error) {
	deadline := time.Now().Add(90 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		pane, err := captureAgyPane(ctx, session.tmuxSessionName)
		if err != nil {
			return -1, err
		}
		if marker := agyApprovalMarkerShown(pane); marker != "" {
			return -1, fmt.Errorf("sidecar turn blocked on unexpected approval prompt (%s)", marker)
		}
		conversationID := session.getConversationID()
		if conversationID == "" {
			conversationID = session.setConversationIDIfEmpty(agyConversationIDFromPane(ctx, session.tmuxSessionName))
		}
		if conversationID == "" {
			conversationID = session.setConversationIDIfEmpty(agyDiscoverConversationID(session.createdAt, prompt))
		}
		record, err := agyReadTurnRecord(conversationID, sinceIdx, prompt)
		if err == nil && record.userIdx >= 0 {
			return record.userIdx, nil
		}
		if time.Now().After(deadline) {
			readError := "none"
			if err != nil {
				readError = err.Error()
			}
			if quotaErr := agyQuotaPaneError("", pane); quotaErr != nil {
				return -1, quotaErr
			}
			return -1, fmt.Errorf("sidecar prompt has no matching user step after 90s (conversation %q, read error %s); pane tail:\n%s", conversationID, readError, agyPaneTail(pane, 20))
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// AGY 1.2.12 has no run-terminal row. A completed assistant step (status 3)
// is the durable answer proof; its SQLite trail must end there and remain
// unchanged for two seconds. Pane readiness remains a secondary guard against
// an assistant narration step that precedes a tool call.
func agyWaitTurnAnswer(ctx context.Context, session *agyInteractiveSession, userIdx int) error {
	deadline := time.Now().Add(agySidecarTurnTimeout)
	var settledAt time.Time
	var settledIdx int
	var settledAnswer string
	for {
		if err := ctx.Err(); err != nil {
			_ = agyTmuxSendKeys(context.Background(), session.tmuxSessionName, "Escape")
			return err
		}
		pane, err := captureAgyPane(ctx, session.tmuxSessionName)
		if err != nil {
			return err
		}
		if marker := agyApprovalMarkerShown(pane); marker != "" {
			return fmt.Errorf("sidecar turn blocked on unexpected approval prompt (%s); pane tail:\n%s", marker, agyPaneTail(pane, 25))
		}
		record, err := agyReadTurnRecord(session.getConversationID(), userIdx, "")
		if err == nil && record.lastType == agyStepAssistant && record.lastStatus == 3 && record.finalAnswer != "" {
			if record.lastIdx != settledIdx || record.finalAnswer != settledAnswer {
				settledIdx, settledAnswer, settledAt = record.lastIdx, record.finalAnswer, time.Now()
			}
			if time.Since(settledAt) >= 2*time.Second && PaneReadyForInput(pane) {
				return nil
			}
		} else {
			settledAt = time.Time{}
		}
		if time.Now().After(deadline) {
			readError := "none"
			if err != nil {
				readError = err.Error()
			}
			if record.finalAnswer == "" {
				if quotaErr := agyQuotaPaneError("", pane); quotaErr != nil {
					return quotaErr
				}
			}
			return fmt.Errorf("sidecar turn has no settled recorded answer within %s (read error %s); pane tail:\n%s", agySidecarTurnTimeout, readError, agyPaneTail(pane, 30))
		}
		select {
		case <-ctx.Done():
			_ = agyTmuxSendKeys(context.Background(), session.tmuxSessionName, "Escape")
			return ctx.Err()
		case <-time.After(agyApprovalPollInterval):
		}
	}
}

// agyPasteToSidecar pastes a (possibly multiline) prompt into the sidecar
// input via bracketed paste. Typing would submit on the first newline;
// pasting holds all lines until Enter. The paste is verified on the pane
// before returning: Enter must never race an undelivered paste. Short
// prompts echo inline (snippet match); long ones collapse to a
// "[Pasted text #N +M lines]" placeholder whose line count must match.
func agyPasteToSidecar(ctx context.Context, tmuxName, prompt string) error {
	load := exec.CommandContext(ctx, "tmux", "load-buffer", "-w", "-t", tmuxName, "-")
	load.Stdin = strings.NewReader(prompt)
	if out, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %w\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "tmux", "paste-buffer", "-d", "-p", "-t", tmuxName).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux paste-buffer: %w\n%s", err, out)
	}
	snippet := agyEchoSnippet(prompt)
	if snippet == "" {
		return fmt.Errorf("sidecar prompt has no verifiable content")
	}
	wantLines := agyPromptLineCount(prompt)
	deadline := time.Now().Add(15 * time.Second)
	var sawMarker bool
	var lastMarker int
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pane, err := captureAgyPane(ctx, tmuxName)
		if err != nil {
			return err
		}
		if strings.Contains(pane, snippet) {
			return nil
		}
		if got, ok := agyPastedMarkerLines(pane); ok {
			sawMarker, lastMarker = true, got
			if got == wantLines {
				return nil
			}
			// Wrong count: a stale marker from an earlier turn whose
			// replacement hasn't rendered yet, or a genuinely truncated
			// paste — keep polling and decide at the deadline.
		}
		if time.Now().After(deadline) {
			if sawMarker {
				return fmt.Errorf("sidecar paste marker shows %d lines, want %d — the prompt may be truncated", lastMarker, wantLines)
			}
			return fmt.Errorf("pasted prompt never appeared in the sidecar input within 15s")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// agyApprovalMarkerShown returns the approval marker text when the pane is
// asking to approve a tool call or file access.
func agyApprovalMarkerShown(pane string) string {
	status := agyPaneStatusRows(pane)
	for _, marker := range agyPaneNativeApprovalMarkers {
		if strings.Contains(status, marker) {
			return marker
		}
	}
	return ""
}

// agyEchoSnippet returns a short distinctive fragment of the prompt for
// echo gating (the TUI echoes the submitted prompt before running).
// agyPromptLineCount counts the logical lines the TUI's paste-collapse
// marker reports ("[Pasted text #N +M lines]"): newline count, plus one
// unless the prompt ends with a newline (verified live: a 52-newline file
// shows +52 lines).
func agyPromptLineCount(prompt string) int {
	n := strings.Count(prompt, "\n") + 1
	if strings.HasSuffix(prompt, "\n") {
		n--
	}
	return n
}

// agyPastedMarkerLines parses the newest paste-collapse marker on the pane.
// The LAST occurrence wins: earlier turns' markers scroll into history while
// the fresh input sits at the bottom.
func agyPastedMarkerLines(pane string) (int, bool) {
	idx := strings.LastIndex(pane, "[Pasted text #")
	if idx < 0 {
		return 0, false
	}
	rest := pane[idx:]
	plus := strings.Index(rest, "+")
	if plus < 0 {
		return 0, false
	}
	rest = rest[plus+1:]
	end := strings.Index(rest, " ")
	if end < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// agyConversationIDFromPane attributes the sidecar's conversation by asking
// the OS what the TUI has open: after its first turn the agy process holds
// conversations/<id>.db (+wal/shm) open and keeps it open while idle
// (verified live). Exact per-pane identity — parallel same-prompt sidecars
// can never collide, unlike content discovery. "" when lsof is missing,
// the pane is gone, or anything is ambiguous: callers fall back to content
// discovery, which carries the strictness.
func agyConversationIDFromPane(ctx context.Context, tmuxName string) string {
	if _, err := exec.LookPath("lsof"); err != nil {
		return ""
	}
	pidOut, err := exec.CommandContext(ctx, "tmux", "display-message", "-t", tmuxName, "-p", "#{pane_pid}").CombinedOutput()
	if err != nil {
		return ""
	}
	panePID := strings.TrimSpace(string(pidOut))
	if panePID == "" {
		return ""
	}
	pids := []string{panePID}
	if childOut, err := exec.CommandContext(ctx, "pgrep", "-P", panePID).CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(childOut), "\n") {
			if pid := strings.TrimSpace(line); pid != "" {
				pids = append(pids, pid)
			}
		}
	}
	lsofOut, err := exec.CommandContext(ctx, "lsof", "-p", strings.Join(pids, ",")).CombinedOutput()
	if err != nil {
		return ""
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(lsofOut), "\n") {
		idx := strings.Index(line, "conversations/")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("conversations/"):]
		end := strings.Index(rest, ".db")
		if end <= 0 {
			continue
		}
		// Exact .db only: -wal/-shm lines belong to the same conversation
		// but must not double-count or admit a truncated name.
		if tail := strings.TrimSpace(rest[end+len(".db"):]); tail != "" {
			continue
		}
		seen[rest[:end]] = true
	}
	if len(seen) != 1 {
		return ""
	}
	for id := range seen {
		return id
	}
	return ""
}

func agyEchoSnippet(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if s := strings.TrimSpace(line); len(s) >= 8 {
			if len(s) > 48 {
				s = s[:48]
			}
			return s
		}
	}
	s := strings.TrimSpace(prompt)
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// agyPaneTail returns the last n non-empty pane lines for diagnostics.
func agyPaneTail(pane string, n int) string {
	var lines []string
	for _, line := range strings.Split(pane, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// agyDiscoverConversationID finds the sidecar's TUI conversation by content:
// the conversation whose latest user step carries our just-sent prompt, among
// .dbs modified since boot. Empty when unattributable (never a guess).
func agyDiscoverConversationID(booted time.Time, prompt string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	snippet := agyEchoSnippet(prompt)
	if snippet == "" {
		return ""
	}
	var match string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fresh := info.ModTime()
		// Fresh steps may live only in WAL with a stale main .db mtime.
		if walInfo, err := os.Stat(filepath.Join(dir, name+"-wal")); err == nil && walInfo.ModTime().After(fresh) {
			fresh = walInfo.ModTime()
		}
		if fresh.Before(booted.Add(-60 * time.Second)) {
			continue
		}
		id := strings.TrimSuffix(name, ".db")
		latest, ok := agyLatestUserStep(filepath.Join(dir, name))
		if !ok {
			continue
		}
		if strings.Contains(latest, snippet) {
			if match != "" {
				return ""
			}
			match = id
		}
	}
	return match
}

// agyLatestUserStep returns the latest type-14 step text in a conversation.
func agyLatestUserStep(path string) (string, bool) {
	tmpPath, cleanup, err := agyCopyDBFile(path)
	if err != nil {
		return "", false
	}
	defer cleanup()
	db, err := sql.Open("sqlite", "file:"+tmpPath)
	if err != nil {
		return "", false
	}
	defer func() { _ = db.Close() }()
	var payload []byte
	err = db.QueryRowContext(context.Background(), fmt.Sprintf(`SELECT step_payload FROM steps WHERE step_type = %d ORDER BY idx DESC LIMIT 1`, agyStepUser)).Scan(&payload)
	if err != nil {
		return "", false
	}
	sub, ok := agyProtoSubmessage(payload, 19)
	if !ok {
		return "", false
	}
	return agyProtoStringField(sub, 2)
}

// agyConversationMaxIdx returns the conversation's current max step idx, or
// -1 when unknown (no conversation yet, or unreadable).
func agyConversationMaxIdx(conversationID string) int {
	if strings.TrimSpace(conversationID) == "" {
		return -1
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return -1
	}
	tmpPath, cleanup, err := agyCopyDBFile(filepath.Join(home, ".gemini", "antigravity-cli", "conversations", conversationID+".db"))
	if err != nil {
		return -1
	}
	defer cleanup()
	db, err := sql.Open("sqlite", "file:"+tmpPath)
	if err != nil {
		return -1
	}
	defer func() { _ = db.Close() }()
	var maxIdx sql.NullInt64
	if err := db.QueryRowContext(context.Background(), `SELECT MAX(idx) FROM steps`).Scan(&maxIdx); err != nil || !maxIdx.Valid {
		return -1
	}
	return int(maxIdx.Int64)
}

// agyTurnUsageSince sums token usage from type-15 steps appended after
// sinceIdx. Verified live: field 5.9 carries input (2), output (3), and
// thinking (9) token counts equal to the exec JSON envelope for the same
// turn shape; tool steps carry no usage section.
func agyTurnUsageSince(conversationID string, sinceIdx int) llmtypes.Usage {
	var usage llmtypes.Usage
	if strings.TrimSpace(conversationID) == "" {
		return usage
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return usage
	}
	tmpPath, cleanup, err := agyCopyDBFile(filepath.Join(home, ".gemini", "antigravity-cli", "conversations", conversationID+".db"))
	if err != nil {
		return usage
	}
	defer cleanup()
	db, err := sql.Open("sqlite", "file:"+tmpPath)
	if err != nil {
		return usage
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), fmt.Sprintf(`SELECT step_payload FROM steps WHERE step_type = %d AND idx > ? ORDER BY idx`, agyStepAssistant), sinceIdx)
	if err != nil {
		return usage
	}
	defer func() { _ = rows.Close() }()
	var thinking int
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		meta, ok := agyProtoSubmessage(payload, 5)
		if !ok {
			continue
		}
		usection, ok := agyProtoSubmessage(meta, 9)
		if !ok {
			continue
		}
		input, _ := agyProtoVarintField(usection, 2)
		output, _ := agyProtoVarintField(usection, 3)
		think, _ := agyProtoVarintField(usection, 9)
		usage.InputTokens += input
		usage.OutputTokens += output
		thinking += think
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if thinking > 0 {
		usage.ThoughtsTokens = &thinking
	}
	return usage
}

// agyStepToolCall is the conversation step type for a tool invocation
// (user/assistant step consts live in agycli_native_transcript.go).
const agyStepToolCall = 132

// agyTurnToolCall is one tool invocation the sidecar made during a turn,
// read back from the conversation .db after the turn completes.
type agyTurnToolCall struct {
	// CallID is agy's own call id (call_<n>), the Start/End pairing key.
	CallID string
	// Name is the semantic tool name: native tools surface directly
	// (view_file, search_web, ...); MCP-dispatch steps (call_mcp_tool)
	// surface the INNER ToolName (execute_shell_command, ...) since the
	// wrapper is transport, not the tool the user cares about.
	Name string
	// Args is the invocation's argument JSON verbatim.
	Args string
	// ErrorText is the failure text for a failed call, "" on success.
	ErrorText string
}

// agyTurnToolCallsSince returns the tool calls appended after sinceIdx in
// idx (completion) order. Layout (verified against live 1.2.7
// conversations): step_payload field 5 carries the call section, whose
// field 4 holds call id (1), tool name (2), and argument JSON (3); a
// failed call additionally carries error text in error_details field 2.
//
// The sidecar never streams tool events live (one content chunk per
// turn), so the adapter synthesizes Start/End pairs from these steps
// after the turn: real invocations, post-hoc delivery. The pairing is
// per-call Start,End in completion order — each call completed before
// the next began under sequential TUI execution.
func agyTurnToolCallsSince(conversationID string, sinceIdx int) []agyTurnToolCall {
	var calls []agyTurnToolCall
	if strings.TrimSpace(conversationID) == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	tmpPath, cleanup, err := agyCopyDBFile(filepath.Join(home, ".gemini", "antigravity-cli", "conversations", conversationID+".db"))
	if err != nil {
		return nil
	}
	defer cleanup()
	db, err := sql.Open("sqlite", "file:"+tmpPath)
	if err != nil {
		return nil
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), fmt.Sprintf(`SELECT step_payload, error_details FROM steps WHERE step_type = %d AND idx > ? ORDER BY idx`, agyStepToolCall), sinceIdx)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var payload, errDet []byte
		if err := rows.Scan(&payload, &errDet); err != nil {
			continue
		}
		section, ok := agyProtoSubmessage(payload, 5)
		if !ok {
			continue
		}
		call, ok := agyProtoSubmessage(section, 4)
		if !ok {
			continue
		}
		callID, _ := agyProtoStringField(call, 1)
		name, _ := agyProtoStringField(call, 2)
		args, _ := agyProtoStringField(call, 3)
		if strings.TrimSpace(name) == "" {
			continue
		}
		if name == "call_mcp_tool" {
			if inner := agyMCPInnerToolName(args); inner != "" {
				name = inner
			}
		}
		errText, _ := agyProtoStringField(errDet, 2)
		if errText == "" {
			errText, _ = agyProtoStringField(errDet, 3)
		}
		calls = append(calls, agyTurnToolCall{CallID: callID, Name: name, Args: args, ErrorText: errText})
	}
	return calls
}

// agyTurnReplySince returns the turn's final assistant text: field 20.1
// from the last type-15 step appended after sinceIdx. Earlier type-15 rows
// are progress narration and must not be folded into unified_completion. Verified
// live: 20.1 is the reply/narration text (duplicated at 20.8), 20.3 is
// chain-of-thought (never user-visible), 20.7 the tool call. Pane scraping
// cannot isolate the reply (the pane mixes prompt echo, thoughts, tool
// renderings, and reply), so the .db is the source of truth; "" when
// unattributable causes the turn to fail instead of inventing a pane reply.
func agyTurnReplySince(conversationID string, sinceIdx int) string {
	var final string
	if strings.TrimSpace(conversationID) == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	tmpPath, cleanup, err := agyCopyDBFile(filepath.Join(home, ".gemini", "antigravity-cli", "conversations", conversationID+".db"))
	if err != nil {
		return ""
	}
	defer cleanup()
	db, err := sql.Open("sqlite", "file:"+tmpPath)
	if err != nil {
		return ""
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(context.Background(), fmt.Sprintf(`SELECT step_payload FROM steps WHERE step_type = %d AND idx > ? ORDER BY idx`, agyStepAssistant), sinceIdx)
	if err != nil {
		return ""
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		content, ok := agyProtoSubmessage(payload, 20)
		if !ok {
			continue
		}
		text, ok := agyProtoStringField(content, 1)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		final = strings.TrimSpace(text)
	}
	return final
}

// agyMCPInnerToolName unwraps an MCP-dispatch step's argument JSON to the
// real tool name ({"ToolName":"execute_shell_command","ServerName":...}).
// "" when the args are not an MCP dispatch envelope.
func agyMCPInnerToolName(args string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var envelope struct {
		ToolName string `json:"ToolName"`
	}
	if err := json.Unmarshal([]byte(args), &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.ToolName)
}

// agyProtoVarintField returns the first field num's varint value.
func agyProtoVarintField(msg []byte, num protowire.Number) (int, bool) {
	for len(msg) > 0 {
		field, typ, n := protowire.ConsumeTag(msg)
		if n < 0 {
			return 0, false
		}
		msg = msg[n:]
		if field != num {
			m := protowire.ConsumeFieldValue(field, typ, msg)
			if m < 0 {
				return 0, false
			}
			msg = msg[m:]
			continue
		}
		if typ != protowire.VarintType {
			return 0, false
		}
		val, n := protowire.ConsumeVarint(msg)
		if n < 0 || val > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int(val), true
	}
	return 0, false
}

// agySettingsPath returns the CLI settings file under home.
func agySettingsPath(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// agyCopyDBFile copies a conversation .db plus its -wal companion (when
// present) to a temp path for WAL-safe reading; a running TUI keeps fresh
// steps in WAL, so the main file alone goes stale mid-session. The -shm
// index is rebuilt by SQLite on open. Callers must run cleanup.
func agyCopyDBFile(path string) (tmpPath string, cleanup func(), err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.CreateTemp("", "agy-turn-*.db")
	if err != nil {
		return "", nil, err
	}
	tmpPath = tmp.Name()
	cleanup = func() {
		_ = os.Remove(tmpPath)
		_ = os.Remove(tmpPath + "-wal")
		_ = os.Remove(tmpPath + "-shm")
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	if wal, err := os.ReadFile(path + "-wal"); err == nil {
		if err := os.WriteFile(tmpPath+"-wal", wal, 0o600); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return tmpPath, cleanup, nil
}
