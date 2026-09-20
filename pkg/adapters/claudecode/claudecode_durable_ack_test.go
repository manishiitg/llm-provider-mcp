package claudecode

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

func writeClaudeDurableFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func claudeUserRow(ts time.Time, content string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"sessionId":"sess-1","message":{"role":"user","content":%s}}`, ts.Format(time.RFC3339Nano), quoteJSON(content))
}

func claudeEnqueueRow(ts time.Time, content string) string {
	return fmt.Sprintf(`{"type":"queue-operation","operation":"enqueue","timestamp":%q,"sessionId":"sess-1","content":%s}`, ts.Format(time.RFC3339Nano), quoteJSON(content))
}

func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestClaudeDurableAckBudget(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("CLAUDE_DURABLE_ACK_SECONDS", "")
		if got := claudeDurableAckBudget(); got != 60*time.Second {
			t.Fatalf("budget = %v, want 60s", got)
		}
	})

	t.Run("override", func(t *testing.T) {
		t.Setenv("CLAUDE_DURABLE_ACK_SECONDS", "90")
		if got := claudeDurableAckBudget(); got != 90*time.Second {
			t.Fatalf("budget = %v, want 90s", got)
		}
	})

	t.Run("clamped", func(t *testing.T) {
		t.Setenv("CLAUDE_DURABLE_ACK_SECONDS", "1")
		if got := claudeDurableAckBudget(); got != 5*time.Second {
			t.Fatalf("budget = %v, want 5s floor", got)
		}
		t.Setenv("CLAUDE_DURABLE_ACK_SECONDS", "9999")
		if got := claudeDurableAckBudget(); got != 300*time.Second {
			t.Fatalf("budget = %v, want 300s ceiling", got)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Setenv("CLAUDE_DURABLE_ACK_SECONDS", "soon")
		if got := claudeDurableAckBudget(); got != 60*time.Second {
			t.Fatalf("budget = %v, want 60s default", got)
		}
	})
}

func TestClaudeTranscriptUserMessageSince(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	msg := "STEER: end with PINEAPPLEBUS_9Q."

	t.Run("exact match", func(t *testing.T) {
		rowAt := base.Add(time.Second)
		path := writeClaudeDurableFixture(t, claudeUserRow(rowAt, msg))
		at, ok := claudeTranscriptUserMessageSince(path, msg, base, 0)
		if !ok {
			t.Fatal("expected the user row to confirm")
		}
		if !at.Equal(rowAt) {
			t.Fatalf("row timestamp = %v, want %v", at, rowAt)
		}
	})

	t.Run("pasted content envelope matches", func(t *testing.T) {
		// Live shape: pasted sends are wrapped in <pasted_content> tags.
		rowAt := base.Add(time.Second)
		wrapped := "<pasted_content id=\"ad42\">\n" + msg + "\n</pasted_content id=\"ad42\">"
		path := writeClaudeDurableFixture(t, claudeUserRow(rowAt, wrapped))
		if _, ok := claudeTranscriptUserMessageSince(path, msg, base, 0); !ok {
			t.Fatal("expected the wrapped user row to confirm the raw message")
		}
	})

	t.Run("tool result blocks do not match", func(t *testing.T) {
		row := fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":%s}]}}`, base.Add(time.Second).Format(time.RFC3339Nano), quoteJSON(msg))
		path := writeClaudeDurableFixture(t, row)
		if _, ok := claudeTranscriptUserMessageSince(path, msg, base, 0); ok {
			t.Fatal("a tool_result block must not confirm typed user text")
		}
	})

	t.Run("assistant rows do not match", func(t *testing.T) {
		row := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":%s}}`, base.Add(time.Second).Format(time.RFC3339Nano), quoteJSON(msg))
		path := writeClaudeDurableFixture(t, row)
		if _, ok := claudeTranscriptUserMessageSince(path, msg, base, 0); ok {
			t.Fatal("an assistant echo must not confirm the send")
		}
	})

	t.Run("offset scopes out earlier identical rows", func(t *testing.T) {
		old := claudeUserRow(base.Add(-time.Hour), msg)
		path := writeClaudeDurableFixture(t, old)
		offset := int64(len(old) + 1)
		if _, ok := claudeTranscriptUserMessageSince(path, msg, base.Add(-2*time.Hour), offset); ok {
			t.Fatal("a row before the send offset must not confirm a new send")
		}
	})

	t.Run("since scopes out historical rows", func(t *testing.T) {
		path := writeClaudeDurableFixture(t, claudeUserRow(base.Add(-time.Hour), msg))
		if _, ok := claudeTranscriptUserMessageSince(path, msg, base, 0); ok {
			t.Fatal("a row older than since must not confirm")
		}
	})

	t.Run("missing file has no proof", func(t *testing.T) {
		if _, ok := claudeTranscriptUserMessageSince(filepath.Join(t.TempDir(), "absent.jsonl"), msg, base, 0); ok {
			t.Fatal("a missing transcript must not confirm")
		}
	})

	t.Run("malformed lines are skipped", func(t *testing.T) {
		rowAt := base.Add(time.Second)
		path := writeClaudeDurableFixture(t, "{not json", claudeUserRow(rowAt, msg))
		if _, ok := claudeTranscriptUserMessageSince(path, msg, base, 0); !ok {
			t.Fatal("expected the good row past malformed lines to confirm")
		}
	})

	t.Run("long message matches on prefix", func(t *testing.T) {
		long := "LIVE_FOLLOWUP_" + strings.Repeat("0123456789", 30)
		rowAt := base.Add(time.Second)
		path := writeClaudeDurableFixture(t, claudeUserRow(rowAt, long[:200]+"…trailing-normalized"))
		if _, ok := claudeTranscriptUserMessageSince(path, long, base, 0); !ok {
			t.Fatal("expected a shared 200-char prefix to confirm a long send")
		}
	})
}

