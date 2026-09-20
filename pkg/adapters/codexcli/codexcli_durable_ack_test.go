package codexcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeDurableAckFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout-test.jsonl")
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func durableUserRow(ts time.Time, text string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}}`,
		ts.Format(time.RFC3339Nano), text)
}

func durableAssistantRow(ts time.Time, text string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":%q}]}}`,
		ts.Format(time.RFC3339Nano), text)
}

func TestCodexRolloutUserMessageSince(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	msg := "Do not use tools. Reply exactly: DURABLE_ACK_1A2B"

	t.Run("exact match after since", func(t *testing.T) {
		rowAt := base.Add(2 * time.Second)
		path := writeDurableAckFixture(t,
			`{"type":"session_meta","payload":{"id":"thread-1","cwd":"/tmp/work"}}`,
			durableUserRow(rowAt, msg),
			durableAssistantRow(rowAt.Add(time.Second), "DURABLE_ACK_1A2B"),
		)
		at, ok := codexRolloutUserMessageSince(path, msg, base, 0)
		if !ok {
			t.Fatal("expected the user row to confirm the send")
		}
		if !at.Equal(rowAt) {
			t.Fatalf("row timestamp = %v, want %v", at, rowAt)
		}
	})

	t.Run("identical earlier message before offset does not confirm", func(t *testing.T) {
		firstAt := base.Add(-time.Minute)
		first := durableUserRow(firstAt, msg) + "\n"
		path := writeDurableAckFixture(t, strings.TrimSuffix(first, "\n"))
		offset := int64(len(first))
		if _, ok := codexRolloutUserMessageSince(path, msg, base, offset); ok {
			t.Fatal("a row before the pre-paste offset must not confirm a new send")
		}
	})

	t.Run("row older than since does not confirm", func(t *testing.T) {
		path := writeDurableAckFixture(t, durableUserRow(base.Add(-time.Hour), msg))
		if _, ok := codexRolloutUserMessageSince(path, msg, base, 0); ok {
			t.Fatal("a stale row must not confirm a new send")
		}
	})

	t.Run("assistant and environment rows do not match", func(t *testing.T) {
		rowAt := base.Add(time.Second)
		path := writeDurableAckFixture(t,
			durableAssistantRow(rowAt, msg),
			durableUserRow(rowAt, "<environment_context><cwd>/tmp/work</cwd></environment_context>"),
		)
		if _, ok := codexRolloutUserMessageSince(path, msg, base, 0); ok {
			t.Fatal("assistant output and environment context are not user acceptance")
		}
	})

	t.Run("malformed lines are skipped", func(t *testing.T) {
		rowAt := base.Add(time.Second)
		path := writeDurableAckFixture(t,
			`{"type":"response_item","payload":`,
			`not json at all`,
			durableUserRow(rowAt, msg),
		)
		if _, ok := codexRolloutUserMessageSince(path, msg, base, 0); !ok {
			t.Fatal("expected the valid user row to confirm despite malformed lines")
		}
	})

	t.Run("missing file has no proof", func(t *testing.T) {
		if _, ok := codexRolloutUserMessageSince(filepath.Join(t.TempDir(), "absent.jsonl"), msg, base, 0); ok {
			t.Fatal("a missing rollout must not confirm")
		}
	})

	t.Run("long message matches on prefix", func(t *testing.T) {
		long := "LIVE_FOLLOWUP_" + strings.Repeat("0123456789", 30)
		rowAt := base.Add(time.Second)
		path := writeDurableAckFixture(t, durableUserRow(rowAt, long[:200]+"…trailing-normalized"))
		if _, ok := codexRolloutUserMessageSince(path, long, base, 0); !ok {
			t.Fatal("expected a shared 200-char prefix to confirm a long send")
		}
	})
}

