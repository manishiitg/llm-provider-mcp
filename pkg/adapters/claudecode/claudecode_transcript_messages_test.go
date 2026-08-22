package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestReadClaudeTranscriptMessagesShapesAToolUseLoop verifies the
// full claude → llmtypes shape mapping by feeding the parser a
// hand-crafted transcript that resembles one user turn followed by a
// tool-use loop: assistant text + tool_use, tool_result, assistant
// follow-up text. Confirms that:
//  1. assistant rows with the same message.id (three content blocks
//     all written as separate rows in the real CLI) collapse into
//     ONE AI message with parts in source order;
//  2. tool_use blocks become llmtypes.ToolCall with Input preserved
//     as a JSON string;
//  3. tool_result rows become llmtypes.ChatMessageTypeTool messages
//     with ToolCallResponse.ToolCallID linking back to the call.
func TestReadClaudeTranscriptMessagesShapesAToolUseLoop(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "11111111-2222-3333-4444-555566667777"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-fake")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")

	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	ts := turnStart.Add(1 * time.Second).Format(time.RFC3339Nano)

	// Real claude-code writes ONE JSONL row per content block. Rows
	// from the same LLM call share message.id but each carries a
	// DIFFERENT single-element content[] (one for text, one for
	// tool_use, etc.). The parser must accumulate blocks across
	// rows into a single AI MessageContent.
	asstA_thinking := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_A","content":[` +
		`{"type":"thinking","thinking":"plan: read first","signature":"sig"}` +
		`]}}`
	asstA_text := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_A","content":[` +
		`{"type":"text","text":"Reading the file."}` +
		`]}}`
	asstA_tool := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_A","content":[` +
		`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"foo.go"}}` +
		`]}}`
	// The tool_result that the CLI feeds back to the model.
	toolResult := `{"type":"user","timestamp":"` + ts + `","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"package main"}]}` +
		`]}}`
	// A second LLM call (msg_B) producing the final assistant text.
	asstB := `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"msg_B","content":[` +
		`{"type":"text","text":"Done."}` +
		`]}}`

	lines := []string{
		asstA_thinking, // thinking block — skipped (no usable parts)
		asstA_text,     // text block — creates group for msg_A
		asstA_tool,     // tool_use block — appended to msg_A's parts
		toolResult,
		asstB,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	msgs := readClaudeTranscriptMessages(sessionID, "", turnStart)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (msg_A AI, tool_result, msg_B AI); msgs=%+v", len(msgs), msgs)
	}

	// msg_A — collapsed AI message with 2 parts (text + tool_use).
	if msgs[0].Role != llmtypes.ChatMessageTypeAI {
		t.Fatalf("msgs[0].Role = %q, want AI", msgs[0].Role)
	}
	if len(msgs[0].Parts) != 2 {
		t.Fatalf("msgs[0].Parts = %d, want 2 (text + tool_use); parts=%+v", len(msgs[0].Parts), msgs[0].Parts)
	}
	if tc, ok := msgs[0].Parts[0].(llmtypes.TextContent); !ok || tc.Text != "Reading the file." {
		t.Fatalf("msgs[0].Parts[0] = %+v, want TextContent{Text:'Reading the file.'}", msgs[0].Parts[0])
	}
	tcall, ok := msgs[0].Parts[1].(llmtypes.ToolCall)
	if !ok {
		t.Fatalf("msgs[0].Parts[1] = %T, want ToolCall", msgs[0].Parts[1])
	}
	if tcall.ID != "toolu_1" {
		t.Fatalf("ToolCall.ID = %q, want toolu_1", tcall.ID)
	}
	if tcall.FunctionCall == nil || tcall.FunctionCall.Name != "Read" {
		t.Fatalf("ToolCall.FunctionCall = %+v, want Name='Read'", tcall.FunctionCall)
	}
	if !strings.Contains(tcall.FunctionCall.Arguments, `"path":"foo.go"`) {
		t.Fatalf("ToolCall.FunctionCall.Arguments = %q, want to contain path:foo.go", tcall.FunctionCall.Arguments)
	}

	// tool_result — Tool-role message linked to the call.
	if msgs[1].Role != llmtypes.ChatMessageTypeTool {
		t.Fatalf("msgs[1].Role = %q, want Tool", msgs[1].Role)
	}
	tres, ok := msgs[1].Parts[0].(llmtypes.ToolCallResponse)
	if !ok {
		t.Fatalf("msgs[1].Parts[0] = %T, want ToolCallResponse", msgs[1].Parts[0])
	}
	if tres.ToolCallID != "toolu_1" {
		t.Fatalf("ToolCallResponse.ToolCallID = %q, want toolu_1", tres.ToolCallID)
	}
	if tres.Content != "package main" {
		t.Fatalf("ToolCallResponse.Content = %q, want 'package main'", tres.Content)
	}

	// msg_B — final assistant text.
	if msgs[2].Role != llmtypes.ChatMessageTypeAI {
		t.Fatalf("msgs[2].Role = %q, want AI", msgs[2].Role)
	}
	if tc, ok := msgs[2].Parts[0].(llmtypes.TextContent); !ok || tc.Text != "Done." {
		t.Fatalf("msgs[2].Parts[0] = %+v, want TextContent{Text:'Done.'}", msgs[2].Parts[0])
	}
}