func TestClaudeTranscriptEnqueueSince(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	msg := "STEER: end with PINEAPPLEBUS_9Q."

	t.Run("enqueue matches wrapped content", func(t *testing.T) {
		rowAt := base.Add(500 * time.Millisecond)
		wrapped := "<pasted_content id=\"ad42\">\n" + msg + "\n</pasted_content id=\"ad42\">"
		path := writeClaudeDurableFixture(t, claudeEnqueueRow(rowAt, wrapped))
		at, ok := claudeTranscriptEnqueueSince(path, msg, base, 0)
		if !ok {
			t.Fatal("expected the enqueue row to prove acceptance")
		}
		if !at.Equal(rowAt) {
			t.Fatalf("row timestamp = %v, want %v", at, rowAt)
		}
	})

	t.Run("dequeue without content never matches", func(t *testing.T) {
		row := fmt.Sprintf(`{"type":"queue-operation","operation":"dequeue","timestamp":%q,"sessionId":"sess-1"}`, base.Add(time.Second).Format(time.RFC3339Nano))
		path := writeClaudeDurableFixture(t, row)
		if _, ok := claudeTranscriptEnqueueSince(path, msg, base, 0); ok {
			t.Fatal("a contentless dequeue must not prove acceptance")
		}
	})

	t.Run("user row is not enqueue proof", func(t *testing.T) {
		path := writeClaudeDurableFixture(t, claudeUserRow(base.Add(time.Second), msg))
		if _, ok := claudeTranscriptEnqueueSince(path, msg, base, 0); ok {
			t.Fatal("a user row must not count as enqueue proof")
		}
	})
}

func claudeDrainRow(op string, ts time.Time, content string) string {
	if content == "" {
		return fmt.Sprintf(`{"type":"queue-operation","operation":%q,"timestamp":%q,"sessionId":"sess-1"}`, op, ts.Format(time.RFC3339Nano))
	}
	return fmt.Sprintf(`{"type":"queue-operation","operation":%q,"timestamp":%q,"sessionId":"sess-1","content":%s}`, op, ts.Format(time.RFC3339Nano), quoteJSON(content))
}

