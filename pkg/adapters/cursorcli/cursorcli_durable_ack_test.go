package cursorcli

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// writeCursorDurableStore builds a minimal store.db with the real schema
// (meta.latestRootBlobId + protobuf root + JSON message blobs) holding
// the given user_query texts in order. It returns the db path and the
// hex ref of each query blob.
func writeCursorDurableStore(t *testing.T, userQueries ...string) (string, []string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)`); err != nil {
		t.Fatalf("create blobs: %v", err)
	}
	ids := make([][]byte, 0, len(userQueries))
	refs := make([]string, 0, len(userQueries))
	bodies := make([]string, 0, len(userQueries))
	for i, q := range userQueries {
		id := synthBlobID(byte(0x10 + i))
		ids = append(ids, id)
		refs = append(refs, hex.EncodeToString(id))
		body, _ := json.Marshal(map[string]any{
			"role": "user",
			"content": []map[string]string{
				{"type": "text", "text": "<user_query>\n" + q + "\n</user_query>"},
			},
		})
		bodies = append(bodies, string(body))
	}
	rootID := synthBlobID(0xff)
	// Root lists message refs; the root blob itself is stored under rootID.
	fullRoot := buildCursorRootBlob(t, ids)
	metaJSON, _ := json.Marshal(map[string]any{"latestRootBlobId": hex.EncodeToString(rootID)})
	if _, err := db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('0', ?)`, hex.EncodeToString(metaJSON)); err != nil {
		t.Fatalf("insert meta: %v", err)
	}
	insertBlob := func(id []byte, body []byte) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO blobs(id,data) VALUES(?, ?)`, hex.EncodeToString(id), body); err != nil {
			t.Fatalf("insert blob: %v", err)
		}
	}
	insertBlob(rootID, fullRoot)
	for i := range ids {
		insertBlob(ids[i], []byte(bodies[i]))
	}
	return dbPath, refs
}

func TestCursorDurableAckBudget(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("CURSOR_DURABLE_ACK_SECONDS", "")
		if got := cursorDurableAckBudget(); got != 180*time.Second {
			t.Fatalf("budget = %v, want 180s", got)
		}
	})

	t.Run("override", func(t *testing.T) {
		t.Setenv("CURSOR_DURABLE_ACK_SECONDS", "90")
		if got := cursorDurableAckBudget(); got != 90*time.Second {
			t.Fatalf("budget = %v, want 90s", got)
		}
	})

	t.Run("clamped", func(t *testing.T) {
		t.Setenv("CURSOR_DURABLE_ACK_SECONDS", "1")
		if got := cursorDurableAckBudget(); got != 5*time.Second {
			t.Fatalf("budget = %v, want 5s floor", got)
		}
		t.Setenv("CURSOR_DURABLE_ACK_SECONDS", "9999")
		if got := cursorDurableAckBudget(); got != 300*time.Second {
			t.Fatalf("budget = %v, want 300s ceiling", got)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Setenv("CURSOR_DURABLE_ACK_SECONDS", "soon")
		if got := cursorDurableAckBudget(); got != 180*time.Second {
			t.Fatalf("budget = %v, want 180s default", got)
		}
	})
}

func TestCursorStoreUserQuerySince(t *testing.T) {
	msg := "STEER: end with PINEAPPLEBUS_9Q."

	t.Run("new row matches", func(t *testing.T) {
		path, _ := writeCursorDurableStore(t, "old turn", msg)
		if !cursorStoreUserQuerySince(path, msg, nil) {
			t.Fatal("expected the user_query row to confirm")
		}
	})

	t.Run("baseline excludes identical earlier row", func(t *testing.T) {
		path, refs := writeCursorDurableStore(t, msg)
		baseline := map[string]struct{}{refs[0]: {}}
		if cursorStoreUserQuerySince(path, msg, baseline) {
			t.Fatal("a row already in the baseline must not confirm a new send")
		}
	})

	t.Run("repeat under new ref matches", func(t *testing.T) {
		path, refs := writeCursorDurableStore(t, msg, msg)
		baseline := map[string]struct{}{refs[0]: {}}
		if !cursorStoreUserQuerySince(path, msg, baseline) {
			t.Fatal("expected the repeat under a new ref to confirm")
		}
	})

	t.Run("other text does not match", func(t *testing.T) {
		path, _ := writeCursorDurableStore(t, "something else")
		if cursorStoreUserQuerySince(path, msg, nil) {
			t.Fatal("a different query must not confirm")
		}
	})

	t.Run("missing store has no proof", func(t *testing.T) {
		if cursorStoreUserQuerySince(filepath.Join(t.TempDir(), "absent.db"), msg, nil) {
			t.Fatal("a missing store must not confirm")
		}
	})

	t.Run("long message matches on prefix", func(t *testing.T) {
		long := "LIVE_FOLLOWUP_" + strings.Repeat("0123456789", 30)
		path, _ := writeCursorDurableStore(t, long[:200]+"…trailing-normalized")
		if !cursorStoreUserQuerySince(path, long, nil) {
			t.Fatal("expected a shared 200-char prefix to confirm a long send")
		}
	})
}

func TestCursorPaneShowsQueuedMessage(t *testing.T) {
	msg := "STEER: end with PINEAPPLEBUS_9Q."
	// Shape per hasCursorQueuedFollowupsSendPrompt: follow-ups header +
	// send/steer footer. LIVE-UNVERIFIED: confirm against a real busy
	// pane once quota resets.
	queued := strings.Join([]string{
		"Follow-ups (1)",
		"STEER: end with PINEAPPLEBUS_9Q.",
		"enter send now · select/edit",
	}, "\n")
	if !cursorPaneShowsQueuedMessage(queued, msg) {
		t.Fatal("expected banner + message text to read as queued")
	}

	bannerOnly := strings.Join([]string{
		"Follow-ups (1)",
		"some other draft",
		"enter send now · select/edit",
	}, "\n")
	if cursorPaneShowsQueuedMessage(bannerOnly, msg) {
		t.Fatal("banner without our text must not read as queued")
	}

	if cursorPaneShowsQueuedMessage("› ready\n", msg) {
		t.Fatal("idle composer must not read as queued")
	}
}

func TestStashAndPeekCursorDurableReceipt(t *testing.T) {
	session := &cursorInteractiveSession{ownerSessionID: "owner-1"}
	since := time.Now()
	baseline := map[string]struct{}{"ref-a": {}}
	stashCursorDurableReceipt(session, "  hello world  ", "/tmp/store.db", baseline, since)
	receipt, ok := peekCursorDurableReceipt(session, "hello world")
	if !ok {
		t.Fatal("expected the stashed receipt to match the trimmed message")
	}
	if receipt.storeDB != "/tmp/store.db" || !receipt.since.Equal(since) {
		t.Fatalf("receipt = %+v, want the stashed snapshot", receipt)
	}
	if _, ok := receipt.baseline["ref-a"]; !ok {
		t.Fatal("receipt must carry the baseline refs")
	}
	if _, ok := peekCursorDurableReceipt(session, "other"); ok {
		t.Fatal("a different message must not match the receipt")
	}

	t.Run("nil session is safe", func(t *testing.T) {
		stashCursorDurableReceipt(nil, "x", "/tmp/s.db", nil, time.Now())
		if _, ok := peekCursorDurableReceipt(nil, "x"); ok {
			t.Fatal("nil session must not match")
		}
	})

	capped := &cursorInteractiveSession{ownerSessionID: "owner-3"}
	for i := 0; i < cursorMaxPendingDurableAcks+3; i++ {
		stashCursorDurableReceipt(capped, fmt.Sprintf("msg-%d", i), "/tmp/s.db", nil, time.Now())
	}
	if _, ok := peekCursorDurableReceipt(capped, "msg-0"); ok {
		t.Fatal("receipts past the cap must drop oldest-first")
	}
	if _, ok := peekCursorDurableReceipt(capped, fmt.Sprintf("msg-%d", cursorMaxPendingDurableAcks+2)); !ok {
		t.Fatal("the newest receipt must survive the cap")
	}
}

func TestTakeCursorDurableReceiptFIFORepeat(t *testing.T) {
	session := &cursorInteractiveSession{ownerSessionID: "owner-fifo"}
	msg := "yes"
	// First send lands a row; the identical second send snapshots after it.
	path, refs := writeCursorDurableStore(t, msg)
	stashCursorDurableReceipt(session, msg, path, map[string]struct{}{}, time.Now())
	stashCursorDurableReceipt(session, msg, path, map[string]struct{}{refs[0]: {}}, time.Now())

	first, ok := takeCursorDurableReceipt(session, msg)
	if !ok || len(first.baseline) != 0 {
		t.Fatalf("first take = (%+v %v), want the first send's baseline", first, ok)
	}
	second, ok := takeCursorDurableReceipt(session, msg)
	if !ok {
		t.Fatalf("second take = (%+v %v), want the second send's baseline", second, ok)
	}
	if _, ok := second.baseline[refs[0]]; !ok {
		t.Fatalf("second take baseline = %+v, want the first row's ref", second.baseline)
	}
	if _, ok := takeCursorDurableReceipt(session, msg); ok {
		t.Fatal("no receipt must remain after two takes")
	}
	// The first wait is satisfied by the first row...
	if !cursorStoreUserQuerySince(path, msg, first.baseline) {
		t.Fatal("expected the first row to confirm the first wait")
	}
	// ...but the second wait cannot be satisfied by the first row.
	if cursorStoreUserQuerySince(path, msg, second.baseline) {
		t.Fatal("the first row must not confirm the repeated send")
	}
}

func TestCursorInteractiveSessionRegistered(t *testing.T) {
	owner := "cursor-registered-owner"
	if InteractiveSessionRegistered(owner) {
		t.Fatal("an unregistered owner must not be steer-ready")
	}
	cursorInteractiveRegistry.Set(owner, "mlp-cursor-test")
	t.Cleanup(func() { cursorInteractiveRegistry.Delete(owner) })
	if !InteractiveSessionRegistered(owner) {
		t.Fatal("a registered owner must be steer-ready")
	}
}
