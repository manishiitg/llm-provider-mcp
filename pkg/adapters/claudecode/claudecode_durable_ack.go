package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Durable submit confirmation for the Claude Code tmux lane: the
// observe-only transcript arbiter behind AwaitClaudeInputDurable.
//
// Live evidence (2026-09-19 probes, haiku): a mid-turn steer lands a
// `queue-operation/enqueue` row ~500ms after send-keys with the full
// text. A steer during text generation then drains via `dequeue`
// followed by its `user` row; a steer during a tool call drains via
// `remove` (full text, no user row ever — the CLI folds it into a
// system-reminder with the tool result instead). So the transcript
// itself carries both halves of the verdict — no pane scrape needed:
//
//   - user row with our text, or a queue drain (remove with our
//     text, or dequeue after our enqueue) -> confirmed (the CLI
//     consumed the send; model obedience is out of scope)
//   - enqueue row but no drain at budget expiry ->
//     accepted_but_unflushed (the CLI holds it; unconfirmed, not failed)
//   - neither -> timeout error (failed)
//
// Pasted sends are wrapped in <pasted_content> tags; matching strips
// them. Offset scoping (plus exact text) means an identical earlier
// message already in the stream cannot confirm a new one.

type ClaudeDurableAckOutcome string

const (
	// ClaudeDurableAckConfirmed means the session transcript contains
	// the user row for this send.
	ClaudeDurableAckConfirmed ClaudeDurableAckOutcome = "confirmed"
	// ClaudeDurableAckUnflushed means the budget expired while the
	// transcript showed the send enqueued but not yet a user row.
	// The CLI holds it; durability is unconfirmed, not failed.
	ClaudeDurableAckUnflushed ClaudeDurableAckOutcome = "accepted_but_unflushed"
)

// ClaudeDurableAck carries the transcript proof for one send.
type ClaudeDurableAck struct {
	Outcome      ClaudeDurableAckOutcome
	Latency      time.Duration
	ProofPath    string
	RowTimestamp time.Time
}

// ClaudeInputUnflushedError reports accepted-but-unflushed through the
// error return so existing err != nil callers stay safe (they treat it
// as a failure) while confirmation-aware callers can distinguish it
// with errors.As.
type ClaudeInputUnflushedError struct {
	OwnerSessionID string
	Latency        time.Duration
}

func (e *ClaudeInputUnflushedError) Error() string {
	return fmt.Sprintf("Claude input accepted but unflushed after %s (still natively queued; durability unconfirmed, not failed)", e.Latency.Round(100*time.Millisecond))
}

const (
	// Default 60s: the probe saw mid-turn flush in ~7s and intake in
	// ~3s. A steer held past a long turn boundary reports unflushed
	// (truthful — the enqueue row proves acceptance), never failed.
	claudeDurableAckBudgetDefault = 60 * time.Second
	claudeDurableAckBudgetMin     = 5 * time.Second
	claudeDurableAckBudgetMax     = 300 * time.Second
	claudeDurableAckPollInterval  = 250 * time.Millisecond
)

// claudeDurableAckBudget bounds the observe-only transcript arbiter.
// CLAUDE_DURABLE_ACK_SECONDS overrides the default; values outside
// [5,300] are clamped so a broken env cannot hang sends or neuter
// the arbiter.
func claudeDurableAckBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CLAUDE_DURABLE_ACK_SECONDS"))
	if raw == "" {
		return claudeDurableAckBudgetDefault
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return claudeDurableAckBudgetDefault
	}
	budget := time.Duration(seconds) * time.Second
	if budget < claudeDurableAckBudgetMin {
		return claudeDurableAckBudgetMin
	}
	if budget > claudeDurableAckBudgetMax {
		return claudeDurableAckBudgetMax
	}
	return budget
}

// claudePendingDurableAck is the pre-send snapshot that scopes one send's
// durability proof. Occurrence disambiguates identical sends queued
// before any row lands: the nth stashed send needs the nth matching
// proof row.
type claudePendingDurableAck struct {
	message    string
	path       string
	offset     int64
	since      time.Time
	occurrence int
}

const (
	claudeMaxPendingDurableAcks = 8
	claudePendingDurableAckTTL  = 10 * time.Minute
)

