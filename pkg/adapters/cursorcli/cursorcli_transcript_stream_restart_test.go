package cursorcli

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorTranscriptStreamDoesNotReplayHistoryOnFreshProcess is a regression
// test for a real, user-visible bug: on the first turn of a FRESH process, the
// transcript streamer replayed the entire chat history as if it were the current
// reply.
//
// Mechanism: cursor's store.db root is cumulative across every turn of a chat,
// and the only thing that kept already-streamed messages from being re-emitted
// was cursorReturnedBlobs — an in-process map. A server restart empties that map
// while the transcript on disk still holds every prior turn's assistant text, so
// the first poll classified all of it as new. A UI that appends stream deltas
// then rendered the whole backlog of "thinking" narration into the current
// reply. Observed live in SparkQuill as 22 historical narration lines replayed
// on the first message after each restart.
//
// The test simulates the restart precisely — a populated store.db plus an EMPTY
// dedup map, which is exactly the state a new process starts in — and asserts no
// pre-existing content is streamed. Remove primeSeenBlobs() from
// newCursorTranscriptStreamState and this fails with the historical text.
func TestCursorTranscriptStreamDoesNotReplayHistoryOnFreshProcess(t *testing.T) {
	const (
		historicalOne = "Checking Myra's school materials for how they use this term."
		historicalTwo = "Regenerating both reports with today's evidence."
	)

	workingDir := newCursorHistoryFixture(t, historicalOne, historicalTwo)

	// A fresh process starts with an empty dedup map. Make that explicit rather
	// than relying on test-ordering luck: another test in this package touching
	// the same owner key would otherwise mask the very bug under test.
	const ownerSessionID = "restart-regression-owner"
	resetCursorReturnedBlobs(ownerSessionID + "\x00transcript-stream")

	state := newCursorTranscriptStreamState(time.Now().Add(-time.Hour), workingDir, ownerSessionID)

	got := collectCursorStreamChunks(t, state)

	for _, text := range []string{historicalOne, historicalTwo} {
		for _, chunk := range got {
			if chunk.Type == llmtypes.StreamChunkTypeContent && strings.Contains(chunk.Content, text) {
				t.Errorf("history replayed into the stream on a fresh process: %q\n"+
					"all %d chunks: %#v", text, len(got), got)
			}
		}
	}
}

// TestCursorTranscriptStreamEmitsGenuinelyNewText is the other half of the
// contract: priming must suppress only what already existed, never text the
// current turn actually produces. Without this, "fix the replay" could be
// satisfied by emitting nothing at all.
func TestCursorTranscriptStreamEmitsGenuinelyNewText(t *testing.T) {
	const (
		historical = "Old narration from a previous turn."
		fresh      = "Brand new narration from THIS turn."
	)

	workingDir := newCursorHistoryFixture(t, historical)

	const ownerSessionID = "new-text-owner"
	resetCursorReturnedBlobs(ownerSessionID + "\x00transcript-stream")

	// Prime against history only...
	state := newCursorTranscriptStreamState(time.Now().Add(-time.Hour), workingDir, ownerSessionID)

	// ...then the turn produces new output, exactly as cursor would commit it.
	appendCursorHistory(t, workingDir, historical, fresh)

	got := collectCursorStreamChunks(t, state)

	var streamed strings.Builder
	for _, chunk := range got {
		if chunk.Type == llmtypes.StreamChunkTypeContent {
			streamed.WriteString(chunk.Content)
		}
	}
	if !strings.Contains(streamed.String(), fresh) {
		t.Errorf("new text was NOT streamed; priming over-suppressed.\ngot: %q\nchunks: %#v",
			streamed.String(), got)
	}
	if strings.Contains(streamed.String(), historical) {
		t.Errorf("history leaked into the stream alongside new text.\ngot: %q", streamed.String())
	}
}

// resetCursorReturnedBlobs clears one stream key's dedup set, simulating the
// empty map a newly-started process begins with.
func resetCursorReturnedBlobs(streamKey string) {
	cursorReturnedBlobsMu.Lock()
	defer cursorReturnedBlobsMu.Unlock()
	delete(cursorReturnedBlobs, streamKey)
}

