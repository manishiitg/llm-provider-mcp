package codexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Durable submit acknowledgement for Codex live input.
//
// A tmux send-keys exit 0 only proves tmux accepted the keystroke, and the
// pane confirmation (draft cleared, activity started, queued text visible)
// can misread on redraw races, CLI version drift, or blind scrapers. The
// session rollout is the authoritative second opinion: Codex appends a
// response_item user row carrying the exact message text, timestamped,
// usually within a second of Enter on an idle composer.
//
// The send path therefore runs in two phases (see
// docs/refactor/codex_durable_ack_p0.md in mcp-agent-builder-go):
//
//  1. Fast pane confirm with the existing resubmit loop. Success returns
//     at once; the common case pays no file cost.
//  2. Observe-only file arbiter, only when phase 1 fails. No more
//     keystrokes are sent here: after exhausted resubmits the message may
//     already be accepted, and another Enter could duplicate it.
//
// A busy steer adds a third outcome: the pane positively shows the
// message natively queued while the user row has not landed yet (observed
// 17s on a no-tool turn; unbounded behind slow tools). That is
// accepted-but-unflushed — held by the CLI, durability unconfirmed —
// not an error.

// CodexDurableAckOutcome is the verdict of the rollout arbiter.
type CodexDurableAckOutcome string

const (
	// CodexDurableAckConfirmed means the session rollout contains the
	// user row for this send.
	CodexDurableAckConfirmed CodexDurableAckOutcome = "confirmed"
	// CodexDurableAckUnflushed means the budget expired while the pane
	// still positively showed the message natively queued. The CLI
	// holds it; durability is unconfirmed, not failed.
	CodexDurableAckUnflushed CodexDurableAckOutcome = "accepted_but_unflushed"
)

// CodexDurableAck carries the rollout proof for one send.
type CodexDurableAck struct {
	Outcome      CodexDurableAckOutcome
	Latency      time.Duration
	ProofPath    string
	RowTimestamp time.Time
}

// CodexInputUnflushedError reports accepted-but-unflushed through the
// error return so existing err != nil callers stay safe (they treat it
// as a failure) while confirmation-aware callers can distinguish it
// with errors.As and reconcile via the turn's terminal marker.
type CodexInputUnflushedError struct {
	OwnerSessionID string
	Latency        time.Duration
}

func (e *CodexInputUnflushedError) Error() string {
	return fmt.Sprintf("Codex input accepted but unflushed after %s (still natively queued; durability unconfirmed, not failed)", e.Latency.Round(100*time.Millisecond))
}

const (
	// Default 180s: codex records a mid-turn steered message
	// only when it processes its queue (turn boundary), which
	// routinely exceeds 60s on long turns — the live e2e saw
	// the row land at 97s. The watch is secondary (never
	// blocks the user), so the generous budget only delays a
	// verdict that would otherwise be a false negative.
	codexDurableAckBudgetDefault = 180 * time.Second
	codexDurableAckBudgetMin     = 5 * time.Second
	codexDurableAckBudgetMax     = 300 * time.Second
	codexDurableAckPollInterval  = 250 * time.Millisecond
)

// codexDurableAckBudget bounds the observe-only file arbiter.
// CODEX_DURABLE_ACK_SECONDS overrides the default; values outside
// [5,300] are clamped so a broken env cannot hang sends or neuter
// the arbiter.
func codexDurableAckBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CODEX_DURABLE_ACK_SECONDS"))
	if raw == "" {
		return codexDurableAckBudgetDefault
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return codexDurableAckBudgetDefault
	}
	budget := time.Duration(seconds) * time.Second
	if budget < codexDurableAckBudgetMin {
		return codexDurableAckBudgetMin
	}
	if budget > codexDurableAckBudgetMax {
		return codexDurableAckBudgetMax
	}
	return budget
}

// codexPendingDurableAck is the pre-paste snapshot that scopes one send's
// durability proof. Offset scoping (plus exact text) means an identical
// earlier message already in the stream cannot confirm a new one.
type codexPendingDurableAck struct {
	message string
	path    string
	offset  int64
	since   time.Time
}

