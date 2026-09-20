package picli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writePiDurableAckFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "markers.jsonl")
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func piUserMarker(ts int64, text string) string {
	escaped := strings.ReplaceAll(text, `"`, `\"`)
	return fmt.Sprintf(`{"type":"message_end","ts":%d,"role":"user","text":"%s"}`, ts, escaped)
}

func TestPiUserAckMarker(t *testing.T) {
	msg := "New instruction: end with PIFLUSH_4D."
	markers := []piMarker{
		{Type: "agent_start", TS: 1},
		{Type: "message_end", TS: 2, Role: "assistant", Text: "earlier answer"},
		{Type: "message_end", TS: 3, Role: "user", Text: msg},
	}
	got, ok := piUserAckMarker(markers, msg)
	if !ok || got.TS != 3 {
		t.Fatalf("ack = (%+v %v), want the user row", got, ok)
	}
	if !piMarkersAcknowledgeUserMessage(markers, msg) {
		t.Fatal("piMarkersAcknowledgeUserMessage must delegate to the same match")
	}
	if _, ok := piUserAckMarker(markers, "some other message"); ok {
		t.Fatal("a different message must not match")
	}
	if _, ok := piUserAckMarker([]piMarker{{Type: "message_end", TS: 4, Role: "assistant", Text: msg}}, msg); ok {
		t.Fatal("an assistant row carrying the text is not user acceptance")
	}
	if _, ok := piUserAckMarker(nil, msg); ok {
		t.Fatal("no markers must not confirm")
	}

	long := "LIVE_FOLLOWUP_" + strings.Repeat("0123456789abcdef", 8)
	longMarkers := []piMarker{{Type: "message_end", TS: 5, Role: "user", Text: long[:80]}}
	if _, ok := piUserAckMarker(longMarkers, long); !ok {
		t.Fatal("expected a shared prefix to confirm a long send")
	}
}

func TestPiUserAckMarkers(t *testing.T) {
	msg := "New instruction: end with PIFLUSH_4D."
	markers := []piMarker{
		{Type: "agent_start", TS: 1},
		{Type: "message_end", TS: 2, Role: "user", Text: msg},
		{Type: "message_end", TS: 3, Role: "assistant", Text: msg},
		{Type: "message_end", TS: 4, Role: "user", Text: msg},
	}
	matched := piUserAckMarkers(markers, msg)
	if len(matched) != 2 || matched[0].TS != 2 || matched[1].TS != 4 {
		t.Fatalf("matched = %+v, want both user rows in stream order", matched)
	}
	if got := piUserAckMarkers(nil, msg); len(got) != 0 {
		t.Fatalf("matched = %+v, want none", got)
	}
}

func TestPiDurableAckBudget(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("PI_DURABLE_ACK_SECONDS", "")
		if got := piDurableAckBudget(); got != 60*time.Second {
			t.Fatalf("budget = %v, want 60s", got)
		}
	})
	t.Run("override", func(t *testing.T) {
		t.Setenv("PI_DURABLE_ACK_SECONDS", "90")
		if got := piDurableAckBudget(); got != 90*time.Second {
			t.Fatalf("budget = %v, want 90s", got)
		}
	})
	t.Run("clamped", func(t *testing.T) {
		t.Setenv("PI_DURABLE_ACK_SECONDS", "1")
		if got := piDurableAckBudget(); got != 5*time.Second {
			t.Fatalf("budget = %v, want 5s floor", got)
		}
		t.Setenv("PI_DURABLE_ACK_SECONDS", "9999")
		if got := piDurableAckBudget(); got != 300*time.Second {
			t.Fatalf("budget = %v, want 300s ceiling", got)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		t.Setenv("PI_DURABLE_ACK_SECONDS", "soon")
		if got := piDurableAckBudget(); got != 60*time.Second {
			t.Fatalf("budget = %v, want 60s default", got)
		}
	})
}

func TestPiPaneShowsQueuedMessage(t *testing.T) {
	msg := "New instruction: end with PIFLUSH_4D."
	// Exact live layout from the 0.85.1 probe: the banner carries the
	// queued text inline above the native-queue hint.
	live := strings.Join([]string{
		" 14. 1970–1979: Limit States",
		" Steering: " + msg,
		" ↳ Option+Up to edit all queued messages",
		"── ⠏ Working ──",
		"/private/tmp/pi-p0-probe-work",
		"π • (google) gemini-3.8-flash",
	}, "\n")
	if !piPaneShowsQueuedMessage(live, msg) {
		t.Fatal("expected the live busy-steer layout to read as queued")
	}
	if piPaneShowsQueuedMessage(live, "some other message") {
		t.Fatal("a different message must not match the queued text")
	}

	wrapped := strings.Join([]string{
		" Steering:",
		" " + msg,
		" ↳ Option+Up to edit all queued messages",
	}, "\n")
	if !piPaneShowsQueuedMessage(wrapped, msg) {
		t.Fatal("expected a wrapped banner to read as queued via the next-lines fallback")
	}

	if piPaneShowsQueuedMessage("π • (google) gemini-3.8-flash idle\n", msg) {
		t.Fatal("an idle pane must not read as queued")
	}
}

