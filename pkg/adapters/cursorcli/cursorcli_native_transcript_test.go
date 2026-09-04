package cursorcli

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// writeCursorStoreFixture builds a store.db shaped like cursor-agent's own
// (hex-encoded meta JSON, protobuf root of 32-byte refs, JSON message blobs)
// under ~/.cursor/chats/<md5(cwd)>/<agentId>/ and returns its path.
func writeCursorStoreFixture(t *testing.T, workingDir, agentID string, blobs []string) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	agentDir := filepath.Join(home, ".cursor", "chats", workingDirHashForCursor(workingDir), agentID)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(agentDir, "store.db")

	ids := make([][]byte, len(blobs))
	for i := range blobs {
		ids[i] = synthBlobID(byte(i + 1))
	}
	idRoot := synthBlobID(0xff)
	root := buildCursorRootBlob(t, ids)

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	metaJSON, _ := json.Marshal(map[string]any{"latestRootBlobId": hex.EncodeToString(idRoot)})
	if _, err := db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('0', ?)`, hex.EncodeToString(metaJSON)); err != nil {
		t.Fatal(err)
	}
	insert := func(id []byte, body []byte) {
		if _, err := db.ExecContext(ctx, `INSERT INTO blobs(id,data) VALUES(?, ?)`, hex.EncodeToString(id), body); err != nil {
			t.Fatal(err)
		}
	}
	insert(idRoot, root)
	for i, blob := range blobs {
		insert(ids[i], []byte(blob))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestReadNativeTranscriptKeepsTypedQueriesAndAssistantProse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workingDir := filepath.Join(home, "ws", "proj")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Two turns, exactly as observed live: per turn a provider-options
	// context blob (plain string) then the query blob (text block wrapping
	// <user_query>), then assistant reasoning/text/tool-call blobs.
	dbPath := writeCursorStoreFixture(t, workingDir, "agent-1", []string{
		`{"role":"system","content":"You are Auto."}`,
		`{"role":"user","content":"<user_info>\nOS Version: darwin\n</user_info>","providerOptions":{}}`,
		`{"role":"user","content":[{"type":"text","text":"<timestamp>Thursday, Sep 3, 2026, 8:42 PM</timestamp>\n<user_query>\nwhich strategy is winning\n</user_query>"}]}`,
		`{"role":"assistant","content":[{"type":"reasoning","text":"**Checking**"},{"type":"text","text":"Let me look."}]}`,
		`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"t1","toolName":"Read","args":{"path":"db"}}]}`,
		`{"role":"tool","content":[{"type":"tool-result","toolCallId":"t1","toolName":"Read","result":"rows"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"The builder flywheel is winning."}]}`,
		`{"role":"user","content":"<user_info>\nOS Version: darwin\n</user_info>","providerOptions":{}}`,
		`{"role":"user","content":[{"type":"text","text":"<user_query>\nand the runner up?\n</user_query>"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"Founder origin stories."}]}`,
	})

	transcript, ok, err := ReadNativeTranscript(workingDir, "agent-1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if transcript.Path != dbPath {
		t.Fatalf("path = %q, want %q", transcript.Path, dbPath)
	}
	want := []struct {
		role llmtypes.ChatMessageType
		text string
	}{
		{llmtypes.ChatMessageTypeHuman, "which strategy is winning"},
		{llmtypes.ChatMessageTypeAI, "Let me look."},
		{llmtypes.ChatMessageTypeAI, "The builder flywheel is winning."},
		{llmtypes.ChatMessageTypeHuman, "and the runner up?"},
		{llmtypes.ChatMessageTypeAI, "Founder origin stories."},
	}
	if len(transcript.Messages) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(transcript.Messages), len(want), transcript.Messages)
	}
	for i, w := range want {
		got := transcript.Messages[i]
		text := got.Parts[0].(llmtypes.TextContent).Text
		if got.Role != w.role || text != w.text {
			t.Fatalf("message %d = %s %q, want %s %q", i, got.Role, text, w.role, w.text)
		}
	}
	if transcript.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should come from the store's mtime")
	}

	if _, ok, err := ReadNativeTranscript(workingDir, "agent-missing"); ok || err != nil {
		t.Fatalf("missing agent: ok=%v err=%v, want false/nil", ok, err)
	}
}

// The turn reader shares the meta/root resolution; make sure the refactor
// kept it working on the same fixture shape.
func TestReadCursorStoreDBMessagesStillReadsAfterRootRefsRefactor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workingDir := filepath.Join(home, "ws", "proj")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := writeCursorStoreFixture(t, workingDir, "agent-2", []string{
		`{"role":"user","content":"<user_info>ctx</user_info>"}`,
		`{"role":"user","content":[{"type":"text","text":"<user_query>\nhi\n</user_query>"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"hello"}]}`,
	})
	msgs := readCursorStoreDBMessages(dbPath, "")
	if len(msgs) != 1 || msgs[0].Role != llmtypes.ChatMessageTypeAI {
		t.Fatalf("turn reader = %+v, want one assistant message", msgs)
	}
}