// collectCursorStreamChunks runs one poll and returns everything it emitted.
func collectCursorStreamChunks(t *testing.T, state *cursorTranscriptStreamState) []llmtypes.StreamChunk {
	t.Helper()
	ch := make(chan llmtypes.StreamChunk, 256)
	state.poll(context.Background(), ch)
	close(ch)
	var out []llmtypes.StreamChunk
	for chunk := range ch {
		out = append(out, chunk)
	}
	return out
}

// newCursorHistoryFixture builds a store.db in the on-disk layout the resolver
// expects (HOME/.cursor/chats/<md5(cwd)>/<agent>/store.db), pre-populated with
// the given assistant texts as prior-turn history, and returns the workingDir.
func newCursorHistoryFixture(t *testing.T, assistantTexts ...string) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workingDir := filepath.Join(tmpHome, "ws", "proj")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll workingDir: %v", err)
	}
	writeCursorStoreDB(t, workingDir, assistantTexts...)
	return workingDir
}

// appendCursorHistory rewrites the fixture's store.db with a longer message
// list, standing in for cursor committing new blobs mid-turn.
func appendCursorHistory(t *testing.T, workingDir string, assistantTexts ...string) {
	t.Helper()
	writeCursorStoreDB(t, workingDir, assistantTexts...)
}

// writeCursorStoreDB (re)creates the store.db for workingDir containing one
// assistant text blob per entry, wired through a root protobuf the same way the
// real schema does. Blob IDs are derived from the text's position so a rewrite
// reuses the SAME id for unchanged text — matching cursor's content-addressed
// ids, which is what makes dedup meaningful.
func writeCursorStoreDB(t *testing.T, workingDir string, assistantTexts ...string) {
	t.Helper()
	writeCursorStoreDBAt(t, workingDir, "agent-restart-test", assistantTexts...)
}

// writeCursorStoreDBAt is writeCursorStoreDB with the agent-session folder
// name (cursor's own native_session_id equivalent) as a parameter, so a test
// can create MULTIPLE distinct sessions under the same workingDir's chatsDir
// — e.g. one standing in for the real conversation and another for an
// unrelated bounded call (read_image) that happens to share the same
// workingDir. See TestReadCursorTranscriptPrefersKnownSessionOverNewerDecoy.
func writeCursorStoreDBAt(t *testing.T, workingDir string, agentSessionID string, assistantTexts ...string) {
	t.Helper()
	hash := workingDirHashForCursor(workingDir)
	if hash == "" {
		t.Fatal("workingDirHashForCursor returned empty")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	agentDir := filepath.Join(home, ".cursor", "chats", hash, agentSessionID)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("MkdirAll agentDir: %v", err)
	}
	dbPath := filepath.Join(agentDir, "store.db")
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove old store.db: %v", err)
	}

	idUserCtx := synthBlobID(0x02)
	idUserQuery := synthBlobID(0x03)
	idRoot := synthBlobID(0xff)
	refs := [][]byte{idUserCtx, idUserQuery}

	type blob struct {
		id   []byte
		body []byte
	}
	blobs := []blob{
		{idUserCtx, []byte(`{"role":"user","content":"<user_info>cwd:/ws/proj</user_info>","providerOptions":{}}`)},
		{idUserQuery, []byte(`{"role":"user","content":[{"type":"text","text":"<user_query>\nhello\n</user_query>"}]}`)},
	}
	for i, text := range assistantTexts {
		id := synthBlobID(byte(0x10 + i))
		body, err := json.Marshal(map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "text", "text": text}},
		})
		if err != nil {
			t.Fatalf("marshal assistant blob: %v", err)
		}
		blobs = append(blobs, blob{id, body})
		refs = append(refs, id)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)`); err != nil {
		t.Fatalf("create blobs: %v", err)
	}
	metaJSON, _ := json.Marshal(map[string]any{"latestRootBlobId": hex.EncodeToString(idRoot)})
	if _, err := db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('0', ?)`, hex.EncodeToString(metaJSON)); err != nil {
		t.Fatalf("insert meta: %v", err)
	}
	blobs = append(blobs, blob{idRoot, buildCursorRootBlob(t, refs)})
	for _, b := range blobs {
		if _, err := db.ExecContext(ctx, `INSERT INTO blobs(id,data) VALUES(?, ?)`,
			hex.EncodeToString(b.id), b.body); err != nil {
			t.Fatalf("insert blob %x: %v", b.id, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	// The resolver skips store.db older than the turn baseline.
	now := time.Now()
	if err := os.Chtimes(dbPath, now, now); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}