func TestPollPiDurableAck(t *testing.T) {
	msg := "New instruction: reply exactly PI_POLL_ACK_9Z8Y."
	base := time.Now()

	t.Run("row landing mid-poll confirms", func(t *testing.T) {
		path := writePiDurableAckFixture(t, `{"type":"session_start","ts":1}`)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat fixture: %v", err)
		}
		go func() {
			time.Sleep(100 * time.Millisecond)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				return
			}
			defer f.Close()
			_, _ = fmt.Fprintln(f, piUserMarker(time.Now().UnixMilli(), msg))
		}()
		ack, err := pollPiDurableAck(context.Background(), msg, base, info.Size(), 5*time.Second, piDurableAckPoll{
			resolve:  func() string { return path },
			interval: 10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("poll error = %v", err)
		}
		if ack.Outcome != PiDurableAckConfirmed || ack.ProofPath != path || ack.RowTimestamp.IsZero() {
			t.Fatalf("ack = %+v, want confirmed with proof and row time", ack)
		}
	})

	t.Run("occurrence 2 waits past the first marker", func(t *testing.T) {
		path := writePiDurableAckFixture(t,
			`{"type":"session_start","ts":1}`,
			piUserMarker(time.Now().UnixMilli(), msg),
		)
		_, err := pollPiDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, piDurableAckPoll{
			resolve:    func() string { return path },
			interval:   10 * time.Millisecond,
			occurrence: 2,
		})
		if err == nil || !strings.Contains(err.Error(), "not durably acknowledged") {
			t.Fatalf("err = %v, want the second wait to time out on one marker", err)
		}
	})

	t.Run("occurrence 2 confirms on a later second marker", func(t *testing.T) {
		path := writePiDurableAckFixture(t,
			`{"type":"session_start","ts":1}`,
			piUserMarker(time.Now().UnixMilli(), msg),
		)
		secondTS := time.Now().Add(time.Second).UnixMilli()
		go func() {
			time.Sleep(100 * time.Millisecond)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				return
			}
			defer f.Close()
			_, _ = fmt.Fprintln(f, piUserMarker(secondTS, msg))
		}()
		ack, err := pollPiDurableAck(context.Background(), msg, base, 0, 5*time.Second, piDurableAckPoll{
			resolve:    func() string { return path },
			interval:   10 * time.Millisecond,
			occurrence: 2,
		})
		if err != nil {
			t.Fatalf("poll error = %v", err)
		}
		if ack.Outcome != PiDurableAckConfirmed {
			t.Fatalf("ack = %+v, want confirmed", ack)
		}
		if ack.RowTimestamp.UnixMilli() != secondTS {
			t.Fatalf("row timestamp = %v, want the second marker's ts", ack.RowTimestamp)
		}
	})

	t.Run("still queued at expiry is unflushed, not failed", func(t *testing.T) {
		path := writePiDurableAckFixture(t, `{"type":"session_start","ts":1}`)
		pane := " Steering: " + msg + "\n ↳ Option+Up to edit all queued messages\n── ⠏ Working ──\n"
		ack, err := pollPiDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, piDurableAckPoll{
			resolve:     func() string { return path },
			capture:     func(context.Context, string) (string, error) { return pane, nil },
			sessionName: "probe-session",
			interval:    10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("unflushed must not error, got %v", err)
		}
		if ack.Outcome != PiDurableAckUnflushed {
			t.Fatalf("ack = %+v, want accepted_but_unflushed", ack)
		}
	})

	t.Run("taken by an active turn without markers is unflushed", func(t *testing.T) {
		path := writePiDurableAckFixture(t, `{"type":"session_start","ts":1}`)
		pane := "assistant text streaming\n── ⠏ Working ──\nπ • (google) gemini-3.8-flash\n"
		ack, err := pollPiDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, piDurableAckPoll{
			resolve:     func() string { return path },
			capture:     func(context.Context, string) (string, error) { return pane, nil },
			sessionName: "probe-session",
			interval:    10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("unflushed must not error, got %v", err)
		}
		if ack.Outcome != PiDurableAckUnflushed {
			t.Fatalf("ack = %+v, want accepted_but_unflushed", ack)
		}
	})

	t.Run("expiry capture runs under a live context", func(t *testing.T) {
		path := writePiDurableAckFixture(t, `{"type":"session_start","ts":1}`)
		pane := " Steering: " + msg + "\n ↳ Option+Up to edit all queued messages\n── ⠏ Working ──\n"
		ack, err := pollPiDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, piDurableAckPoll{
			resolve: func() string { return path },
			capture: func(cctx context.Context, _ string) (string, error) {
				if err := cctx.Err(); err != nil {
					return "", err
				}
				return pane, nil
			},
			sessionName: "probe-session",
			interval:    10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("unflushed must not error, got %v", err)
		}
		if ack.Outcome != PiDurableAckUnflushed {
			t.Fatalf("ack = %+v, want accepted_but_unflushed", ack)
		}
	})

	t.Run("parent cancel fails fast even when queued", func(t *testing.T) {
		path := writePiDurableAckFixture(t, `{"type":"session_start","ts":1}`)
		pane := " Steering: " + msg + "\n ↳ Option+Up to edit all queued messages\n── ⠏ Working ──\n"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := pollPiDurableAck(ctx, msg, base, 0, 5*time.Second, piDurableAckPoll{
			resolve: func() string { return path },
			capture: func(cctx context.Context, _ string) (string, error) {
				if err := cctx.Err(); err != nil {
					return "", err
				}
				return pane, nil
			},
			sessionName: "probe-session",
			interval:    10 * time.Millisecond,
		})
		if err == nil {
			t.Fatal("expected cancellation to abort the poll, not report unflushed")
		}
	})

	t.Run("draft sitting on an idle pane fails", func(t *testing.T) {
		path := writePiDurableAckFixture(t, `{"type":"session_start","ts":1}`)
		pane := msg + "\nπ • (google) gemini-3.8-flash idle\n"
		_, err := pollPiDurableAck(context.Background(), msg, base, 0, 120*time.Millisecond, piDurableAckPoll{
			resolve:     func() string { return path },
			capture:     func(context.Context, string) (string, error) { return pane, nil },
			sessionName: "probe-session",
			interval:    10 * time.Millisecond,
		})
		if err == nil || !strings.Contains(err.Error(), "not durably acknowledged") {
			t.Fatalf("err = %v, want a durable-ack failure", err)
		}
	})

	t.Run("context cancel aborts", func(t *testing.T) {
		path := writePiDurableAckFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := pollPiDurableAck(ctx, msg, base, 0, 5*time.Second, piDurableAckPoll{
			resolve:  func() string { return path },
			interval: 10 * time.Millisecond,
		})
		if err == nil {
			t.Fatal("expected cancellation to abort the poll")
		}
	})
}

