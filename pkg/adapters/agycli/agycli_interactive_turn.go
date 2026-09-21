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
	"sort"
	"strconv"
	"strings"
	"time"

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
func ensureAgyInteractiveSessionForTurn(ctx context.Context, ownerSessionID, workingDir, model, mcpJSON, resumeConversation string) (*agyInteractiveSession, error) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	workingDir = strings.TrimSpace(workingDir)
	model = strings.TrimSpace(model)
	resumeConversation = strings.TrimSpace(resumeConversation)
	fingerprint := agyMountFingerprint(mcpJSON)
	if session, ok := activeAgyInteractiveSession(ownerSessionID); ok {
		if agySidecarReusable(ctx, session, workingDir, model, fingerprint, resumeConversation) {
			return session, nil
		}
		CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, "sidecar tool surface changed")
	}
	var releaseMounts func()
	if fingerprint != "unmounted" {
		servers, err := agyParseMCPServers(mcpJSON)
		if err != nil {
			return nil, err
		}
		// Hold before boot: permissions must exist when the TUI starts.
		// A different surface waits here (ctx-bounded) for release.
		var holdRelease func()
		_, holdRelease, err = agyHoldMounts(ctx, fingerprint, servers)
		if err != nil {
			return nil, err
		}
		releaseMounts = holdRelease
		// Re-check after acquiring: a concurrent turn for the same
		// owner may have rebooted while this one waited.
		if session, ok := activeAgyInteractiveSession(ownerSessionID); ok {
			if agySidecarReusable(ctx, session, workingDir, model, fingerprint, resumeConversation) {
				releaseMounts()
				return session, nil
			}
			CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, "sidecar tool surface changed")
		}
	}
	session, err := bootAgyInteractiveSession(ctx, ownerSessionID, workingDir, model, resumeConversation)
	if err != nil {
		if releaseMounts != nil {
			releaseMounts()
		}
		return nil, err
	}
	session.model = model
	session.mountFingerprint = fingerprint
	session.releaseMounts = releaseMounts
	return session, nil
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
	if resumeConversation != "" && session.conversationID != resumeConversation {
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
func bootAgyInteractiveSession(ctx context.Context, ownerSessionID, workingDir, model, resumeConversation string) (*agyInteractiveSession, error) {
	tmuxName := agySanitizeTmuxName(ownerSessionID)
	args := []string{"new-session", "-d", "-s", tmuxName, "-x", "200", "-y", "50", "-c", workingDir}
	for _, key := range agySidecarKeyEnvVars {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			args = append(args, "-e", key+"="+value)
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
	launch := exec.CommandContext(ctx, "tmux", append(args, cli...)...)
	if out, err := launch.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tmux new-session %s: %w\n%s", tmuxName, err, out)
	}
	session := &agyInteractiveSession{
		ownerSessionID:  ownerSessionID,
		tmuxSessionName: tmuxName,
		workingDir:      workingDir,
		createdAt:       time.Now(),
		conversationID:  resumeConversation,
	}
	agyInteractiveRegistry.Lock()
	agyInteractiveRegistry.sessions[ownerSessionID] = session
	agyInteractiveRegistry.Unlock()
	if _, err := waitAgyPaneReady(ctx, tmuxName, 90*time.Second); err != nil {
		CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, "boot failed")
		return nil, err
	}
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
	idxBefore := agyConversationMaxIdx(session.conversationID)
	if err := agyPasteToSidecar(ctx, session.tmuxSessionName, prompt); err != nil {
		return "", llmtypes.Usage{}, nil, err
	}
	if err := agyTmuxSendKeys(ctx, session.tmuxSessionName, "Enter"); err != nil {
		return "", llmtypes.Usage{}, nil, fmt.Errorf("submit sidecar prompt: %w", err)
	}
	// Echo gating cannot prove submission (pasted-but-unsubmitted input
	// also echoes), so require the busy marker: the turn started.
	if err := agyWaitSidecarTurnStarted(ctx, session.tmuxSessionName); err != nil {
		return "", llmtypes.Usage{}, nil, err
	}
	pane, err := agyWaitSidecarTurnDone(ctx, session.tmuxSessionName)
	if err != nil {
		return "", llmtypes.Usage{}, nil, err
	}
	// The TUI persists steps write-behind: pane completion precedes the
	// .db flush, so await the new steps before metering. Generous bound:
	// a slow flush must wait, not fail (observed: a mounted key-mode turn
	// needed past 30s once); a truly missing conversation still fails
	// loudly at the deadline.
	if err := agyAwaitTurnSteps(ctx, session, idxBefore, prompt, 90*time.Second); err != nil {
		return "", llmtypes.Usage{}, nil, err
	}
	// The reply comes from the .db (user-visible assistant text only),
	// never the pane (which mixes prompt echo, thoughts, and tool
	// renderings into the reply). Pane scraping stays as the fallback so
	// a pane-proved turn never fails on unattributable steps.
	reply := agyTurnReplySince(session.conversationID, idxBefore)
	if reply == "" {
		reply = agyExtractLastReply(pane)
	}
	if reply == "" {
		return "", llmtypes.Usage{}, nil, fmt.Errorf("sidecar turn produced no extractable reply; pane tail:\n%s", agyPaneTail(pane, 30))
	}
	usage := agyTurnUsageSince(session.conversationID, idxBefore)
	toolCalls := agyTurnToolCallsSince(session.conversationID, idxBefore)
	return reply, usage, toolCalls, nil
}

