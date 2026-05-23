package cursorcli

import (
	"context"
	"crypto/md5" //nolint:gosec
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestReadCursorTranscriptMessagesShapesAToolLoop builds a synthetic
// cursor store.db mirroring the on-disk layout (md5-of-cwd workspace
// dir + agent dir + sqlite file with meta + blobs tables, root
// protobuf with field-1 refs to JSON message blobs) and verifies the
// parser:
//  1. resolves the db via workingDir → md5 → chats/<hash>/<agent>/;
//  2. follows the protobuf root's field-1 refs in chronological order;
//  3. maps role+content-type to llmtypes parts;
//  4. skips system, the first user (provider-options context),
//     subsequent user blobs (already in outer history), and
//     redacted-reasoning blocks.
func TestReadCursorTranscriptMessagesShapesAToolLoop(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Pick a cwd that the test can deterministically md5.
	workingDir := filepath.Join(tmpHome, "ws", "proj")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll workingDir: %v", err)
	}
	hash := workingDirHashForCursor(workingDir)
	if hash == "" {
		t.Fatal("workingDirHashForCursor returned empty")
	}

	agentDir := filepath.Join(tmpHome, ".cursor", "chats", hash, "agent-test-1")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("MkdirAll agentDir: %v", err)
	}
	dbPath := filepath.Join(agentDir, "store.db")

	// Build message blobs. Use synthetic 32-byte IDs (the parser
	// doesn't verify they hash the content — it just looks them up).
	systemMsg := `{"role":"system","content":"you are composer"}`
	userCtx := `{"role":"user","content":"<user_info>cwd:/ws/proj</user_info>","providerOptions":{}}`
	userQuery := `{"role":"user","content":[{"type":"text","text":"<user_query>\nedit foo.go\n</user_query>"}]}`
	assistantText := `{"role":"assistant","content":[{"type":"redacted-reasoning","data":"AAA"},{"type":"text","text":"I'll read foo.go first."}]}`
	assistantToolCall := `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"tool_1","toolName":"Read","args":{"path":"foo.go"}}]}`
	toolResult := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"tool_1","toolName":"Read","result":"package main"}]}`
	finalText := `{"role":"assistant","content":[{"type":"text","text":"Done."}]}`

	idSys := synthBlobID(0x01)
	idUserCtx := synthBlobID(0x02)
	idUserQuery := synthBlobID(0x03)
	idAsstText := synthBlobID(0x04)
	idAsstTool := synthBlobID(0x05)
	idToolRes := synthBlobID(0x06)
	idFinal := synthBlobID(0x07)
	idRoot := synthBlobID(0xff)

	// Build a root protobuf with field-1 (wire-type 2, length 32)
	// refs in chronological order, plus a couple of metadata fields
	// the parser ignores (proves it tolerates extra fields).
	root := buildCursorRootBlob(t, [][]byte{
		idSys, idUserCtx, idUserQuery, idAsstText, idAsstTool, idToolRes, idFinal,
	})

	// Create sqlite db + tables matching cursor's schema.
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatalf("create meta: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)`); err != nil {
		t.Fatalf("create blobs: %v", err)
	}
	// meta.value is hex-encoded JSON in the real cursor schema.
	metaJSON, _ := json.Marshal(map[string]any{
		"latestRootBlobId": hex.EncodeToString(idRoot),
		"lastUsedModel":    "composer-2.5",
	})
	if _, err := db.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES('0', ?)`, hex.EncodeToString(metaJSON)); err != nil {
		t.Fatalf("insert meta: %v", err)
	}
	insertBlob := func(id []byte, body []byte) {
		t.Helper()
		if _, err := db.ExecContext(context.Background(), `INSERT INTO blobs(id,data) VALUES(?, ?)`, hex.EncodeToString(id), body); err != nil {
			t.Fatalf("insert blob %x: %v", id, err)
		}
	}
	insertBlob(idRoot, root)
	insertBlob(idSys, []byte(systemMsg))
	insertBlob(idUserCtx, []byte(userCtx))
	insertBlob(idUserQuery, []byte(userQuery))
	insertBlob(idAsstText, []byte(assistantText))
	insertBlob(idAsstTool, []byte(assistantToolCall))
	insertBlob(idToolRes, []byte(toolResult))
	insertBlob(idFinal, []byte(finalText))
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Bump mtime so the parser's cutoff filter accepts it.
	now := time.Now()
	if err := os.Chtimes(dbPath, now, now); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	turnStart := time.Now().Add(-1 * time.Hour)
	msgs := readCursorTranscriptMessages(turnStart, workingDir, "")

	// Expect: assistant text, assistant tool_call, tool result, final assistant text.
	// (System, user-context, user-query all skipped.)
	if len(msgs) != 4 {
		t.Fatalf("got %d messages, want 4; msgs=%+v", len(msgs), msgs)
	}

	// [0] assistant text (with redacted-reasoning silently dropped)
	if msgs[0].Role != llmtypes.ChatMessageTypeAI {
		t.Fatalf("msgs[0].Role = %q, want AI", msgs[0].Role)
	}
	if tc, ok := msgs[0].Parts[0].(llmtypes.TextContent); !ok || tc.Text != "I'll read foo.go first." {
		t.Fatalf("msgs[0].Parts[0] = %+v, want TextContent 'I'll read foo.go first.'", msgs[0].Parts[0])
	}

	// [1] assistant tool_call
	tcall, ok := msgs[1].Parts[0].(llmtypes.ToolCall)
	if !ok {
		t.Fatalf("msgs[1].Parts[0] = %T, want ToolCall", msgs[1].Parts[0])
	}
	if tcall.ID != "tool_1" || tcall.FunctionCall == nil || tcall.FunctionCall.Name != "Read" {
		t.Fatalf("ToolCall = %+v, want ID=tool_1 Name=Read", tcall)
	}
	if !strings.Contains(tcall.FunctionCall.Arguments, `"path":"foo.go"`) {
		t.Fatalf("ToolCall.Arguments = %q, want to contain path:foo.go", tcall.FunctionCall.Arguments)
	}

	// [2] tool result
	if msgs[2].Role != llmtypes.ChatMessageTypeTool {
		t.Fatalf("msgs[2].Role = %q, want Tool", msgs[2].Role)
	}
	tres, ok := msgs[2].Parts[0].(llmtypes.ToolCallResponse)
	if !ok || tres.ToolCallID != "tool_1" || tres.Content != "package main" {
		t.Fatalf("msgs[2].Parts[0] = %+v, want ToolCallResponse{tool_1, 'package main'}", msgs[2].Parts[0])
	}

	// [3] final assistant text
	if tc, ok := msgs[3].Parts[0].(llmtypes.TextContent); !ok || tc.Text != "Done." {
		t.Fatalf("msgs[3].Parts[0] = %+v, want TextContent 'Done.'", msgs[3].Parts[0])
	}
}

// TestReadCursorTranscriptMessagesDedupesAcrossMultiTurnCalls
// proves the per-session cache: cursor's root is cumulative, so on
// turn 2 the root references both turn-1 and turn-2 message blobs.
// Two sequential calls with the same ownerSessionID must produce
// strictly the new messages on the second call.
func TestReadCursorTranscriptMessagesDedupesAcrossMultiTurnCalls(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	workingDir := filepath.Join(tmpHome, "ws", "proj")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	hash := workingDirHashForCursor(workingDir)
	agentDir := filepath.Join(tmpHome, ".cursor", "chats", hash, "agent-multi-turn")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("MkdirAll agentDir: %v", err)
	}
	dbPath := filepath.Join(agentDir, "store.db")

	systemMsg := `{"role":"system","content":"sys"}`
	userT1 := `{"role":"user","content":"ctx"}`
	asstT1 := `{"role":"assistant","content":[{"type":"text","text":"turn-1 answer"}]}`
	userT2 := `{"role":"user","content":[{"type":"text","text":"<user_query>turn 2</user_query>"}]}`
	asstT2 := `{"role":"assistant","content":[{"type":"text","text":"turn-2 answer"}]}`

	idSys := synthBlobID(0x11)
	idUserT1 := synthBlobID(0x12)
	idAsstT1 := synthBlobID(0x13)
	idUserT2 := synthBlobID(0x14)
	idAsstT2 := synthBlobID(0x15)
	idRoot1 := synthBlobID(0xa1)
	idRoot2 := synthBlobID(0xa2)

	root1 := buildCursorRootBlob(t, [][]byte{idSys, idUserT1, idAsstT1})
	root2 := buildCursorRootBlob(t, [][]byte{idSys, idUserT1, idAsstT1, idUserT2, idAsstT2})

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, _ = db.ExecContext(context.Background(), `CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`)
	_, _ = db.ExecContext(context.Background(), `CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)`)
	insertBlob := func(id []byte, body []byte) {
		t.Helper()
		_, _ = db.ExecContext(context.Background(), `INSERT INTO blobs(id,data) VALUES(?, ?)`, hex.EncodeToString(id), body)
	}
	insertBlob(idRoot1, root1)
	insertBlob(idRoot2, root2)
	insertBlob(idSys, []byte(systemMsg))
	insertBlob(idUserT1, []byte(userT1))
	insertBlob(idAsstT1, []byte(asstT1))
	insertBlob(idUserT2, []byte(userT2))
	insertBlob(idAsstT2, []byte(asstT2))

	// Turn 1: latest root is root1.
	setMeta := func(rootID []byte) {
		t.Helper()
		metaJSON, _ := json.Marshal(map[string]any{"latestRootBlobId": hex.EncodeToString(rootID)})
		_, _ = db.ExecContext(context.Background(), `DELETE FROM meta`)
		_, _ = db.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES('0', ?)`, hex.EncodeToString(metaJSON))
	}
	setMeta(idRoot1)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	now := time.Now()
	_ = os.Chtimes(dbPath, now, now)

	turnStart := time.Now().Add(-1 * time.Hour)
	const owner = "owner-multi-turn"

	t1 := readCursorTranscriptMessages(turnStart, workingDir, owner)
	if len(t1) != 1 {
		t.Fatalf("turn 1: got %d msgs, want 1 (asstT1); msgs=%+v", len(t1), t1)
	}
	if tc, ok := t1[0].Parts[0].(llmtypes.TextContent); !ok || tc.Text != "turn-1 answer" {
		t.Fatalf("turn 1 msg[0] = %+v, want 'turn-1 answer'", t1[0].Parts[0])
	}

	// Promote root2 (turn 2 occurred — cursor wrote a new root with
	// all 5 refs, accumulating turn-1 refs and adding turn-2 refs).
	db, err = sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open #2: %v", err)
	}
	setMeta(idRoot2)
	if err := db.Close(); err != nil {
		t.Fatalf("close #2: %v", err)
	}
	_ = os.Chtimes(dbPath, now, now)

	t2 := readCursorTranscriptMessages(turnStart, workingDir, owner)
	if len(t2) != 1 {
		t.Fatalf("turn 2: got %d msgs, want 1 (only asstT2 — asstT1 must be deduped); msgs=%+v", len(t2), t2)
	}
	if tc, ok := t2[0].Parts[0].(llmtypes.TextContent); !ok || tc.Text != "turn-2 answer" {
		t.Fatalf("turn 2 msg[0] = %+v, want 'turn-2 answer'", t2[0].Parts[0])
	}
}