func TestStashAndPeekPiDurableReceipt(t *testing.T) {
	session := &piInteractiveSession{ownerSessionID: "owner-1"}
	since := time.Now()
	stashPiDurableReceipt(session, "  hello world  ", "/tmp/markers.jsonl", 1234, since)
	receipt, ok := peekPiDurableReceipt(session, "hello world")
	if !ok {
		t.Fatal("expected the stashed receipt to match the trimmed message")
	}
	if receipt.offset != 1234 || receipt.path != "/tmp/markers.jsonl" || !receipt.since.Equal(since) {
		t.Fatalf("receipt = %+v, want the stashed snapshot", receipt)
	}
	if _, ok := peekPiDurableReceipt(session, "other"); ok {
		t.Fatal("a different message must not match the receipt")
	}
	stale := &piInteractiveSession{ownerSessionID: "owner-2"}
	stashPiDurableReceipt(stale, "old", "/tmp/m.jsonl", 10, time.Now().Add(-time.Hour))
	if _, ok := peekPiDurableReceipt(stale, "old"); ok {
		t.Fatal("an expired receipt must not match")
	}

	capped := &piInteractiveSession{ownerSessionID: "owner-3"}
	for i := 0; i < piMaxPendingDurableAcks+3; i++ {
		stashPiDurableReceipt(capped, fmt.Sprintf("msg-%d", i), "/tmp/m.jsonl", int64(i), time.Now())
	}
	if _, ok := peekPiDurableReceipt(capped, "msg-0"); ok {
		t.Fatal("receipts past the cap must drop oldest-first")
	}
	if _, ok := peekPiDurableReceipt(capped, fmt.Sprintf("msg-%d", piMaxPendingDurableAcks+2)); !ok {
		t.Fatal("the newest receipt must survive the cap")
	}
}