// TestReadClaudeTranscriptMessagesHonorsTurnStart proves rows before
// turnStart are filtered out, so multi-turn sessions don't leak prior
// assistant text/tool_use into the current turn's enrichment.
func TestReadClaudeTranscriptMessagesHonorsTurnStart(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "deadbeef-0000-0000-0000-000000000000"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-fake")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")

	turnStart := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	before := turnStart.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	after := turnStart.Add(1 * time.Second).Format(time.RFC3339Nano)

	lines := []string{
		`{"type":"assistant","timestamp":"` + before + `","message":{"id":"msg_OLD","content":[{"type":"text","text":"prior turn"}]}}`,
		`{"type":"assistant","timestamp":"` + after + `","message":{"id":"msg_NEW","content":[{"type":"text","text":"current turn"}]}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	msgs := readClaudeTranscriptMessages(sessionID, "", turnStart)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 (prior turn must be excluded); msgs=%+v", len(msgs), msgs)
	}
	if tc, ok := msgs[0].Parts[0].(llmtypes.TextContent); !ok || tc.Text != "current turn" {
		t.Fatalf("msgs[0].Parts[0] = %+v, want TextContent{Text:'current turn'}", msgs[0].Parts[0])
	}
}

func TestReadClaudeTranscriptMessagesRejectsNonUUIDSessionID(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	for _, sessionID := range []string{"../secret", "--help", "deadbeef-0000-0000-0000-000000000000/extra"} {
		if msgs := readClaudeTranscriptMessages(sessionID, "", time.Time{}); len(msgs) != 0 {
			t.Fatalf("session %q returned %d messages, want none", sessionID, len(msgs))
		}
	}
}

func TestCompletedAssistantResponseFromTranscriptUsesOnlyCommittedEndTurn(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "9b6a291d-04e1-41d3-b64a-fb70293db03a"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")
	turnStart := time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)
	before := turnStart.Add(-time.Second).Format(time.RFC3339Nano)
	after := turnStart.Add(time.Second).Format(time.RFC3339Nano)
	lines := []string{
		`{"type":"assistant","timestamp":"` + before + `","message":{"id":"old","stop_reason":"end_turn","content":[{"type":"text","text":"prior turn"}]}}`,
		`{"type":"assistant","timestamp":"` + after + `","message":{"id":"tool-call","stop_reason":"tool_use","content":[{"type":"text","text":"Running deeper technical checks now."}]}}`,
		`{"type":"assistant","timestamp":"` + after + `","message":{"id":"tool-call","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-1","name":"api-bridge","input":{}}]}}`,
		`{"type":"assistant","timestamp":"` + after + `","message":{"id":"final","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"done"}]}}`,
		`{"type":"assistant","timestamp":"` + after + `","message":{"id":"final","stop_reason":"end_turn","content":[{"type":"text","text":"The quality check finished.\n\n**Result: ready.**"}]}}`,
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	want := "The quality check finished.\n\n**Result: ready.**"
	got := completedAssistantResponseFromTranscript(sessionID, "", turnStart)
	if !got.Found || !got.Completed || got.Text != want {
		t.Fatalf("completed response = %+v, want found completed text %q", got, want)
	}
}

func TestCompletedAssistantResponseFromTranscriptRejectsInterruptedTurn(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "7ea267a7-9e91-4167-89ad-071ef14345bd"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")
	ts := time.Now().UTC()
	line := `{"type":"assistant","timestamp":"` + ts.Format(time.RFC3339Nano) + `","message":{"id":"active","stop_reason":"tool_use","content":[{"type":"text","text":"Running deeper technical checks now."}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	got := completedAssistantResponseFromTranscript(sessionID, "", ts.Add(-time.Second))
	if !got.Found || got.Completed || got.Text != "" {
		t.Fatalf("interrupted response = %+v, want found but incomplete without end_turn", got)
	}
}

func TestWaitForCompletedAssistantResponseFromTranscriptAllowsDelayedCommit(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "84fbf51a-54d9-4fbe-a20d-3c62dc457fe4"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")
	turnStart := time.Now().UTC().Add(-time.Second)
	initial := `{"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"id":"final","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"done"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(initial), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// This deliberately exceeds the adapter's former fixed two-second grace.
	// The pane becoming ready is not authoritative while the transcript still
	// contains only an in-progress tool_use response.
	go func() {
		time.Sleep(2100 * time.Millisecond)
		committed := `{"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"id":"final","stop_reason":"end_turn","content":[{"type":"text","text":"Hello from Video Studio."}]}}` + "\n"
		f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString(committed)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	got := waitForCompletedAssistantResponseFromTranscript(ctx, sessionID, "", turnStart)
	if !got.Found || !got.Completed || got.Text != "Hello from Video Studio." {
		t.Fatalf("completed response = %+v, want delayed committed final text", got)
	}
}

func TestWaitForCompletedAssistantResponseFromTranscriptStopsOnTurnCancellation(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	const sessionID = "21262e97-79a8-4d35-bd4c-faf1cb6acdd7"
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	transcript := filepath.Join(projectDir, sessionID+".jsonl")
	turnStart := time.Now().UTC().Add(-time.Second)
	active := `{"type":"assistant","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"id":"active","stop_reason":"tool_use","content":[{"type":"text","text":"Generating the references now."}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(active), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 125*time.Millisecond)
	defer cancel()
	started := time.Now()
	got := waitForCompletedAssistantResponseFromTranscript(ctx, sessionID, "", turnStart)
	if !got.Found || got.Completed || got.Text != "" {
		t.Fatalf("cancelled response = %+v, want found but incomplete", got)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("wait ignored turn cancellation; elapsed=%v", elapsed)
	}
}