// TestReadCursorTranscriptMessagesReturnsNilWhenNoSession proves the
// parser is silent when no cursor session exists for the workingDir.
func TestReadCursorTranscriptMessagesReturnsNilWhenNoSession(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	got := readCursorTranscriptMessages(time.Now().Add(-1*time.Hour), filepath.Join(tmpHome, "nonexistent"), "")
	if got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

// TestWorkingDirHashForCursorMatchesCursorConvention sanity-checks
// our md5 of cwd against a known-good value derived from a real
// cursor chats/ directory.
func TestWorkingDirHashForCursorMatchesCursorConvention(t *testing.T) {
	const cwd = "/Users/mipl/ai-work/mcp-agent-builder-go/agent_go"
	const want = "6896b5fb4c04f7509edfcea25986ea6d"
	got := workingDirHashForCursor(cwd)
	if got != want {
		t.Fatalf("workingDirHashForCursor(%q) = %q, want %q", cwd, got, want)
	}
}

// synthBlobID returns a deterministic 32-byte id for the test (one
// repeated marker byte). Real cursor IDs are SHA256 of content; the
// parser only uses them as opaque lookup keys, so any 32 bytes work.
func synthBlobID(marker byte) []byte {
	id := make([]byte, 32)
	for i := range id {
		id[i] = marker
	}
	// Embed marker count to keep ids distinguishable when debugging.
	binary.BigEndian.PutUint16(id[:2], uint16(marker))
	return id
}

// buildCursorRootBlob constructs a protobuf root blob with one
// length-delimited field-1 entry per child ref, in source order.
func buildCursorRootBlob(t *testing.T, refs [][]byte) []byte {
	t.Helper()
	var buf []byte
	for _, r := range refs {
		if len(r) != 32 {
			t.Fatalf("ref must be 32 bytes, got %d", len(r))
		}
		// tag = field<<3 | wire_type
		// field 1, wire_type 2 (length-delimited) → 0x0a
		buf = append(buf, 0x0a)
		// length = 32 (single varint byte)
		buf = append(buf, 32)
		buf = append(buf, r...)
	}
	// Add a couple of unrelated fields the parser should ignore.
	buf = append(buf, 0x10, 0x01) // field 2, varint, value 1
	buf = append(buf, 0xb0, 0x01, // field 22 (22<<3|0 = 176 = 0xb0)
		0x03, 'c', 'l', 'i')
	_ = md5.Sum //nolint:gosec // suppress unused import warning when not exercised
	return buf
}
