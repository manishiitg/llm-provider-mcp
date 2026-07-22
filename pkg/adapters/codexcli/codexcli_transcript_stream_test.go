package codexcli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

// Real Codex (0.144+) rollout rows: assistant prose is an event_msg
// agent_message; MCP calls are event_msg mcp_tool_call_begin/_end with the tool
// under invocation.tool.
func codexAgentMsg(ts, text string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"agent_message","message":"` + text + `"}}` + "\n"
}
func codexMCPCallEnd(ts, tool, callID string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"` + callID + `","invocation":{"server":"api-bridge","tool":"` + tool + `"}}}` + "\n"
}

// TestReadCodexTranscriptEventsIncremental verifies the mid-turn rollout tailer
// against the REAL schema: prior-turn rows skipped, agent_message → content,
// mcp_tool_call_end → tool start (name from invocation.tool), reading from the
// returned offset yields only new rows, and a partial trailing line is held back.
func TestReadCodexTranscriptEventsIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	old := turnStart.Add(-time.Second).Format(time.RFC3339Nano)

	appendLine(t, path, codexAgentMsg(old, "PRIOR")+codexAgentMsg(ts, "Reading the file.")+codexMCPCallEnd(ts, "echo_contract", "c1"))

	events, off1, err := readCodexTranscriptEventsFromFile(path, 0, turnStart)
	if err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read 1: got %d events, want 2 (text, tool); %+v", len(events), events)
	}
	if events[0].Text != "Reading the file." || events[0].ToolName != "" {
		t.Fatalf("read 1 events[0] = %+v", events[0])
	}
	if events[1].ToolName != "echo_contract" || events[1].ToolCallID != "c1" {
		t.Fatalf("read 1 events[1] = %+v", events[1])
	}

	// Complete row + partial (no-newline) row.
	text2 := codexAgentMsg(ts, "Done.")
	partial := `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"agent_message","message":"Aft`
	appendLine(t, path, text2+partial)

	events, off2, err := readCodexTranscriptEventsFromFile(path, off1, turnStart)
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if len(events) != 1 || events[0].Text != "Done." {
		t.Fatalf("read 2: got %+v, want single 'Done.' (partial held)", events)
	}
	if off2 <= off1 {
		t.Fatalf("read 2: offset did not advance")
	}

	appendLine(t, path, `er."}}`+"\n")
	events, _, err = readCodexTranscriptEventsFromFile(path, off2, turnStart)
	if err != nil {
		t.Fatalf("read 3: %v", err)
	}
	if len(events) != 1 || events[0].Text != "After." {
		t.Fatalf("read 3: got %+v, want single 'After.' (completed partial)", events)
	}
}

// TestReadCodexTranscriptEventsInterleavedOrder proves the realistic
// text → tool → text → tool → final-text shape streams in correct order across
// incremental (append-live) polls, using the real event_msg schema.
func TestReadCodexTranscriptEventsInterleavedOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)

	rows := []string{
		codexAgentMsg(ts, "Let me check the first file."),
		codexMCPCallEnd(ts, "echo_contract", "c1"),
		codexAgentMsg(ts, "Now the second file."),
		codexMCPCallEnd(ts, "echo_contract", "c2"),
		codexAgentMsg(ts, "Done. FINAL."),
	}

	var got []string
	var offset int64
	for _, r := range rows {
		appendLine(t, path, r)
		events, next, err := readCodexTranscriptEventsFromFile(path, offset, turnStart)
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
		"tool:echo_contract",
		"text:Now the second file.",
		"tool:echo_contract",
		"text:Done. FINAL.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaved emission order wrong:\n got=%v\nwant=%v", got, want)
	}
}

// TestReadCodexTranscriptEventsResponseItemForm verifies the older response_item
// form is still parsed (assistant output_text + function_call), so both Codex
// schemas are covered.
func TestReadCodexTranscriptEventsResponseItemForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)

	appendLine(t, path,
		`{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`+"\n"+
			`{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call_A"}}`+"\n")

	events, _, err := readCodexTranscriptEventsFromFile(path, 0, turnStart)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 || events[0].Text != "hi" || events[1].ToolName != "exec_command" {
		t.Fatalf("response_item form parse wrong: %+v", events)
	}
}

// TestReadCodexTranscriptEventsRobustToNoiseAndUnicode covers production-variety
// input the happy-path tests miss: a garbage/non-JSON line and an unrelated event
// type interleaved with real rows must be skipped without derailing parsing, and
// multi-byte unicode (emoji, accents, CJK) in assistant text must survive
// byte-for-byte.
func TestReadCodexTranscriptEventsRobustToNoiseAndUnicode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	unicodeMsg := "Café ✅ 完了 — result is ZEBRA_✨"

	appendLine(t, path,
		"this is not json at all\n"+
			`{"timestamp":"`+ts+`","type":"session_meta","payload":{"type":"whatever"}}`+"\n"+
			codexAgentMsg(ts, unicodeMsg)+
			`{"broken":`+"\n"+ // truncated/garbage JSON
			codexMCPCallEnd(ts, "echo_contract", "c1"))

	events, _, err := readCodexTranscriptEventsFromFile(path, 0, turnStart)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("noise not skipped cleanly: got %d events, want 2 (unicode text + tool); %+v", len(events), events)
	}
	if events[0].Text != unicodeMsg {
		t.Fatalf("unicode text mangled:\n got=%q\nwant=%q", events[0].Text, unicodeMsg)
	}
	if events[1].ToolName != "echo_contract" {
		t.Fatalf("tool after noise not parsed: %+v", events[1])
	}
}

// TestReadCodexTranscriptEventsLargeContentLine proves a large assistant message
// (multi-KB, the kind a real coding turn emits) is read whole, not truncated at
// the tailer's read boundary.
func TestReadCodexTranscriptEventsLargeContentLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	big := strings.Repeat("A", 200*1024) // 200KB assistant message

	appendLine(t, path, codexAgentMsg(ts, big))
	events, _, err := readCodexTranscriptEventsFromFile(path, 0, turnStart)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 1 || len(events[0].Text) != len(big) {
		t.Fatalf("large content not read whole: got %d events, text len %d (want 1 event, len %d)", len(events), func() int {
			if len(events) > 0 {
				return len(events[0].Text)
			}
			return 0
		}(), len(big))
	}
}