// agyWaitSidecarTurnStarted waits for the busy marker after Enter: proof
// the TUI accepted the prompt and a turn is running. An approval prompt
// here fails loudly (bridge tools are pre-approved).
func agyWaitSidecarTurnStarted(ctx context.Context, tmuxName string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pane, err := captureAgyPane(ctx, tmuxName)
		if err != nil {
			return err
		}
		if marker := agyApprovalMarkerShown(pane); marker != "" {
			return fmt.Errorf("sidecar turn blocked on unexpected approval prompt (%s); bridge tools are pre-approved, refusing to auto-answer", marker)
		}
		if strings.Contains(pane, agyPaneBusyMarker) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sidecar did not start the turn within 30s of Enter (prompt may not have submitted); pane tail:\n%s", agyPaneTail(pane, 20))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// agyAwaitTurnSteps waits for the turn's steps to flush to the
// conversation .db, discovering the conversation when still unknown:
// first by the TUI process's open files (exact, parallel-safe), then by
// content match (strict: ambiguity fails rather than misattributes).
// Returns an error when attribution stays impossible: metering must be
// explicit, never a silent zero blamed on a slow flush.
func agyAwaitTurnSteps(ctx context.Context, session *agyInteractiveSession, idxBefore int, prompt string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if session.conversationID == "" {
			session.conversationID = agyConversationIDFromPane(ctx, session.tmuxSessionName)
		}
		if session.conversationID == "" {
			session.conversationID = agyDiscoverConversationID(session.createdAt, prompt)
		}
		if session.conversationID != "" && agyConversationMaxIdx(session.conversationID) > idxBefore {
			return nil
		}
		if time.Now().After(deadline) {
			if session.conversationID == "" {
				return fmt.Errorf("sidecar turn steps never attributable to a conversation within %s", timeout)
			}
			return fmt.Errorf("sidecar turn steps for conversation %s never appeared within %s", session.conversationID, timeout)
		}
		time.Sleep(500 * time.Millisecond)
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

// agyWaitSidecarTurnDone waits for turn completion, failing fast on
// unexpected approval prompts instead of hanging until the timeout.
func agyWaitSidecarTurnDone(ctx context.Context, tmuxName string) (string, error) {
	deadline := time.Now().Add(agySidecarTurnTimeout)
	for {
		if err := ctx.Err(); err != nil {
			_ = agyTmuxSendKeys(context.Background(), tmuxName, "Escape")
			return "", err
		}
		pane, err := captureAgyPane(ctx, tmuxName)
		if err != nil {
			return "", err
		}
		if marker := agyApprovalMarkerShown(pane); marker != "" {
			return "", fmt.Errorf("sidecar turn blocked on unexpected approval prompt (%s); bridge tools are pre-approved, refusing to auto-answer; pane tail:\n%s", marker, agyPaneTail(pane, 25))
		}
		if PaneReadyForInput(pane) {
			return pane, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("sidecar turn did not complete within %s; pane tail:\n%s", agySidecarTurnTimeout, agyPaneTail(pane, 30))
		}
		select {
		case <-ctx.Done():
			_ = agyTmuxSendKeys(context.Background(), tmuxName, "Escape")
			return "", ctx.Err()
		case <-time.After(agyApprovalPollInterval):
		}
	}
}

// agyApprovalMarkerShown returns the approval marker text when the pane is
// asking to approve a tool call or file access.
func agyApprovalMarkerShown(pane string) string {
	for _, marker := range agyPaneNativeApprovalMarkers {
		if strings.Contains(pane, marker) {
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

// agyTurnReplySince returns the turn's user-visible assistant text: field
// 20.1 across type-15 steps appended after sinceIdx, in idx order. Verified
// live: 20.1 is the reply/narration text (duplicated at 20.8), 20.3 is
// chain-of-thought (never user-visible), 20.7 the tool call. Pane scraping
// cannot isolate the reply (the pane mixes prompt echo, thoughts, tool
// renderings, and reply), so the .db is the source of truth; "" when
// unattributable (callers fall back to the pane).
func agyTurnReplySince(conversationID string, sinceIdx int) string {
	var parts []string
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
		parts = append(parts, strings.TrimSpace(text))
	}
	return strings.Join(parts, "\n\n")
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

// agyAllowBaseline captures permissions-key presence BEFORE a mount hold
// adds entries, so the matching unmount can remove exactly what the hold
// created (an unmount must not leave an explicit empty list behind, nor
// delete a key the user already had). Written at add, read at remove, both
// under agyMCPMountMu; one mount activation is live at a time.
var agyAllowBaseline = struct {
	valid    bool
	hadPerms bool
	hadAllow bool
}{}

// agyAllowMountedTools appends mcp(<mount>/*) entries to
// permissions.allow, preserving every other key. Callers hold
// agyMCPMountMu; entries are removed by agyRemoveAllowedTools at unmount.
func agyAllowMountedTools(mounted []string) error {
	return agyEditPermissionAllows(func(allows []string) []string {
		have := map[string]bool{}
		for _, a := range allows {
			have[a] = true
		}
		for _, name := range mounted {
			entry := "mcp(" + name + "/*)"
			if !have[entry] {
				allows = append(allows, entry)
				have[entry] = true
			}
		}
		sort.Strings(allows)
		return allows
	})
}

// agyRemoveAllowedTools drops this session's mcp(<mount>/*) entries,
// leaving user and foreign entries untouched. Keys the matching hold
// created are removed again (no explicit empty list left behind); keys the
// user already had are preserved even when emptied.
func agyRemoveAllowedTools(mounted []string) error {
	drop := map[string]bool{}
	for _, name := range mounted {
		drop["mcp("+name+"/*)"] = true
	}
	return agyEditPermissionAllowsBaseline(func(allows []string) []string {
		kept := allows[:0]
		for _, a := range allows {
			if !drop[a] {
				kept = append(kept, a)
			}
		}
		return kept
	})
}

func agyEditPermissionAllows(edit func([]string) []string) error {
	return agyEditPermissionAllowsInner(edit, false)
}

// agyEditPermissionAllowsBaseline is the unmount half: key presence is
// judged against the pre-hold baseline captured at add time, not the
// current file (which trivially "has" the keys the hold just created).
func agyEditPermissionAllowsBaseline(edit func([]string) []string) error {
	return agyEditPermissionAllowsInner(edit, true)
}

func agyEditPermissionAllowsInner(edit func([]string) []string, useBaseline bool) error {
	// Whole-file write under the shared settings mutex (see agyTrustMu):
	// trust/untrust/keymode RMWs race these otherwise.
	agyTrustMu.Lock()
	defer agyTrustMu.Unlock()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return fmt.Errorf("agy settings home: %w", err)
	}
	path := agySettingsPath(home)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read agy settings: %w", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return fmt.Errorf("parse agy settings: %w", err)
	}
	perms, _ := settings["permissions"].(map[string]interface{})
	_, hadPerms := settings["permissions"]
	_, hadAllow := perms["allow"]
	if perms == nil {
		perms = map[string]interface{}{}
	}
	if !useBaseline {
		agyAllowBaseline = struct {
			valid    bool
			hadPerms bool
			hadAllow bool
		}{valid: true, hadPerms: hadPerms, hadAllow: hadAllow}
	} else if agyAllowBaseline.valid {
		hadPerms, hadAllow = agyAllowBaseline.hadPerms, agyAllowBaseline.hadAllow
	}
	var allows []string
	if list, ok := perms["allow"].([]interface{}); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				allows = append(allows, s)
			}
		}
	}
	edited := edit(allows)
	if len(edited) == 0 && !hadAllow {
		// Presence-preserving: never create permissions.allow for an edit
		// that nets to nothing (an unmount on a keyless file must leave no
		// trace, not an explicit empty list).
		if !hadPerms {
			delete(settings, "permissions")
		} else {
			delete(perms, "allow")
			settings["permissions"] = perms
		}
	} else {
		perms["allow"] = edited
		settings["permissions"] = perms
	}
	merged, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agy settings: %w", err)
	}
	if err := os.WriteFile(path, append(merged, '\n'), 0o600); err != nil {
		return fmt.Errorf("write agy settings: %w", err)
	}
	return nil
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