const (
	codexMaxPendingDurableAcks = 8
	codexPendingDurableAckTTL  = 10 * time.Minute
)

func stashCodexDurableReceipt(session *codexInteractiveSession, message, path string, offset int64, since time.Time) {
	if session == nil {
		return
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	kept := session.pendingDurable[:0]
	for _, pending := range session.pendingDurable {
		if time.Since(pending.since) < codexPendingDurableAckTTL {
			kept = append(kept, pending)
		}
	}
	kept = append(kept, codexPendingDurableAck{message: message, path: path, offset: offset, since: since})
	if len(kept) > codexMaxPendingDurableAcks {
		kept = append([]codexPendingDurableAck(nil), kept[len(kept)-codexMaxPendingDurableAcks:]...)
	}
	session.pendingDurable = kept
}

// peekCodexDurableReceipt returns the earliest pending snapshot for an
// identical message. Receipts are deliberately not consumed: sends are
// broker-serialized per session and the CLI queues FIFO, so offset+text
// scoping self-disambiguates repeats without cross-goroutine ownership.
func peekCodexDurableReceipt(session *codexInteractiveSession, message string) (codexPendingDurableAck, bool) {
	if session == nil {
		return codexPendingDurableAck{}, false
	}
	message = strings.TrimSpace(message)
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	for _, pending := range session.pendingDurable {
		if pending.message == message && time.Since(pending.since) < codexPendingDurableAckTTL {
			return pending, true
		}
	}
	return codexPendingDurableAck{}, false
}

// codexRolloutUserMessageSince returns the timestamp of the first
// response_item user row at byte offset >= minOffset, timestamped at or
// after since, whose text matches message — or false when no row does.
// Malformed lines are skipped; a missing file simply has no proof yet.
func codexRolloutUserMessageSince(path, message string, since time.Time, minOffset int64) (time.Time, bool) {
	message = strings.TrimSpace(message)
	if strings.TrimSpace(path) == "" || message == "" {
		return time.Time{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	type contentPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type event struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type    string        `json:"type"`
			Role    string        `json:"role"`
			Content []contentPart `json:"content"`
		} `json:"payload"`
	}
	// A 2s skew grace covers filesystem/timestamp granularity; offset
	// scoping remains the primary guard against stale matches.
	since = since.Add(-2 * time.Second)
	reader := bufio.NewReader(f)
	var offset int64
	for {
		line, readErr := reader.ReadBytes('\n')
		lineStart := offset
		offset += int64(len(line))
		if len(line) > 0 && lineStart >= minOffset {
			var e event
			if json.Unmarshal(line, &e) == nil && e.Type == "response_item" && e.Payload.Role == "user" {
				timestamp, parseErr := time.Parse(time.RFC3339Nano, e.Timestamp)
				if parseErr == nil && !timestamp.Before(since) {
					for _, part := range e.Payload.Content {
						if part.Type != "input_text" {
							continue
						}
						if codexUserRowMatches(part.Text, message) {
							return timestamp, true
						}
						break
					}
				}
			}
		}
		if readErr != nil {
			return time.Time{}, false
		}
	}
}

// codexUserRowMatches compares rollout user text with the sent message.
// The paste path is byte-exact in practice, but a long message matches
// on a shared prefix because terminal editors may normalize trailing
// content; short messages must match exactly.
func codexUserRowMatches(rowText, message string) bool {
	rowText = strings.TrimSpace(rowText)
	message = strings.TrimSpace(message)
	if rowText == message {
		return true
	}
	const prefixLen = 200
	if len(message) > prefixLen && strings.HasPrefix(rowText, message[:prefixLen]) {
		return true
	}
	return false
}

// codexPaneShowsQueuedMessage reports that message is sitting in Codex's
// native queue right now: a queue banner in recent scrollback with the
// message text below it and no completion line between the banner and
// the bottom (a completion there means the queue already flushed,
// making both banner and text historical). hasCodexQueuedInput is
// positional-only and goes blind once the ready footer repaints below
// the banner — the exact layout a busy steer produces — so the
// arbiter keys on the message text instead.
func codexPaneShowsQueuedMessage(captured, message string) bool {
	needle := strings.TrimSpace(strings.Split(strings.TrimSpace(message), "\n")[0])
	if len(needle) > 80 {
		needle = needle[:80]
	}
	if needle == "" {
		return false
	}
	lines := strings.Split(stripCodexANSI(captured), "\n")
	seenNonEmpty := 0
	seenNeedle := false
	for i := len(lines) - 1; i >= 0 && seenNonEmpty < 80; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seenNonEmpty++
		if isCodexCompletedStatusLine(line) {
			return false
		}
		if isCodexQueuedInputLine(line) {
			return seenNeedle
		}
		if !seenNeedle && strings.Contains(line, needle) {
			seenNeedle = true
		}
	}
	return false
}