func TestTakePiDurableReceiptFIFORepeat(t *testing.T) {
	session := &piInteractiveSession{ownerSessionID: "owner-fifo"}
	msg := "yes"
	// First send lands a marker; the identical second send snapshots after it.
	line := piUserMarker(time.Now().UnixMilli(), msg) + "\n"
	path := writePiDurableAckFixture(t, strings.TrimSuffix(line, "\n"))
	secondOffset := int64(len(line))
	stashPiDurableReceipt(session, msg, path, 0, time.Now())
	stashPiDurableReceipt(session, msg, path, secondOffset, time.Now())

	first, ok := takePiDurableReceipt(session, msg)
	if !ok || first.offset != 0 {
		t.Fatalf("first take = (%+v %v), want the first send's baseline", first, ok)
	}
	second, ok := takePiDurableReceipt(session, msg)
	if !ok || second.offset != secondOffset {
		t.Fatalf("second take = (%+v %v), want the second send's baseline", second, ok)
	}
	if _, ok := takePiDurableReceipt(session, msg); ok {
		t.Fatal("no receipt must remain after two takes")
	}
	// The first wait is satisfied by the first marker...
	markers, _, err := readPiMarkersSince(path, first.offset)
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	if _, ok := piUserAckMarker(markers, msg); !ok {
		t.Fatal("expected the first marker to confirm the first wait")
	}
	// ...but the second wait cannot be satisfied by the first marker.
	markers, _, err = readPiMarkersSince(path, second.offset)
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	if _, ok := piUserAckMarker(markers, msg); ok {
		t.Fatal("the first marker must not confirm the repeated send")
	}
}

func TestTakePiDurableReceiptOccurrenceRepeat(t *testing.T) {
	session := &piInteractiveSession{ownerSessionID: "owner-occ"}
	msg := "yes"
	// Both identical sends snapshot before either marker lands: same
	// offset on both receipts, so only occurrence disambiguates them.
	// (The poll waits past the first marker — see TestPollPiDurableAck.)
	path := writePiDurableAckFixture(t, `{"type":"session_start","ts":1}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture: %v", err)
	}
	stashPiDurableReceipt(session, msg, path, info.Size(), time.Now())
	stashPiDurableReceipt(session, msg, path, info.Size(), time.Now())

	first, ok := takePiDurableReceipt(session, msg)
	if !ok || first.occurrence != 1 {
		t.Fatalf("first take = (%+v %v), want occurrence 1", first, ok)
	}
	second, ok := takePiDurableReceipt(session, msg)
	if !ok || second.occurrence != 2 {
		t.Fatalf("second take = (%+v %v), want occurrence 2", second, ok)
	}
	if _, ok := takePiDurableReceipt(session, msg); ok {
		t.Fatal("no receipt must remain after two takes")
	}
}

func TestTakePiDurableReceiptConcurrentOccurrence(t *testing.T) {
	session := &piInteractiveSession{ownerSessionID: "owner-occ-race"}
	msg := "yes"
	stashPiDurableReceipt(session, msg, "/tmp/markers.jsonl", 0, time.Now())
	stashPiDurableReceipt(session, msg, "/tmp/markers.jsonl", 0, time.Now())

	// Watcher goroutines take in acquisition order, not spawn order:
	// however they schedule, the two takes must bind distinct
	// occurrences — never the same receipt twice, never a gap.
	var wg sync.WaitGroup
	got := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			receipt, ok := takePiDurableReceipt(session, msg)
			if !ok {
				t.Errorf("take %d: expected a receipt", idx)
				return
			}
			got[idx] = receipt.occurrence
		}(i)
	}
	wg.Wait()
	if (got[0] != 1 || got[1] != 2) && (got[0] != 2 || got[1] != 1) {
		t.Fatalf("take occurrences = %v, want {1 2} in either order", got)
	}
	if _, ok := takePiDurableReceipt(session, msg); ok {
		t.Fatal("no receipt must remain after two takes")
	}
}

func TestInteractiveSessionRegistered(t *testing.T) {
	owner := "pi-registered-owner"
	if InteractiveSessionRegistered(owner) {
		t.Fatal("an unregistered owner must not be steer-ready")
	}
	piInteractiveRegistry.Lock()
	piInteractiveRegistry.sessions[owner] = &piInteractiveSession{ownerSessionID: owner, tmuxSessionName: "mlp-pi-test"}
	piInteractiveRegistry.Unlock()
	t.Cleanup(func() {
		piInteractiveRegistry.Lock()
		delete(piInteractiveRegistry.sessions, owner)
		piInteractiveRegistry.Unlock()
	})
	if !InteractiveSessionRegistered(owner) {
		t.Fatal("a registered owner must be steer-ready")
	}
}