func stashClaudeDurableReceipt(session *claudeInteractivePersistentSession, message, path string, offset int64, since time.Time) {
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
		if time.Since(pending.since) < claudePendingDurableAckTTL {
			kept = append(kept, pending)
		}
	}
	// Occurrence is send-ordered: stashes run inside the
	// broker-serialized send, so 1 + identical unexpired already
	// pending is this send's position among identical queued sends.
	occurrence := 1
	for _, pending := range kept {
		if pending.message == message {
			occurrence++
		}
	}
	kept = append(kept, claudePendingDurableAck{message: message, path: path, offset: offset, since: since, occurrence: occurrence})
	if len(kept) > claudeMaxPendingDurableAcks {
		kept = append([]claudePendingDurableAck(nil), kept[len(kept)-claudeMaxPendingDurableAcks:]...)
	}
	session.pendingDurable = kept
}

// peekClaudeDurableReceipt returns the earliest pending snapshot for an
// identical message without consuming it. Production waits use
// takeClaudeDurableReceipt; peek remains for introspection and tests.
func peekClaudeDurableReceipt(session *claudeInteractivePersistentSession, message string) (claudePendingDurableAck, bool) {
	if session == nil {
		return claudePendingDurableAck{}, false
	}
	message = strings.TrimSpace(message)
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	for _, pending := range session.pendingDurable {
		if pending.message == message && time.Since(pending.since) < claudePendingDurableAckTTL {
			return pending, true
		}
	}
	return claudePendingDurableAck{}, false
}

// takeClaudeDurableReceipt removes and returns the earliest pending
// snapshot for an identical message. FIFO consumption plus the
// receipt's occurrence binds each wait to distinct proof: the nth
// take needs the nth matching row, so identical sends queued before
// any row lands (same offset on every receipt) cannot share one row.
// Takes serialize under this lock, but take order is acquisition
// order, not send order — the occurrence threshold keeps k takes
// needing k distinct rows however watchers schedule.
func takeClaudeDurableReceipt(session *claudeInteractivePersistentSession, message string) (claudePendingDurableAck, bool) {
	if session == nil {
		return claudePendingDurableAck{}, false
	}
	message = strings.TrimSpace(message)
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	for i, pending := range session.pendingDurable {
		if pending.message == message && time.Since(pending.since) < claudePendingDurableAckTTL {
			out := pending
			session.pendingDurable = append(session.pendingDurable[:i], session.pendingDurable[i+1:]...)
			return out, true
		}
	}
	return claudePendingDurableAck{}, false
}

var claudePastedContentTag = regexp.MustCompile(`</?pasted_content[^>]*>`)