func TestClaudeTranscriptDrainSince(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	msg := "STEER: end with PINEAPPLEBUS_9Q."
	wrapped := "<pasted_content id=\"ad42\">\n" + msg + "\n</pasted_content id=\"ad42\">"

	t.Run("remove with our text drains", func(t *testing.T) {
		// Live tool-time shape: enqueue, then remove with full text.
		drainAt := base.Add(8 * time.Second)
		path := writeClaudeDurableFixture(t,
			claudeEnqueueRow(base.Add(time.Second), wrapped),
			claudeDrainRow("remove", drainAt, wrapped),
		)
		at, ok := claudeTranscriptDrainSince(path, msg, base, 0)
		if !ok {
			t.Fatal("expected the remove row to prove the drain")
		}
		if !at.Equal(drainAt) {
			t.Fatalf("drain timestamp = %v, want %v", at, drainAt)
		}
	})

	t.Run("remove with other text does not drain", func(t *testing.T) {
		path := writeClaudeDurableFixture(t,
			claudeEnqueueRow(base.Add(time.Second), wrapped),
			claudeDrainRow("remove", base.Add(8*time.Second), "something else"),
		)
		if _, ok := claudeTranscriptDrainSince(path, msg, base, 0); ok {
			t.Fatal("a remove of another message must not prove our drain")
		}
	})

	t.Run("dequeue after our enqueue drains", func(t *testing.T) {
		// Dequeue rows are contentless: the match is positional.
		drainAt := base.Add(7 * time.Second)
		path := writeClaudeDurableFixture(t,
			claudeEnqueueRow(base.Add(time.Second), wrapped),
			claudeDrainRow("dequeue", drainAt, ""),
		)
		if _, ok := claudeTranscriptDrainSince(path, msg, base, 0); !ok {
			t.Fatal("expected a dequeue after our enqueue to prove the drain")
		}
	})

	t.Run("dequeue without our enqueue does not drain", func(t *testing.T) {
		path := writeClaudeDurableFixture(t,
			claudeDrainRow("dequeue", base.Add(time.Second), ""),
		)
		if _, ok := claudeTranscriptDrainSince(path, msg, base, 0); ok {
			t.Fatal("a dequeue with no matching enqueue must not prove our drain")
		}
	})

	t.Run("enqueue alone is not a drain", func(t *testing.T) {
		path := writeClaudeDurableFixture(t,
			claudeEnqueueRow(base.Add(time.Second), wrapped),
		)
		if _, ok := claudeTranscriptDrainSince(path, msg, base, 0); ok {
			t.Fatal("an undrained enqueue must not prove the drain")
		}
	})
}

func TestClaudeTranscriptDrainOccurrence(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	msg := "STEER: end with PINEAPPLEBUS_9Q."
	wrapped := "<pasted_content id=\"ad42\">\n" + msg + "\n</pasted_content id=\"ad42\">"

	t.Run("dequeue shape needs one drain per send", func(t *testing.T) {
		oneDrain := writeClaudeDurableFixture(t,
			claudeEnqueueRow(base.Add(time.Second), wrapped),
			claudeEnqueueRow(base.Add(2*time.Second), wrapped),
			claudeDrainRow("dequeue", base.Add(7*time.Second), ""),
		)
		if _, ok := claudeTranscriptDrainOccurrenceSince(oneDrain, msg, base, 0, 1); !ok {
			t.Fatal("expected the first drain to confirm the first wait")
		}
		if _, ok := claudeTranscriptDrainOccurrenceSince(oneDrain, msg, base, 0, 2); ok {
			t.Fatal("one drain must not confirm two waits")
		}
		twoDrains := writeClaudeDurableFixture(t,
			claudeEnqueueRow(base.Add(time.Second), wrapped),
			claudeEnqueueRow(base.Add(2*time.Second), wrapped),
			claudeDrainRow("dequeue", base.Add(7*time.Second), ""),
			claudeDrainRow("dequeue", base.Add(8*time.Second), ""),
		)
		if _, ok := claudeTranscriptDrainOccurrenceSince(twoDrains, msg, base, 0, 2); !ok {
			t.Fatal("expected the second drain to confirm the second wait")
		}
	})

	t.Run("remove shape needs one drain per send", func(t *testing.T) {
		oneDrain := writeClaudeDurableFixture(t,
			claudeEnqueueRow(base.Add(time.Second), wrapped),
			claudeEnqueueRow(base.Add(2*time.Second), wrapped),
			claudeDrainRow("remove", base.Add(8*time.Second), wrapped),
		)
		if _, ok := claudeTranscriptDrainOccurrenceSince(oneDrain, msg, base, 0, 1); !ok {
			t.Fatal("expected the first drain to confirm the first wait")
		}
		if _, ok := claudeTranscriptDrainOccurrenceSince(oneDrain, msg, base, 0, 2); ok {
			t.Fatal("one drain must not confirm two waits")
		}
	})
}

