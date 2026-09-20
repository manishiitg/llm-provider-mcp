package picli

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Durable submit acknowledgement for Pi live input.
//
// Pi's marker stream (written by the injected mlp-marker.ts extension)
// is the authoritative second opinion behind the pane: Pi appends a
// message_end user row carrying the exact message text, timestamped in
// epoch milliseconds. A busy steer that Pi natively queues lands as a
// fresh turn (turn_start + user row, no new agent_start) when the queue
// flushes, so the match keys on text + pre-paste offset only, never on
// turn boundaries.
//
// The send path already consults markers inline (6s budget); this file
// adds the long-budget observe-only arbiter for pane failures plus the
// Await entry point the server durability watcher calls after every
// fast ack. See docs/refactor/codex_durable_ack_p0.md in
// mcp-agent-builder-go for the two-stage receipt design.

// PiDurableAckOutcome is the verdict of the marker arbiter.
type PiDurableAckOutcome string

const (
	// PiDurableAckConfirmed means the marker stream contains the user
	// row for this send.
	PiDurableAckConfirmed PiDurableAckOutcome = "confirmed"
	// PiDurableAckUnflushed means the budget expired while the pane
	// still positively showed the message held (natively queued, or
	// taken by an active turn whose marker row is still pending). Held
	// by the CLI, durability unconfirmed, not failed.
	PiDurableAckUnflushed PiDurableAckOutcome = "accepted_but_unflushed"
)

// PiDurableAck carries the marker proof for one send.
type PiDurableAck struct {
	Outcome      PiDurableAckOutcome
	Latency      time.Duration
	ProofPath    string
	RowTimestamp time.Time
}

// PiInputUnflushedError reports accepted-but-unflushed through the
// error return so existing err != nil callers stay safe while
// confirmation-aware callers can distinguish it with errors.As.
type PiInputUnflushedError struct {
	OwnerSessionID string
	Latency        time.Duration
}

func (e *PiInputUnflushedError) Error() string {
	return fmt.Sprintf("Pi input accepted but unflushed after %s (still held by the CLI; durability unconfirmed, not failed)", e.Latency.Round(100*time.Millisecond))
}

const (
	piDurableAckBudgetDefault = 60 * time.Second
	piDurableAckBudgetMin     = 5 * time.Second
	piDurableAckBudgetMax     = 300 * time.Second
	piDurableAckPollInterval  = 250 * time.Millisecond
)

// piDurableAckBudget bounds the observe-only marker arbiter.
// PI_DURABLE_ACK_SECONDS overrides the default; values outside
// [5,300] are clamped so a broken env cannot hang sends or neuter
// the arbiter.
func piDurableAckBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("PI_DURABLE_ACK_SECONDS"))
	if raw == "" {
		return piDurableAckBudgetDefault
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return piDurableAckBudgetDefault
	}
	budget := time.Duration(seconds) * time.Second
	if budget < piDurableAckBudgetMin {
		return piDurableAckBudgetMin
	}
	if budget > piDurableAckBudgetMax {
		return piDurableAckBudgetMax
	}
	return budget
}

// piPendingDurableAck is the pre-paste snapshot that scopes one send's
// durability proof. Offset scoping (plus text match) means an identical
// earlier message already in the stream cannot confirm a new one.
type piPendingDurableAck struct {
	message string
	path    string
	offset  int64
	since   time.Time
}

const (
	piMaxPendingDurableAcks = 8
	piPendingDurableAckTTL  = 10 * time.Minute
)

func stashPiDurableReceipt(session *piInteractiveSession, message, path string, offset int64, since time.Time) {
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
		if time.Since(pending.since) < piPendingDurableAckTTL {
			kept = append(kept, pending)
		}
	}
	kept = append(kept, piPendingDurableAck{message: message, path: path, offset: offset, since: since})
	if len(kept) > piMaxPendingDurableAcks {
		kept = append([]piPendingDurableAck(nil), kept[len(kept)-piMaxPendingDurableAcks:]...)
	}
	session.pendingDurable = kept
}

// peekPiDurableReceipt returns the earliest pending snapshot for an
// identical message. Receipts are deliberately not consumed: sends are
// broker-serialized per session and Pi queues FIFO, so offset+text
// scoping self-disambiguates repeats without cross-goroutine ownership.
func peekPiDurableReceipt(session *piInteractiveSession, message string) (piPendingDurableAck, bool) {
	if session == nil {
		return piPendingDurableAck{}, false
	}
	message = strings.TrimSpace(message)
	session.durableMu.Lock()
	defer session.durableMu.Unlock()
	for _, pending := range session.pendingDurable {
		if pending.message == message && time.Since(pending.since) < piPendingDurableAckTTL {
			return pending, true
		}
	}
	return piPendingDurableAck{}, false
}

