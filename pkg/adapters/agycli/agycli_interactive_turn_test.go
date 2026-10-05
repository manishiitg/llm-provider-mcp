package agycli

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"google.golang.org/protobuf/encoding/protowire"
	_ "modernc.org/sqlite"
)

func TestAgyMountFingerprint(t *testing.T) {
	if got := agyMountFingerprint(""); got != "unmounted" {
		t.Fatalf("empty = %q, want unmounted", got)
	}
	if got := agyMountFingerprint("  \n"); got != "unmounted" {
		t.Fatalf("blank = %q, want unmounted", got)
	}
	a := agyMountFingerprint(`{"mcpServers":{"x":{"command":"node"}}}`)
	b := agyMountFingerprint("  " + `{"mcpServers":{"x":{"command":"node"}}}` + "\n")
	c := agyMountFingerprint(`{"mcpServers":{"y":{"command":"node"}}}`)
	if a == "unmounted" || a != b {
		t.Fatalf("same config fingerprints differ: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different configs share fingerprint %q", a)
	}
}

func TestAgyTurnRecordBindsMatchingUserAndWaitsForFinalAssistant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "turn.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER, status INTEGER, step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	insert := func(idx, stepType int, payload []byte) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), `INSERT INTO steps VALUES (?, ?, 3, ?)`, idx, stepType, payload); err != nil {
			t.Fatal(err)
		}
	}
	insert(0, agyStepUser, agyTestPayload(agyStepUser, 19, 2, "old prompt"))
	insert(1, agyStepAssistant, agyTestPayload(agyStepAssistant, 20, 1, "old answer"))
	insert(2, agyStepUser, agyTestPayload(agyStepUser, 19, 2, "new prompt"))
	insert(3, agyStepAssistant, agyTestPayload(agyStepAssistant, 20, 3, "private thinking"))
	insert(4, agyStepToolCall, agyTestPayload(agyStepToolCall, 5, 2, "tool"))
	record, err := agyReadTurnRecord("turn", -1, "new prompt")
	if err != nil || record.userIdx != 2 || record.lastType != agyStepToolCall || record.answer != "" {
		t.Fatalf("pending tool record = %+v, err %v", record, err)
	}
	insert(5, agyStepAssistant, agyTestPayload(agyStepAssistant, 20, 1, "new answer"))
	record, err = agyReadTurnRecord("turn", 2, "")
	if err != nil || record.lastType != agyStepAssistant || record.lastStatus != 3 || record.answer != "new answer" || record.finalAnswer != "new answer" {
		t.Fatalf("completed record = %+v, err %v", record, err)
	}
	insert(6, agyStepUser, agyTestPayload(agyStepUser, 19, 2, "tool prompt"))
	insert(7, agyStepAssistant, agyTestPayload(agyStepAssistant, 20, 1, "working on it"))
	insert(8, agyStepToolCall, agyTestPayload(agyStepToolCall, 5, 2, "tool"))
	insert(9, agyStepAssistant, agyTestPayload(agyStepAssistant, 20, 1, "final answer"))
	record, err = agyReadTurnRecord("turn", 6, "")
	if err != nil || record.answer != "working on it\n\nfinal answer" || record.finalAnswer != "final answer" {
		t.Fatalf("progress/final record = %+v, err %v", record, err)
	}
	if got := agyTurnReplySince("turn", 6); got != "final answer" {
		t.Fatalf("final reply = %q, want final answer without progress", got)
	}
}

func TestAgyTurnUsageDecodesGoldenPayload(t *testing.T) {
	raw, err := os.ReadFile("testdata/assistant_step_usage.bin")
	if err != nil {
		t.Fatalf("read golden payload: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Captured live 2026-09-20: field 5.9 holds input 8523, output 313,
	// thinking 309 — the same turn shape exec JSON reports. Pins the tag
	// mapping against real CLI bytes, not hand-built fixtures.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "golden.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER, step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO steps (idx, step_type, step_payload) VALUES (0, 14, ?)`, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO steps (idx, step_type, step_payload) VALUES (1, 15, ?)`, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got := agyTurnUsageSince("golden", -1)
	if got.InputTokens != 8523 || got.OutputTokens != 313 {
		t.Fatalf("usage = %+v, want input 8523 output 313", got)
	}
	if got.TotalTokens != 8836 {
		t.Fatalf("total = %d, want 8836", got.TotalTokens)
	}
	if got.ThoughtsTokens == nil || *got.ThoughtsTokens != 309 {
		t.Fatalf("thinking = %+v, want 309", got.ThoughtsTokens)
	}
	if since := agyTurnUsageSince("golden", 1); since.TotalTokens != 0 {
		t.Fatalf("usage since idx 1 = %+v, want zero (no new steps)", since)
	}
	if missing := agyTurnUsageSince("nope", -1); missing.TotalTokens != 0 {
		t.Fatalf("usage for missing conversation = %+v, want zero", missing)
	}
}

// A real CHECKPOINT row (captured 2026-06-10, conversation 0370df02 idx 4) is
// the intent-only title checkpoint AGY writes after every first answer: it must
// not read as a compaction. The same row with intent_only cleared is what a
// real compaction records, and becomes one End chunk timed from its metadata.
func TestAgyCheckpointStepCompaction(t *testing.T) {
	raw, err := os.ReadFile("testdata/checkpoint_step_intent_only.bin")
	if err != nil {
		t.Fatal(err)
	}
	var compacting []byte
	for rest := raw; len(rest) > 0; {
		num, typ, n := protowire.ConsumeTag(rest)
		m := protowire.ConsumeFieldValue(num, typ, rest[n:])
		if n < 0 || m < 0 {
			t.Fatal("bad fixture")
		}
		field := rest[:n+m]
		if num == 30 {
			inner, _ := protowire.ConsumeBytes(rest[n:])
			var kept []byte
			for r := inner; len(r) > 0; {
				in, it, k := protowire.ConsumeTag(r)
				l := protowire.ConsumeFieldValue(in, it, r[k:])
				if in != 9 {
					kept = append(kept, r[:k+l]...)
				}
				r = r[k+l:]
			}
			field = protowire.AppendBytes(protowire.AppendTag(nil, 30, protowire.BytesType), kept)
		}
		compacting = append(compacting, field...)
		rest = rest[n+m:]
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "c1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER, status INTEGER, step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	for idx, payload := range [][]byte{raw, compacting} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO steps VALUES (?, 23, 3, ?)`, idx, payload); err != nil {
			t.Fatal(err)
		}
	}
	record, err := agyReadTurnRecord("c1", -1, "", home)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.compactions) != 1 {
		t.Fatalf("want only the non-intent checkpoint, got %+v", record.compactions)
	}
	chunk := llmtypes.ContextCompactionChunk(record.compactions[0])
	got := chunk.ContextCompaction
	if chunk.Type != llmtypes.StreamChunkTypeContextCompaction || got.Provider != "agy-cli" || got.Phase != llmtypes.ContextCompactionPhaseEnd ||
		got.ID != "c1:1" || got.Outcome != llmtypes.ContextCompactionOutcomeSuccess ||
		!got.StartedAt.Equal(time.Unix(1781070351, 779980000)) || !got.EndedAt.Equal(time.Unix(1781070352, 571718000)) ||
		got.DurationMs != 791 || got.TokensBefore != 0 || got.TokensAfter != 0 {
		t.Fatalf("unexpected compaction chunk %+v", got)
	}
}