func TestPollClaudeDurableAck(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	msg := "STEER: end with PINEAPPLEBUS_9Q."
	wrapped := "<pasted_content id=\"ad42\">\n" + msg + "\n</pasted_content id=\"ad42\">"

	t.Run("user row confirms", func(t *testing.T) {
		path := writeClaudeDurableFixture(t, claudeUserRow(base.Add(time.Second), msg))
		ack, err := pollClaudeDurableAck(context.Background(), msg, base, 0, 5*time.Second, claudeDurableAckPoll{
			resolve:  func() string { return path },
			interval: 10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("poll error = %v", err)
		}
		if ack.Outcome != ClaudeDurableAckConfirmed || ack.ProofPath != path {
			t.Fatalf("ack = %+v, want confirmed with proof", ack)
		}
	})

	t.Run("occurrence 2 waits past the first user row", func(t *testing.T) {
		path := writeClaudeDurableFixture(t, claudeUserRow(base.Add(time.Second), msg))
		_, err := pollClaudeDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, claudeDurableAckPoll{
			resolve:    func() string { return path },
			interval:   10 * time.Millisecond,
			occurrence: 2,
		})
		if err == nil || !strings.Contains(err.Error(), "not durably acknowledged") {
			t.Fatalf("err = %v, want the second wait to time out on one row", err)
		}
	})

	t.Run("occurrence 2 with one enqueue fails, not unflushed", func(t *testing.T) {
		path := writeClaudeDurableFixture(t, claudeEnqueueRow(base.Add(time.Second), wrapped))
		_, err := pollClaudeDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, claudeDurableAckPoll{
			resolve:    func() string { return path },
			interval:   10 * time.Millisecond,
			occurrence: 2,
		})
		if err == nil || !strings.Contains(err.Error(), "not durably acknowledged") {
			t.Fatalf("err = %v, want failed: one enqueue cannot hold two sends", err)
		}
	})

	t.Run("occurrence 1 with one enqueue is unflushed", func(t *testing.T) {
		path := writeClaudeDurableFixture(t, claudeEnqueueRow(base.Add(time.Second), wrapped))
		ack, err := pollClaudeDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, claudeDurableAckPoll{
			resolve:    func() string { return path },
			interval:   10 * time.Millisecond,
			occurrence: 1,
		})
		if err != nil {
			t.Fatalf("unflushed must not error, got %v", err)
		}
		if ack.Outcome != ClaudeDurableAckUnflushed {
			t.Fatalf("ack = %+v, want accepted_but_unflushed", ack)
		}
	})

	t.Run("context cancel aborts", func(t *testing.T) {
		path := writeClaudeDurableFixture(t, claudeUserRow(base.Add(-time.Hour), "older"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := pollClaudeDurableAck(ctx, msg, base, 0, 5*time.Second, claudeDurableAckPoll{
			resolve:  func() string { return path },
			interval: 10 * time.Millisecond,
		})
		if err == nil {
			t.Fatal("expected cancellation to abort the poll")
		}
	})
}

func TestStashAndPeekClaudeDurableReceipt(t *testing.T) {
	session := &claudeInteractivePersistentSession{ownerSessionID: "owner-1"}
	since := time.Now()
	stashClaudeDurableReceipt(session, "  hello world  ", "/tmp/session.jsonl", 4242, since)
	receipt, ok := peekClaudeDurableReceipt(session, "hello world")
	if !ok {
		t.Fatal("expected the stashed receipt to match the trimmed message")
	}
	if receipt.offset != 4242 || receipt.path != "/tmp/session.jsonl" || !receipt.since.Equal(since) {
		t.Fatalf("receipt = %+v, want the stashed snapshot", receipt)
	}
	if _, ok := peekClaudeDurableReceipt(session, "other"); ok {
		t.Fatal("a different message must not match the receipt")
	}

	t.Run("nil session is safe", func(t *testing.T) {
		stashClaudeDurableReceipt(nil, "x", "/tmp/s.jsonl", 0, time.Now())
		if _, ok := peekClaudeDurableReceipt(nil, "x"); ok {
			t.Fatal("nil session must not match")
		}
	})

	capped := &claudeInteractivePersistentSession{ownerSessionID: "owner-3"}
	for i := 0; i < claudeMaxPendingDurableAcks+3; i++ {
		stashClaudeDurableReceipt(capped, fmt.Sprintf("msg-%d", i), "/tmp/s.jsonl", int64(i), time.Now())
	}
	if _, ok := peekClaudeDurableReceipt(capped, "msg-0"); ok {
		t.Fatal("receipts past the cap must drop oldest-first")
	}
	if _, ok := peekClaudeDurableReceipt(capped, fmt.Sprintf("msg-%d", claudeMaxPendingDurableAcks+2)); !ok {
		t.Fatal("the newest receipt must survive the cap")
	}
}

func TestTakeClaudeDurableReceiptFIFORepeat(t *testing.T) {
	session := &claudeInteractivePersistentSession{ownerSessionID: "owner-fifo"}
	base := time.Now().UTC().Truncate(time.Second)
	msg := "yes"
	// First send lands a row; the identical second send snapshots after it.
	row := claudeUserRow(base.Add(time.Second), msg) + "\n"
	path := writeClaudeDurableFixture(t, strings.TrimSuffix(row, "\n"))
	secondOffset := int64(len(row))
	stashClaudeDurableReceipt(session, msg, path, 0, base)
	// Same window start: the second wait's exclusion must come from its
	// offset alone, proving FIFO baselines disambiguate repeats.
	stashClaudeDurableReceipt(session, msg, path, secondOffset, base)

	first, ok := takeClaudeDurableReceipt(session, msg)
	if !ok || first.offset != 0 {
		t.Fatalf("first take = (%+v %v), want the first send's baseline", first, ok)
	}
	second, ok := takeClaudeDurableReceipt(session, msg)
	if !ok || second.offset != secondOffset {
		t.Fatalf("second take = (%+v %v), want the second send's baseline", second, ok)
	}
	if _, ok := takeClaudeDurableReceipt(session, msg); ok {
		t.Fatal("no receipt must remain after two takes")
	}
	// The first wait is satisfied by the first row...
	if _, ok := claudeTranscriptUserMessageSince(path, msg, first.since, first.offset); !ok {
		t.Fatal("expected the first row to confirm the first wait")
	}
	// ...but the second wait cannot be satisfied by the first row.
	if _, ok := claudeTranscriptUserMessageSince(path, msg, second.since, second.offset); ok {
		t.Fatal("the first row must not confirm the repeated send")
	}
}

func TestTakeClaudeDurableReceiptOccurrenceRepeat(t *testing.T) {
	session := &claudeInteractivePersistentSession{ownerSessionID: "owner-occ"}
	base := time.Now().UTC().Truncate(time.Second)
	msg := "yes"
	// Both identical sends snapshot before either row lands: same
	// offset on both receipts, so only occurrence disambiguates them.
	stashClaudeDurableReceipt(session, msg, "/tmp/session.jsonl", 0, base)
	stashClaudeDurableReceipt(session, msg, "/tmp/session.jsonl", 0, base)

	first, ok := takeClaudeDurableReceipt(session, msg)
	if !ok || first.occurrence != 1 {
		t.Fatalf("first take = (%+v %v), want occurrence 1", first, ok)
	}
	second, ok := takeClaudeDurableReceipt(session, msg)
	if !ok || second.occurrence != 2 {
		t.Fatalf("second take = (%+v %v), want occurrence 2", second, ok)
	}

	path := writeClaudeDurableFixture(t, claudeUserRow(base.Add(time.Second), msg))
	// The first wait is satisfied by the first row...
	if _, ok := claudeTranscriptUserMessageOccurrenceSince(path, msg, first.since, first.offset, first.occurrence); !ok {
		t.Fatal("expected the first row to confirm the first wait")
	}
	// ...but the second wait needs a second row.
	if _, ok := claudeTranscriptUserMessageOccurrenceSince(path, msg, second.since, second.offset, second.occurrence); ok {
		t.Fatal("the first row must not confirm the repeated send")
	}
}

func TestTakeClaudeDurableReceiptConcurrentOccurrence(t *testing.T) {
	session := &claudeInteractivePersistentSession{ownerSessionID: "owner-occ-race"}
	msg := "yes"
	stashClaudeDurableReceipt(session, msg, "/tmp/session.jsonl", 0, time.Now())
	stashClaudeDurableReceipt(session, msg, "/tmp/session.jsonl", 0, time.Now())

	// Watcher goroutines take in acquisition order, not spawn order:
	// however they schedule, the two takes must bind distinct
	// occurrences — never the same receipt twice, never a gap.
	var wg sync.WaitGroup
	got := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			receipt, ok := takeClaudeDurableReceipt(session, msg)
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
	if _, ok := takeClaudeDurableReceipt(session, msg); ok {
		t.Fatal("no receipt must remain after two takes")
	}
}

func TestClaudeInteractiveSessionRegistered(t *testing.T) {
	owner := "claude-registered-owner"
	if InteractiveSessionRegistered(owner) {
		t.Fatal("an unregistered owner must not be steer-ready")
	}
	claudeInteractiveOwnerRegistry.Set(owner, "mlp-claude-test")
	t.Cleanup(func() { claudeInteractiveOwnerRegistry.Delete(owner) })
	if !InteractiveSessionRegistered(owner) {
		t.Fatal("a registered owner must be steer-ready")
	}
}