// piUserAckMarker returns the first message_end user row matching
// message, using the same compact-text + long-prefix rule as
// piMarkersAcknowledgeUserMessage, which delegates to it.
func piUserAckMarker(markers []piMarker, message string) (piMarker, bool) {
	want := piCompactDraftText(message)
	if want == "" {
		return piMarker{}, false
	}
	const longMessageRunes = 64
	for _, marker := range markers {
		if marker.Type != "message_end" || marker.Role != "user" {
			continue
		}
		got := piCompactDraftText(marker.Text)
		if got == "" {
			continue
		}
		if got == want {
			return marker, true
		}
		if len([]rune(want)) >= longMessageRunes && (strings.HasPrefix(got, want) || strings.HasPrefix(want, got)) {
			return marker, true
		}
	}
	return piMarker{}, false
}

// piPaneShowsQueuedMessage reports that message is sitting in Pi's
// native queue right now: a "Steering:" line with the message text in
// recent scrollback. No completion disqualifier is needed — this only
// runs when no marker ack exists, and a flush always emits the user
// row, so a visible queue with no ack is genuinely still held (or the
// extension is dead, for which "held per the TUI" is still the honest
// verdict over "failed").
func piPaneShowsQueuedMessage(captured, message string) bool {
	needle := strings.TrimSpace(strings.Split(strings.TrimSpace(message), "\n")[0])
	if len(needle) > 80 {
		needle = needle[:80]
	}
	if needle == "" {
		return false
	}
	// The banner carries the queued text inline ("Steering: <message>");
	// the next-lines fallback covers wrapped layouts.
	lines := strings.Split(captured, "\n")
	const window = 120
	start := 0
	if len(lines) > window {
		start = len(lines) - window
	}
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(stripPiANSI(lines[i]))
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "Steering:") {
			continue
		}
		if strings.Contains(line, needle) {
			return true
		}
		for j := i + 1; j < len(lines) && j <= i+3; j++ {
			if strings.Contains(strings.TrimSpace(stripPiANSI(lines[j])), needle) {
				return true
			}
		}
	}
	return false
}

// piDurableAckPoll tunes pollPiDurableAck. Zero values select live
// defaults; tests inject resolve/capture and a shorter interval.
type piDurableAckPoll struct {
	resolve     func() string
	capture     func(context.Context, string) (string, error)
	sessionName string
	interval    time.Duration
}