// claudeNormalizeRowText strips the <pasted_content> envelope pasted
// sends are wrapped in, so transcript rows compare against the raw
// sent message.
func claudeNormalizeRowText(text string) string {
	text = claudePastedContentTag.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func claudeRowTextMatches(rowText, message string) bool {
	rowText = claudeNormalizeRowText(rowText)
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

// claudeTranscriptRow is the minimal projection of a session JSONL row
// the arbiter needs. User content is either a string (typed/pasted
// user text — what we match) or an array (tool results — skipped).
type claudeTranscriptRow struct {
	Type      string `json:"type"`
	Operation string `json:"operation"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Content   string `json:"content"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func claudeRowTimestamp(row claudeTranscriptRow) time.Time {
	if ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(row.Timestamp)); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(row.Timestamp)); err == nil {
		return ts
	}
	return time.Time{}
}

func claudeUserRowText(row claudeTranscriptRow) (string, bool) {
	if row.Type != "user" || strings.TrimSpace(row.Message.Role) != "user" {
		return "", false
	}
	raw := strings.TrimSpace(string(row.Message.Content))
	if raw == "" || strings.HasPrefix(raw, "[") {
		return "", false
	}
	var text string
	if err := json.Unmarshal(row.Message.Content, &text); err != nil {
		return "", false
	}
	return text, true
}

// claudeTranscriptUserMessageOccurrenceSince returns the timestamp of the
// occurrence-th user row at byte offset >= minOffset, timestamped at
// or after since, whose text matches message — or false when fewer
// rows match. Occurrence values below 1 mean 1. Malformed lines are
// skipped; a missing file simply has no proof yet. Rows without a
// parseable timestamp still count when past the offset.
func claudeTranscriptUserMessageOccurrenceSince(path, message string, since time.Time, minOffset int64, occurrence int) (time.Time, bool) {
	return claudeTranscriptRowOccurrenceSince(path, message, since, minOffset, occurrence, claudeUserRowText)
}

// claudeTranscriptUserMessageSince returns the timestamp of the first
// user row at byte offset >= minOffset, timestamped at or after since,
// whose text matches message — or false when no row does. Malformed
// lines are skipped; a missing file simply has no proof yet. Rows
// without a parseable timestamp still count when past the offset.
func claudeTranscriptUserMessageSince(path, message string, since time.Time, minOffset int64) (time.Time, bool) {
	return claudeTranscriptUserMessageOccurrenceSince(path, message, since, minOffset, 1)
}

// claudeTranscriptEnqueueOccurrenceSince returns the timestamp of the
// occurrence-th queue-operation/enqueue row at byte offset >=
// minOffset, timestamped at or after since, whose content matches
// message — or false when fewer rows match. Occurrence values below 1
// mean 1.
func claudeTranscriptEnqueueOccurrenceSince(path, message string, since time.Time, minOffset int64, occurrence int) (time.Time, bool) {
	extract := func(row claudeTranscriptRow) (string, bool) {
		if row.Type != "queue-operation" || strings.TrimSpace(row.Operation) != "enqueue" {
			return "", false
		}
		return row.Content, true
	}
	return claudeTranscriptRowOccurrenceSince(path, message, since, minOffset, occurrence, extract)
}

// claudeTranscriptEnqueueSince returns the timestamp of the first
// queue-operation/enqueue row at byte offset >= minOffset,
// timestamped at or after since, whose content matches message.
func claudeTranscriptEnqueueSince(path, message string, since time.Time, minOffset int64) (time.Time, bool) {
	return claudeTranscriptEnqueueOccurrenceSince(path, message, since, minOffset, 1)
}

// scanClaudeTranscriptRows visits each parseable JSONL row at byte offset
// >= minOffset in file order with the row's starting offset. Malformed
// lines are skipped; a missing file visits nothing. Returning false
// stops the scan.
func scanClaudeTranscriptRows(path string, minOffset int64, visit func(row claudeTranscriptRow, offset int64) bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if minOffset < 0 {
		minOffset = 0
	}
	if int64(len(raw)) <= minOffset {
		return
	}
	offset := minOffset
	for _, line := range strings.SplitAfter(string(raw[minOffset:]), "\n") {
		trimmed := strings.TrimSpace(line)
		start := offset
		offset += int64(len(line))
		if trimmed == "" {
			continue
		}
		var row claudeTranscriptRow
		if err := json.Unmarshal([]byte(trimmed), &row); err != nil {
			continue
		}
		if !visit(row, start) {
			return
		}
	}
}

// claudeRowAfterSince reports whether the row is inside the send window.
// Rows with an unparseable timestamp still count when past the offset.
func claudeRowAfterSince(row claudeTranscriptRow, since time.Time) bool {
	at := claudeRowTimestamp(row)
	return at.IsZero() || !at.Before(since)
}

func claudeTranscriptRowOccurrenceSince(path, message string, since time.Time, minOffset int64, occurrence int, extract func(claudeTranscriptRow) (string, bool)) (time.Time, bool) {
	if occurrence < 1 {
		occurrence = 1
	}
	var found time.Time
	matched := 0
	scanClaudeTranscriptRows(path, minOffset, func(row claudeTranscriptRow, _ int64) bool {
		text, ok := extract(row)
		if !ok || !claudeRowTextMatches(text, message) || !claudeRowAfterSince(row, since) {
			return true
		}
		matched++
		if matched < occurrence {
			return true
		}
		if at := claudeRowTimestamp(row); !at.IsZero() {
			found = at
		} else {
			found = time.Now()
		}
		return false
	})
	return found, !found.IsZero()
}

// claudeTranscriptDrainOccurrenceSince returns the timestamp of the
// occurrence-th queue-drain event proving the CLI consumed our queued
// sends: queue-operation/remove rows carrying our text and
// queue-operation/dequeue rows positioned after our enqueue row
// (dequeue rows are contentless, so the match is positional — sends
// are broker-serialized per session and the CLI drains FIFO), counted
// in file order. Each event means one send reached the model loop;
// whether the model obeys it is beyond the delivery contract.
// Occurrence values below 1 mean 1.
func claudeTranscriptDrainOccurrenceSince(path, message string, since time.Time, minOffset int64, occurrence int) (time.Time, bool) {
	if occurrence < 1 {
		occurrence = 1
	}
	var found time.Time
	drained := 0
	var enqueueOffset int64 = -1
	record := func(row claudeTranscriptRow) bool {
		drained++
		if drained < occurrence {
			return true
		}
		if at := claudeRowTimestamp(row); !at.IsZero() {
			found = at
		} else {
			found = time.Now()
		}
		return false
	}
	scanClaudeTranscriptRows(path, minOffset, func(row claudeTranscriptRow, offset int64) bool {
		if row.Type != "queue-operation" {
			return true
		}
		switch strings.TrimSpace(row.Operation) {
		case "enqueue":
			if enqueueOffset < 0 && claudeRowTextMatches(row.Content, message) && claudeRowAfterSince(row, since) {
				enqueueOffset = offset
			}
		case "remove":
			if claudeRowTextMatches(row.Content, message) && claudeRowAfterSince(row, since) {
				return record(row)
			}
		case "dequeue":
			if enqueueOffset >= 0 && offset > enqueueOffset && claudeRowAfterSince(row, since) {
				return record(row)
			}
		}
		return true
	})
	return found, !found.IsZero()
}

// claudeTranscriptDrainSince returns the timestamp of the first
// queue-drain row proving the CLI consumed our queued send: a
// queue-operation/remove carrying our text, or any queue-operation/dequeue
// positioned after our enqueue row (dequeue rows are contentless, so the
// match is positional — sends are broker-serialized per session and the
// CLI drains FIFO). Either means the message reached the model loop;
// whether the model obeys it is beyond the delivery contract.
func claudeTranscriptDrainSince(path, message string, since time.Time, minOffset int64) (time.Time, bool) {
	return claudeTranscriptDrainOccurrenceSince(path, message, since, minOffset, 1)
}

// claudeDurableAckPoll tunes pollClaudeDurableAck. Zero values select
// live defaults; tests inject resolve and a shorter interval.
// Occurrence is the taken receipt's 1-based position among identical
// queued sends: the wait needs the occurrence-th matching row. Values
// below 1 mean 1.
type claudeDurableAckPoll struct {
	resolve    func() string
	interval   time.Duration
	occurrence int
}

// pollClaudeDurableAck waits observe-only for the transcript proof of one
// send: its user row, or the queue-drain row proving the CLI consumed it
// (tool-time steers drain via remove with no user row at all). It never
// sends keys: on entry the message may already be accepted, so any
// keystroke risks duplicating it. On budget expiry it distinguishes
// still-queued (unflushed — the enqueue row proves the CLI holds it)
// from truly lost via the same transcript.
func pollClaudeDurableAck(ctx context.Context, message string, since time.Time, minOffset int64, timeout time.Duration, poll claudeDurableAckPoll) (ClaudeDurableAck, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := poll.interval
	if interval <= 0 {
		interval = claudeDurableAckPollInterval
	}
	occurrence := poll.occurrence
	if occurrence < 1 {
		occurrence = 1
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	resolve := poll.resolve
	if resolve == nil {
		resolve = func() string { return "" }
	}
	check := func() (time.Time, bool) {
		path := resolve()
		if strings.TrimSpace(path) == "" {
			return time.Time{}, false
		}
		if at, ok := claudeTranscriptUserMessageOccurrenceSince(path, message, since, minOffset, occurrence); ok {
			return at, true
		}
		return claudeTranscriptDrainOccurrenceSince(path, message, since, minOffset, occurrence)
	}
	if at, ok := check(); ok {
		return ClaudeDurableAck{Outcome: ClaudeDurableAckConfirmed, Latency: time.Since(since), ProofPath: resolve(), RowTimestamp: at}, nil
	}
	for {
		select {
		case <-deadline.Done():
			if _, ok := claudeTranscriptEnqueueOccurrenceSince(resolve(), message, since, minOffset, occurrence); ok {
				return ClaudeDurableAck{Outcome: ClaudeDurableAckUnflushed, Latency: time.Since(since)}, nil
			}
			if ctx.Err() != nil {
				return ClaudeDurableAck{}, ctx.Err()
			}
			return ClaudeDurableAck{}, fmt.Errorf("Claude input not durably acknowledged after %s", timeout)
		case <-ticker.C:
			if at, ok := check(); ok {
				return ClaudeDurableAck{Outcome: ClaudeDurableAckConfirmed, Latency: time.Since(since), ProofPath: resolve(), RowTimestamp: at}, nil
			}
		}
	}
}

// resolveClaudeTranscriptPathNoMu resolves the session transcript from
// immutable-after-construction identity (native ID, working dir, home)
// without taking session.mu: the live send path and the durability
// watcher both run concurrent with a turn that owns mu for its full
// lifetime.
func resolveClaudeTranscriptPathNoMu(session *claudeInteractivePersistentSession) string {
	if session == nil {
		return ""
	}
	path, err := resolveClaudeTranscriptPath(session.nativeSessionID, session.workingDir, true, session.accountHome)
	if err != nil {
		return ""
	}
	return path
}

// stashClaudeDurableReceiptForSend snapshots the transcript offset before
// a live-input send so the later durability watch only matches rows the
// CLI wrote for this send.
func stashClaudeDurableReceiptForSend(ownerSessionID, message string, since time.Time) {
	session, ok := claudeInteractivePersistentRegistry.Get(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return
	}
	path := resolveClaudeTranscriptPathNoMu(session)
	var offset int64
	if strings.TrimSpace(path) != "" {
		if info, err := os.Stat(path); err == nil {
			offset = info.Size()
		}
	}
	stashClaudeDurableReceipt(session, message, path, offset, since)
}

// InteractiveSessionRegistered reports whether the owner's Claude Code
// TUI is registered — the live-injection transport exists. Cheap
// registry lookup (no tmux round-trip) for steer gating; delivery
// re-verifies liveness. Mirrors the SendClaudeCodeInput registry.
func InteractiveSessionRegistered(ownerSessionID string) bool {
	_, ok := activeClaudeInteractiveOwner(ownerSessionID)
	return ok
}

// AwaitClaudeInputDurable waits for the transcript proof that a previous
// SendClaudeCodeInput reached the CLI. It is the durability half of
// the two-stage delivery receipt (fast pane ack, then this): the
// server watcher calls it after every fast ack and promotes the chat
// tick on confirmation. A zero timeout selects the env-tuned budget.
func AwaitClaudeInputDurable(ctx context.Context, ownerSessionID, message string, timeout time.Duration) (ClaudeDurableAck, error) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return ClaudeDurableAck{}, fmt.Errorf("owner session id is required")
	}
	if strings.TrimSpace(message) == "" {
		return ClaudeDurableAck{}, fmt.Errorf("message is empty")
	}
	if timeout <= 0 {
		timeout = claudeDurableAckBudget()
	}
	session, ok := claudeInteractivePersistentRegistry.Get(ownerSessionID)
	if !ok || session == nil {
		return ClaudeDurableAck{}, fmt.Errorf("no active Claude Code tmux session registered for owner session %s", ownerSessionID)
	}
	receipt, ok := takeClaudeDurableReceipt(session, message)
	since := time.Now().Add(-2 * time.Minute)
	var minOffset int64
	occurrence := 1
	if ok {
		since = receipt.since
		minOffset = receipt.offset
		occurrence = receipt.occurrence
	}
	ack, err := pollClaudeDurableAck(ctx, message, since, minOffset, timeout, claudeDurableAckPoll{
		resolve:    func() string { return resolveClaudeTranscriptPathNoMu(session) },
		occurrence: occurrence,
	})
	if err != nil {
		return ClaudeDurableAck{}, err
	}
	if ack.Outcome == ClaudeDurableAckConfirmed {
		log.Printf("[claude-durable-ack] owner=%s latency=%dms proof=%s", ownerSessionID, ack.Latency.Milliseconds(), ack.ProofPath)
	} else {
		log.Printf("[claude-durable-ack] owner=%s UNFLUSHED after %dms (still natively queued)", ownerSessionID, ack.Latency.Milliseconds())
	}
	return ack, nil
}