// codexDurableAckPoll tunes pollCodexDurableAck. Zero values select live
// defaults; tests inject resolve/capture and a shorter interval.
type codexDurableAckPoll struct {
	resolve     func() string
	capture     func(context.Context, string) (string, error)
	sessionName string
	interval    time.Duration
}

// pollCodexDurableAck waits observe-only for the user row matching one
// send. It never sends keys: on entry the message may already be
// accepted, so any keystroke risks duplicating it. On budget expiry it
// distinguishes still-queued (unflushed, held by the CLI) from truly
// lost via one final pane read.
func pollCodexDurableAck(ctx context.Context, message string, since time.Time, minOffset int64, timeout time.Duration, poll codexDurableAckPoll) (CodexDurableAck, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := poll.interval
	if interval <= 0 {
		interval = codexDurableAckPollInterval
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	check := func() (time.Time, bool) {
		if poll.resolve == nil {
			return time.Time{}, false
		}
		path := poll.resolve()
		if strings.TrimSpace(path) == "" {
			return time.Time{}, false
		}
		return codexRolloutUserMessageSince(path, message, since, minOffset)
	}
	if at, ok := check(); ok {
		return CodexDurableAck{Outcome: CodexDurableAckConfirmed, Latency: time.Since(since), ProofPath: poll.resolve(), RowTimestamp: at}, nil
	}
	for {
		select {
		case <-deadline.Done():
			if poll.capture != nil && strings.TrimSpace(poll.sessionName) != "" {
				if captured, err := poll.capture(deadline, poll.sessionName); err == nil && codexPaneShowsQueuedMessage(captured, message) {
					return CodexDurableAck{Outcome: CodexDurableAckUnflushed, Latency: time.Since(since)}, nil
				}
			}
			if ctx.Err() != nil {
				return CodexDurableAck{}, ctx.Err()
			}
			return CodexDurableAck{}, fmt.Errorf("Codex input not durably acknowledged after %s", timeout)
		case <-ticker.C:
			if at, ok := check(); ok {
				return CodexDurableAck{Outcome: CodexDurableAckConfirmed, Latency: time.Since(since), ProofPath: poll.resolve(), RowTimestamp: at}, nil
			}
		}
	}
}

// resolveCodexRolloutPathNoMu is the read-only half of
// resolveCodexRolloutPathLocked for callers that must not take
// session.mu: the live send path and the durability watcher both run
// concurrent with a turn that owns mu for its full lifetime. Identity
// comes from rolloutMu; workingDir/accountRoot are immutable after
// construction. Unlike the locking variant it never writes the
// binding back — the turn path owns that; repeated resolution is
// cheap next to a poll interval.
func resolveCodexRolloutPathNoMu(session *codexInteractiveSession, since time.Time) string {
	if session == nil {
		return ""
	}
	path, threadID := codexRolloutIdentity(session)
	if threadID != "" {
		if resolved := findCodexRolloutForThread(threadID, session.accountRoot); resolved != "" {
			return resolved
		}
		return path
	}
	if strings.TrimSpace(path) != "" {
		return path
	}
	return findCodexRolloutByWorkingDirExcluding(since, session.workingDir, boundCodexRolloutPaths(session), session.accountRoot)
}

// stashCodexDurableReceiptForSend snapshots the rollout offset before a
// live send so the phase-2 arbiter and AwaitCodexInputDurable scope
// their user-row match to rows this send could have produced.
func stashCodexDurableReceiptForSend(ownerSessionID, message string, since time.Time) {
	session, ok := codexPersistentRegistry.Get(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return
	}
	path, _ := codexRolloutIdentity(session)
	var offset int64
	if strings.TrimSpace(path) != "" {
		if info, err := os.Stat(path); err == nil {
			offset = info.Size()
		}
	}
	stashCodexDurableReceipt(session, message, path, offset, since)
}

// codexArbiterAfterPaneFailure is phase 2 of live submit confirmation:
// observe-only rollout arbitration after the pane loop failed. It never
// sends keys. A rollout match converts the pane failure into success
// (logging the disagreement as provider drift); a still-queued pane
// becomes accepted-but-unflushed; anything else keeps the original
// pane error with the arbiter note attached.
func codexArbiterAfterPaneFailure(ctx context.Context, ownerSessionID, sessionName, message string, since time.Time, phase1Err error) error {
	session, ok := codexPersistentRegistry.Get(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return phase1Err
	}
	receipt, ok := peekCodexDurableReceipt(session, message)
	minOffset := int64(0)
	if ok {
		since = receipt.since
		minOffset = receipt.offset
	}
	budget := codexDurableAckBudget()
	ack, err := pollCodexDurableAck(ctx, message, since, minOffset, budget, codexDurableAckPoll{
		resolve:     func() string { return resolveCodexRolloutPathNoMu(session, since) },
		capture:     captureCodexPane,
		sessionName: sessionName,
	})
	if err == nil && ack.Outcome == CodexDurableAckConfirmed {
		log.Printf("[codex-durable-ack] PANE/FILE DISAGREE owner=%s latency=%dms proof=%s phase1=%v",
			ownerSessionID, ack.Latency.Milliseconds(), ack.ProofPath, phase1Err)
		return nil
	}
	if err == nil && ack.Outcome == CodexDurableAckUnflushed {
		return &CodexInputUnflushedError{OwnerSessionID: ownerSessionID, Latency: ack.Latency}
	}
	if ctx.Err() != nil {
		return phase1Err
	}
	return fmt.Errorf("%w; durable arbiter found no rollout proof within %s", phase1Err, budget)
}

// AwaitCodexInputDurable waits for the rollout proof that a previous
// SendCodexInteractiveInput reached the CLI. It is the durability half
// of the two-stage delivery receipt (fast pane ack, then this): the
// server watcher calls it after every fast ack and promotes the chat
// tick on confirmation. A zero timeout selects the env-tuned budget.
func AwaitCodexInputDurable(ctx context.Context, ownerSessionID, message string, timeout time.Duration) (CodexDurableAck, error) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return CodexDurableAck{}, fmt.Errorf("owner session id is required")
	}
	if strings.TrimSpace(message) == "" {
		return CodexDurableAck{}, fmt.Errorf("message is empty")
	}
	if timeout <= 0 {
		timeout = codexDurableAckBudget()
	}
	session, ok := codexPersistentRegistry.Get(ownerSessionID)
	if !ok || session == nil {
		return CodexDurableAck{}, fmt.Errorf("no active Codex interactive session registered for owner session %s", ownerSessionID)
	}
	receipt, ok := peekCodexDurableReceipt(session, message)
	since := time.Now().Add(-2 * time.Minute)
	var minOffset int64
	if ok {
		since = receipt.since
		minOffset = receipt.offset
	}
	tmuxName := session.tmuxSessionName
	ack, err := pollCodexDurableAck(ctx, message, since, minOffset, timeout, codexDurableAckPoll{
		resolve:     func() string { return resolveCodexRolloutPathNoMu(session, since) },
		capture:     captureCodexPane,
		sessionName: tmuxName,
	})
	if err != nil {
		return CodexDurableAck{}, err
	}
	if ack.Outcome == CodexDurableAckConfirmed {
		log.Printf("[codex-durable-ack] owner=%s latency=%dms proof=%s", ownerSessionID, ack.Latency.Milliseconds(), ack.ProofPath)
	} else {
		log.Printf("[codex-durable-ack] owner=%s UNFLUSHED after %dms (still natively queued)", ownerSessionID, ack.Latency.Milliseconds())
	}
	return ack, nil
}