func TestCodexDurableAckBudget(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("CODEX_DURABLE_ACK_SECONDS", "")
		if got := codexDurableAckBudget(); got != 180*time.Second {
			t.Fatalf("budget = %v, want 180s", got)
		}
	})
	t.Run("override", func(t *testing.T) {
		t.Setenv("CODEX_DURABLE_ACK_SECONDS", "90")
		if got := codexDurableAckBudget(); got != 90*time.Second {
			t.Fatalf("budget = %v, want 90s", got)
		}
	})
	t.Run("clamped", func(t *testing.T) {
		t.Setenv("CODEX_DURABLE_ACK_SECONDS", "1")
		if got := codexDurableAckBudget(); got != 5*time.Second {
			t.Fatalf("budget = %v, want 5s floor", got)
		}
		t.Setenv("CODEX_DURABLE_ACK_SECONDS", "9999")
		if got := codexDurableAckBudget(); got != 300*time.Second {
			t.Fatalf("budget = %v, want 300s ceiling", got)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		t.Setenv("CODEX_DURABLE_ACK_SECONDS", "soon")
		if got := codexDurableAckBudget(); got != 180*time.Second {
			t.Fatalf("budget = %v, want 180s default", got)
		}
	})
}

func TestCodexPaneShowsQueuedMessage(t *testing.T) {
	msg := "New instruction: end with PINEAPPLEBUS_9Q."
	queued := "• Messages to be submitted after next tool call (press esc to interrupt and send immediately)"
	// Exact live layout from the 0.155.0 probe: banner, queued text,
	// ready footer repainted below.
	live := strings.Join([]string{
		"› Do not use tools. Write a 500-word essay on concrete.",
		"• Planning 500-word essay draft (5s • esc to interrupt)",
		queued,
		"  ↳ " + msg,
		"› Ask Codex to do anything",
		"  gpt-5.6-luna medium · /tmp/work",
	}, "\n")
	if !codexPaneShowsQueuedMessage(live, msg) {
		t.Fatal("expected the live busy-steer layout to read as queued")
	}
	if codexPaneShowsQueuedMessage(live, "some other message") {
		t.Fatal("a different message must not match the queued text")
	}

	flushed := strings.Join([]string{
		queued,
		"  ↳ " + msg,
		"PINEAPPLEBUS_9Q",
		"✻ Worked for 42s",
		"› Ask Codex to do anything",
	}, "\n")
	if codexPaneShowsQueuedMessage(flushed, msg) {
		t.Fatal("a completion below the banner means the queue flushed; it must not read as queued")
	}

	bannerWithoutText := strings.Join([]string{
		queued,
		"› Ask Codex to do anything",
	}, "\n")
	if codexPaneShowsQueuedMessage(bannerWithoutText, msg) {
		t.Fatal("a banner without this message text must not confirm it as queued")
	}

	if codexPaneShowsQueuedMessage("› Ask Codex to do anything\n", msg) {
		t.Fatal("an idle pane must not read as queued")
	}
}

