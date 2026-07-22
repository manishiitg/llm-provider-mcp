package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func appendLine(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestReadClaudeTranscriptEventsIncremental verifies the mid-turn tailer:
//  1. rows older than turnStart are skipped (a resumed session's prior turns
//     don't replay);
//  2. assistant text → content event, tool_use → tool-call-start event;
//  3. reading from the returned offset yields ONLY newly-appended rows (no
//     re-emission);
//  4. a partial (not-yet-newline-terminated) trailing line is held back and
//     only surfaces once completed.
func TestReadClaudeTranscriptEventsIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")

	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	old := turnStart.Add(-time.Second).Format(time.RFC3339Nano)

	oldTurn := `{"type":"assistant","timestamp":"` + old + `","message":{"id":"msg_0","content":[{"type":"text","text":"PRIOR TURN"}]}}` + "\n"
	text1 := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_A","content":[{"type":"text","text":"Reading the file."}]}}` + "\n"
	tool1 := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_A","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"foo.go"}}]}}` + "\n"

	appendLine(t, path, oldTurn+text1+tool1)

	// First read from offset 0: skips the prior turn, emits text then tool.
	events, off1, err := readClaudeTranscriptEventsFromFile(path, 0, turnStart)
	if err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read 1: got %d events, want 2 (text, tool); %+v", len(events), events)
	}
	if events[0].Text != "Reading the file." || events[0].ToolName != "" {
		t.Fatalf("read 1 events[0] = %+v, want text 'Reading the file.'", events[0])
	}
	if events[1].ToolName != "Read" || events[1].ToolCallID != "toolu_1" || events[1].Text != "" {
		t.Fatalf("read 1 events[1] = %+v, want tool Read/toolu_1", events[1])
	}

	// Append one complete line plus a PARTIAL line (no trailing newline yet).
	text2 := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_B","content":[{"type":"text","text":"Done."}]}}` + "\n"
	partial := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_C","content":[{"type":"text","text":"Aft`
	appendLine(t, path, text2+partial)

	events, off2, err := readClaudeTranscriptEventsFromFile(path, off1, turnStart)
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if len(events) != 1 || events[0].Text != "Done." {
		t.Fatalf("read 2: got %+v, want single 'Done.' (partial line held back)", events)
	}
	if off2 <= off1 {
		t.Fatalf("read 2: offset did not advance (%d -> %d)", off1, off2)
	}

	// Complete the held-back partial line.
	rest := `er."}]}}` + "\n"
	appendLine(t, path, rest)

	events, _, err = readClaudeTranscriptEventsFromFile(path, off2, turnStart)
	if err != nil {
		t.Fatalf("read 3: %v", err)
	}
	if len(events) != 1 || events[0].Text != "After." {
		t.Fatalf("read 3: got %+v, want single 'After.' (completed partial line)", events)
	}
}

// TestReadClaudeTranscriptEventsInterleavedOrder proves the realistic shape a
// real turn produces — text → tool → text → tool → final text — streams in the
// correct ORDER across incremental (append-live) polls, and that tool_result
// (user) rows are skipped rather than emitted.
func TestReadClaudeTranscriptEventsInterleavedOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)

	asst := func(id, block string) string {
		return `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"` + id + `","content":[` + block + `]}}` + "\n"
	}
	text := func(s string) string { return `{"type":"text","text":"` + s + `"}` }
	toolUse := func(id, name string) string {
		return `{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":{}}`
	}
	toolResult := func(id string) string {
		return `{"type":"user","timestamp":"` + ts + `","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]}}` + "\n"
	}

	rows := []string{
		asst("mA", text("Let me check the first file.")),
		asst("mA", toolUse("t1", "read_file")),
		toolResult("t1"),
		asst("mB", text("Now the second file.")),
		asst("mB", toolUse("t2", "read_file")),
		toolResult("t2"),
		asst("mC", text("Done. FINAL.")),
	}

	var got []string
	var offset int64
	for _, row := range rows { // append one row at a time, poll after each
		appendLine(t, path, row)
		events, next, err := readClaudeTranscriptEventsFromFile(path, offset, turnStart)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		offset = next
		for _, e := range events {
			if e.ToolName != "" {
				got = append(got, "tool:"+e.ToolName)
			} else {
				got = append(got, "text:"+e.Text)
			}
		}
	}

	want := []string{
		"text:Let me check the first file.",
		"tool:read_file",
		"text:Now the second file.",
		"tool:read_file",
		"text:Done. FINAL.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaved emission order wrong:\n got=%v\nwant=%v", got, want)
	}
}

// TestStreamClaudeTranscriptEmitsChunks drives the full goroutine against a
// transcript under a fake $HOME, asserting it globs the sidecar, tails it, and
// emits mapped StreamChunks onto the channel.
func TestStreamClaudeTranscriptEmitsChunks(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "11111111-2222-3333-4444-555566667777"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-fake")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(projectDir, sessionID+".jsonl")

	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	appendLine(t, path,
		`{"type":"assistant","timestamp":"`+ts+`","message":{"id":"m1","content":[{"type":"text","text":"Hello"}]}}`+"\n"+
			`{"type":"assistant","timestamp":"`+ts+`","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Write","input":{}}]}}`+"\n")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ch := make(chan llmtypes.StreamChunk, 8)
	go streamClaudeTranscript(ctx, sessionID, turnStart, ch)

	got := map[llmtypes.StreamChunkType]llmtypes.StreamChunk{}
	deadline := time.After(3 * time.Second)
	for len(got) < 2 {
		select {
		case c := <-ch:
			got[c.Type] = c
		case <-deadline:
			t.Fatalf("timed out; got %d chunk kinds: %+v", len(got), got)
		}
	}

	if c := got[llmtypes.StreamChunkTypeContent]; c.Content != "Hello" {
		t.Fatalf("content chunk = %+v, want Content 'Hello'", c)
	}
	if c := got[llmtypes.StreamChunkTypeToolCallStart]; c.ToolName != "Write" || c.ToolCallID != "t1" {
		t.Fatalf("tool chunk = %+v, want ToolCallStart Write/t1", c)
	}
	if c := got[llmtypes.StreamChunkTypeContent]; c.Metadata["claude_code_stream_source"] != "transcript" {
		t.Fatalf("missing stream-source metadata: %+v", c.Metadata)
	}
}
