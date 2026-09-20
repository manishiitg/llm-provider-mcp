package cursorcli

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"

	_ "modernc.org/sqlite"
)

// TestCursorTranscriptStreamNoHistoryReplayLive is the live counterpart of
// TestCursorTranscriptStreamDoesNotReplayHistoryOnFreshProcess: a real
// cursor-agent turn commits real history to a real store.db, then a freshly
// constructed reader (a new owner key, so an empty dedup map — exactly the
// state a restarted process begins in) must not re-emit it. The deterministic
// test proves the priming mechanism against a synthetic store; this proves the
// real CLI writes its transcript in the shape the mechanism assumes — the
// coverage gap through which the original 22-line replay bug shipped.
//
// Gated behind -coding-cli-p0-live; requires a real cursor-agent CLI, node, tmux.
func TestCursorTranscriptStreamNoHistoryReplayLive(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	workDir := t.TempDir()
	owner := "cursor-noreplay-live-" + cursorRandomHex(4)
	marker := "NOREPLAY_" + strings.ToUpper(cursorRandomHex(4))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Task "+marker+": what is 2+2? Reply with one line: the answer, a space, then the task ID.")},
		WithInteractiveSessionID(owner),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithForce(),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	final := ""
	if len(resp.Choices) == 1 {
		final = strings.TrimSpace(resp.Choices[0].Content)
	}
	if !strings.Contains(final, marker) {
		t.Fatalf("turn did not produce the marker; final=%q", final)
	}

	// Cursor commits store.db asynchronously: wait for the chats tree under
	// this working dir to go quiescent so the fresh reader starts from a
	// stable transcript rather than racing a late commit.
	stores := waitForCursorStoresQuiescent(t, workDir, 90*time.Second)

	// History + shape proof: the marker bytes must be present in a real
	// store. Without this, absence below could mean "nothing to replay".
	var markerStores []string
	for _, store := range stores {
		if cursorStoreBlobsContain(t, store, marker) {
			markerStores = append(markerStores, store)
		}
	}
	if len(markerStores) == 0 {
		t.Fatalf("marker %q not found in any store.db under %s; cannot prove replay suppression", marker, workDir)
	}
	// Touch ONLY the marker-positive stores: the fresh reader selects the
	// freshest post-baseline store, and warmup stores must stay skipped so
	// the assertion below runs against real history. Mtime is not content;
	// priming keys on content-addressed blob IDs either way.
	now := time.Now()
	for _, store := range markerStores {
		if err := os.Chtimes(store, now, now); err != nil {
			t.Fatalf("Chtimes %s: %v", store, err)
		}
	}

	// Restart simulation: a fresh owner key means an empty dedup map, and
	// construction primes every on-disk blob as already-seen history.
	freshOwner := owner + "-restart"
	resetCursorReturnedBlobs(cursorTranscriptStreamKey(freshOwner))
	state := newCursorTranscriptStreamState(time.Now(), workDir, freshOwner, "")
	if path := freshestCursorStoreDBSince(workDir, state.baseline, ""); path == "" {
		t.Fatal("fresh reader resolved no store; the absence assertion would be vacuous")
	}
	for i := 0; i < 3; i++ {
		for _, chunk := range collectCursorStreamChunks(t, state) {
			if chunk.Type == llmtypes.StreamChunkTypeContent && strings.Contains(chunk.Content, marker) {
				t.Fatalf("history replayed on a fresh reader: %q", marker)
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("fresh reader emitted no history across 3 polls; marker %q verified in %d store(s)", marker, len(markerStores))
}

// waitForCursorStoresQuiescent waits until the chats tree for workingDir
// stops changing (sizes + mtimes stable across 5s) and returns every
// store.db beneath it. Fails if no store appears or the tree never settles.
func waitForCursorStoresQuiescent(t *testing.T, workDir string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastSig string
	var stableSince time.Time
	for {
		stores := allCursorStoreDBs(workDir, "")
		sig := cursorChatsTreeSig(stores)
		if len(stores) > 0 && sig == lastSig {
			if !stableSince.IsZero() && time.Since(stableSince) >= 5*time.Second {
				return stores
			}
		} else {
			lastSig = sig
			stableSince = time.Now()
		}
		if time.Now().After(deadline) {
			t.Fatalf("store.db tree under %s never settled (%d stores)", workDir, len(stores))
		}
		time.Sleep(2 * time.Second)
	}
}

func cursorChatsTreeSig(stores []string) string {
	var b strings.Builder
	for _, store := range stores {
		info, err := os.Stat(store)
		if err != nil {
			continue
		}
		b.WriteString(store)
		b.WriteByte(0)
		b.WriteString(info.ModTime().UTC().Format(time.RFC3339Nano))
		b.WriteByte(0)
		// Include WAL sidecars: an uncheckpointed commit moves bytes there first.
		for _, sidecar := range []string{store + "-wal", store + "-shm"} {
			if side, err := os.Stat(sidecar); err == nil {
				b.WriteString(sidecar)
				b.WriteByte(0)
				b.WriteString(side.ModTime().UTC().Format(time.RFC3339Nano))
				b.WriteByte(0)
			}
		}
	}
	return b.String()
}

// cursorStoreBlobsContain reports whether any blob in the store carries the
// marker bytes. A direct SQL scan: no stream state, no side effects.
func cursorStoreBlobsContain(t *testing.T, storeDB, marker string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+storeDB+"?mode=ro")
	if err != nil {
		t.Fatalf("sql.Open %s: %v", storeDB, err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT data FROM blobs`)
	if err != nil {
		t.Fatalf("query blobs in %s: %v", storeDB, err)
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			t.Fatalf("scan blob in %s: %v", storeDB, err)
		}
		if strings.Contains(string(data), marker) {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows in %s: %v", storeDB, err)
	}
	return false
}