// pollPiDurableAck waits observe-only for the marker row matching one
// send. It never sends keys: on entry the message may already be
// accepted, so any keystroke risks duplicating it. On budget expiry it
// distinguishes still-held (natively queued, or taken by an active
// turn) from truly lost via one final pane read.
func pollPiDurableAck(ctx context.Context, message string, since time.Time, minOffset int64, timeout time.Duration, poll piDurableAckPoll) (PiDurableAck, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := poll.interval
	if interval <= 0 {
		interval = piDurableAckPollInterval
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	offset := minOffset
	check := func() (piMarker, bool) {
		if poll.resolve == nil {
			return piMarker{}, false
		}
		path := poll.resolve()
		if strings.TrimSpace(path) == "" {
			return piMarker{}, false
		}
		markers, nextOffset, err := readPiMarkersSince(path, offset)
		if err != nil {
			return piMarker{}, false
		}
		offset = nextOffset
		return piUserAckMarker(markers, message)
	}
	if marker, ok := check(); ok {
		return PiDurableAck{Outcome: PiDurableAckConfirmed, Latency: time.Since(since), ProofPath: poll.resolve(), RowTimestamp: time.UnixMilli(marker.TS)}, nil
	}
	for {
		select {
		case <-deadline.Done():
			if poll.capture != nil && strings.TrimSpace(poll.sessionName) != "" {
				if captured, err := poll.capture(deadline, poll.sessionName); err == nil {
					if piPaneShowsQueuedMessage(captured, message) {
						return PiDurableAck{Outcome: PiDurableAckUnflushed, Latency: time.Since(since)}, nil
					}
					if !piPaneShowsPromptDraft(captured, message) && piPaneHasStatusLine(captured) && !piPaneLooksIdle(captured) {
						return PiDurableAck{Outcome: PiDurableAckUnflushed, Latency: time.Since(since)}, nil
					}
				}
			}
			if ctx.Err() != nil {
				return PiDurableAck{}, ctx.Err()
			}
			return PiDurableAck{}, fmt.Errorf("Pi input not durably acknowledged after %s", timeout)
		case <-ticker.C:
			if marker, ok := check(); ok {
				return PiDurableAck{Outcome: PiDurableAckConfirmed, Latency: time.Since(since), ProofPath: poll.resolve(), RowTimestamp: time.UnixMilli(marker.TS)}, nil
			}
		}
	}
}

// resolvePiMarkerPathNoMu returns the session's marker path without
// taking session.mu. markerPath is immutable after construction, so
// the arbiter and the server watcher can resolve it concurrent with a
// turn that owns mu.
func resolvePiMarkerPathNoMu(session *piInteractiveSession) string {
	if session == nil {
		return ""
	}
	return session.markerPath
}

// stashPiDurableReceiptForSend snapshots the marker offset before a
// live send so the arbiter and AwaitPiInputDurable scope their user-row
// match to rows this send could have produced.
func stashPiDurableReceiptForSend(ownerSessionID, message string, since time.Time) {
	session, ok := activePiInteractiveSession(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return
	}
	path := resolvePiMarkerPathNoMu(session)
	var offset int64
	if strings.TrimSpace(path) != "" {
		if size, err := piMarkerFileSize(path); err == nil {
			offset = size
		}
	}
	stashPiDurableReceipt(session, message, path, offset, since)
}

// piArbiterAfterSubmitFailure is the long-budget observe-only marker
// arbitration after the inline submit check failed. It never sends
// keys. A marker match converts the failure into success (logging the
// disagreement as provider drift); a still-held pane becomes
// accepted-but-unflushed; anything else keeps the original error with
// the arbiter note attached.
func piArbiterAfterSubmitFailure(ctx context.Context, ownerSessionID, sessionName, message string, since time.Time, submitErr error) error {
	session, ok := activePiInteractiveSession(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return submitErr
	}
	receipt, ok := peekPiDurableReceipt(session, message)
	minOffset := int64(0)
	if ok {
		since = receipt.since
		minOffset = receipt.offset
	}
	budget := piDurableAckBudget()
	ack, err := pollPiDurableAck(ctx, message, since, minOffset, budget, piDurableAckPoll{
		resolve:     func() string { return resolvePiMarkerPathNoMu(session) },
		capture:     capturePiPane,
		sessionName: sessionName,
	})
	if err == nil && ack.Outcome == PiDurableAckConfirmed {
		log.Printf("[pi-durable-ack] PANE/FILE DISAGREE owner=%s latency=%dms proof=%s submit=%v",
			ownerSessionID, ack.Latency.Milliseconds(), ack.ProofPath, submitErr)
		return nil
	}
	if err == nil && ack.Outcome == PiDurableAckUnflushed {
		return &PiInputUnflushedError{OwnerSessionID: ownerSessionID, Latency: ack.Latency}
	}
	if ctx.Err() != nil {
		return submitErr
	}
	return fmt.Errorf("%w; durable arbiter found no marker proof within %s", submitErr, budget)
}

// AwaitPiInputDurable waits for the marker proof that a previous
// SendPiInteractiveInput reached the CLI. It is the durability half
// of the two-stage delivery receipt (fast pane ack, then this): the
// server watcher calls it after every fast ack and promotes the chat
// tick on confirmation. A zero timeout selects the env-tuned budget.
func AwaitPiInputDurable(ctx context.Context, ownerSessionID, message string, timeout time.Duration) (PiDurableAck, error) {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	if ownerSessionID == "" {
		return PiDurableAck{}, fmt.Errorf("owner session id is required")
	}
	if strings.TrimSpace(message) == "" {
		return PiDurableAck{}, fmt.Errorf("message is empty")
	}
	if timeout <= 0 {
		timeout = piDurableAckBudget()
	}
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || session == nil {
		return PiDurableAck{}, fmt.Errorf("no active Pi interactive session registered for owner session %s", ownerSessionID)
	}
	receipt, ok := peekPiDurableReceipt(session, message)
	since := time.Now().Add(-2 * time.Minute)
	var minOffset int64
	if ok {
		since = receipt.since
		minOffset = receipt.offset
	}
	tmuxName := session.tmuxSessionName
	ack, err := pollPiDurableAck(ctx, message, since, minOffset, timeout, piDurableAckPoll{
		resolve:     func() string { return resolvePiMarkerPathNoMu(session) },
		capture:     capturePiPane,
		sessionName: tmuxName,
	})
	if err != nil {
		return PiDurableAck{}, err
	}
	if ack.Outcome == PiDurableAckConfirmed {
		log.Printf("[pi-durable-ack] owner=%s latency=%dms proof=%s", ownerSessionID, ack.Latency.Milliseconds(), ack.ProofPath)
	} else {
		log.Printf("[pi-durable-ack] owner=%s UNFLUSHED after %dms (still held by Pi)", ownerSessionID, ack.Latency.Milliseconds())
	}
	return ack, nil
}