func TestPollCodexDurableAck(t *testing.T) {
	msg := "Do not use tools. Reply exactly: POLL_ACK_9Z8Y"
	base := time.Now().UTC()

	t.Run("row landing mid-poll confirms", func(t *testing.T) {
		path := writeDurableAckFixture(t, `{"type":"session_meta","payload":{"id":"thread-1"}}`)
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
			_, _ = fmt.Fprintln(f, durableUserRow(time.Now().UTC(), msg))
		}()
		ack, err := pollCodexDurableAck(context.Background(), msg, base, info.Size(), 5*time.Second, codexDurableAckPoll{
			resolve:  func() string { return path },
			interval: 10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("poll error = %v", err)
		}
		if ack.Outcome != CodexDurableAckConfirmed || ack.ProofPath != path {
			t.Fatalf("ack = %+v, want confirmed with proof", ack)
		}
	})

	t.Run("still queued at expiry is unflushed, not failed", func(t *testing.T) {
		path := writeDurableAckFixture(t, `{"type":"session_meta","payload":{"id":"thread-1"}}`)
		pane := "• Messages to be submitted after next tool call (press esc to interrupt and send immediately)\n  ↳ " + msg + "\n› Ask Codex to do anything\n"
		ack, err := pollCodexDurableAck(context.Background(), msg, base, 0, 150*time.Millisecond, codexDurableAckPoll{
			resolve:     func() string { return path },
			capture:     func(context.Context, string) (string, error) { return pane, nil },
			sessionName: "probe-session",
			interval:    10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("unflushed must not error, got %v", err)
		}
		if ack.Outcome != CodexDurableAckUnflushed {
			t.Fatalf("ack = %+v, want accepted_but_unflushed", ack)
		}
	})

	t.Run("no row and no queue fails", func(t *testing.T) {
		path := writeDurableAckFixture(t, `{"type":"session_meta","payload":{"id":"thread-1"}}`)
		_, err := pollCodexDurableAck(context.Background(), msg, base, 0, 120*time.Millisecond, codexDurableAckPoll{
			resolve:     func() string { return path },
			capture:     func(context.Context, string) (string, error) { return "› Ask Codex to do anything\n", nil },
			sessionName: "probe-session",
			interval:    10 * time.Millisecond,
		})
		if err == nil || !strings.Contains(err.Error(), "not durably acknowledged") {
			t.Fatalf("err = %v, want a durable-ack failure", err)
		}
	})

	t.Run("context cancel aborts", func(t *testing.T) {
		path := writeDurableAckFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := pollCodexDurableAck(ctx, msg, base, 0, 5*time.Second, codexDurableAckPoll{
			resolve:  func() string { return path },
			interval: 10 * time.Millisecond,
		})
		if err == nil {
			t.Fatal("expected cancellation to abort the poll")
		}
	})
}

func TestStashAndPeekDurableReceipt(t *testing.T) {
	session := &codexInteractiveSession{ownerSessionID: "owner-1"}
	since := time.Now()
	stashCodexDurableReceipt(session, "  hello world  ", "/tmp/rollout.jsonl", 1234, since)
	receipt, ok := peekCodexDurableReceipt(session, "hello world")
	if !ok {
		t.Fatal("expected the stashed receipt to match the trimmed message")
	}
	if receipt.offset != 1234 || receipt.path != "/tmp/rollout.jsonl" || !receipt.since.Equal(since) {
		t.Fatalf("receipt = %+v, want the stashed snapshot", receipt)
	}
	if _, ok := peekCodexDurableReceipt(session, "other"); ok {
		t.Fatal("a different message must not match the receipt")
	}

	stale := &codexInteractiveSession{ownerSessionID: "owner-2"}
	stashCodexDurableReceipt(stale, "old", "/tmp/r.jsonl", 10, time.Now().Add(-time.Hour))
	if _, ok := peekCodexDurableReceipt(stale, "old"); ok {
		t.Fatal("an expired receipt must not match")
	}

	capped := &codexInteractiveSession{ownerSessionID: "owner-3"}
	for i := 0; i < codexMaxPendingDurableAcks+3; i++ {
		stashCodexDurableReceipt(capped, fmt.Sprintf("msg-%d", i), "/tmp/r.jsonl", int64(i), time.Now())
	}
	if _, ok := peekCodexDurableReceipt(capped, "msg-0"); ok {
		t.Fatal("receipts past the cap must drop oldest-first")
	}
	if _, ok := peekCodexDurableReceipt(capped, fmt.Sprintf("msg-%d", codexMaxPendingDurableAcks+2)); !ok {
		t.Fatal("the newest receipt must survive the cap")
	}
}

func TestInteractiveSessionRegistered(t *testing.T) {
	owner := "codex-registered-owner"
	if InteractiveSessionRegistered(owner) {
		t.Fatal("an unregistered owner must not be steer-ready")
	}
	codexInteractiveRegistry.Set(owner, "mlp-codex-test")
	t.Cleanup(func() { codexInteractiveRegistry.Delete(owner) })
	if !InteractiveSessionRegistered(owner) {
		t.Fatal("a registered owner must be steer-ready")
	}
}
