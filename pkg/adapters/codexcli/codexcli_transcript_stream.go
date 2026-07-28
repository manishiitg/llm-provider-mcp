package codexcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// codexInteractiveStreamTranscriptEnabled reports whether to tail Codex's
// rollout JSONL for structured streaming. Opt-in (default OFF), set per call
// via WithStreamTranscript — there is no environment-variable fallback.
func codexInteractiveStreamTranscriptEnabled(opts *llmtypes.CallOptions) bool {
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if v, ok := opts.Metadata.Custom[MetadataKeyStreamTranscript].(bool); ok {
			return v
		}
	}
	return false
}

// codexTranscriptEvent is one structured item recovered from the rollout during
// a turn: assistant text (Text set), the start of a tool call (ToolName set),
// or a tool's completion (IsToolEnd set — from mcp_tool_call_end for MCP
// bridge calls, or function_call_output/custom_tool_call_output for codex's
// native tools).
type codexTranscriptEvent struct {
	Text       string
	ToolName   string
	ToolCallID string
	Key        string // dedup key for content (row timestamp); tools dedup by ToolCallID
	// IsToolEnd distinguishes a completion row from a start row; both carry
	// ToolCallID, so this is the only way to tell them apart once mapped onto
	// the shared event shape. ToolResult is only populated for native tools —
	// codex's own event stream never includes an MCP call's result text, not
	// even in the structured transport (see codexcli_structured_adapter.go's
	// item.completed handling for mcp_tool_call, verified live against
	// codex-cli 0.145.0), so this is a real constraint, not a gap.
	IsToolEnd    bool
	ToolResult   string
	ToolDuration time.Duration
}

// codexTranscriptStreamState tails the current turn's rollout JSONL and emits
// content/tool-call-start StreamChunks as new rows are written. It resolves the
// rollout path once (mirroring codexTurnCompletionTracker) and then advances by
// byte offset. Used INSIDE waitForCodexInteractiveResponse — which returns
// before the adapter closes the StreamChan — so there is no send-on-closed-chan
// race that a detached goroutine would risk.
type codexTranscriptStreamState struct {
	turnStart   time.Time
	workingDir  string
	path        string
	offset      int64
	seenTool    map[string]bool // defensive dedup of tool starts by call_id
	seenContent map[string]bool // dedup assistant text (codex writes it as BOTH agent_message and response_item message)
	// pendingToolStarts carries each tool call's start timestamp across polls
	// (begin and end rows, or a call and its output row, can land in
	// different polls) so the matching end can compute a real duration
	// instead of reporting zero.
	pendingToolStarts map[string]time.Time
}

func newCodexTranscriptStreamState(turnStart time.Time, workingDir string) *codexTranscriptStreamState {
	return &codexTranscriptStreamState{
		turnStart:         turnStart,
		workingDir:        workingDir,
		seenTool:          map[string]bool{},
		seenContent:       map[string]bool{},
		pendingToolStarts: map[string]time.Time{},
	}
}

// poll reads any newly-appended rollout rows and emits their events on
// streamChan. Best-effort: swallows IO errors and returns on ctx cancellation.
func (s *codexTranscriptStreamState) poll(ctx context.Context, streamChan chan<- llmtypes.StreamChunk) {
	if streamChan == nil {
		return
	}
	if s.path == "" {
		s.path = findCodexRolloutForTurn(s.turnStart, s.workingDir)
		if s.path == "" {
			return // rollout not created yet — try again next tick
		}
	}
	events, next, err := readCodexTranscriptEventsFromFile(s.path, s.offset, s.turnStart, s.pendingToolStarts)
	if err != nil {
		return
	}
	s.offset = next
	for _, e := range events {
		// Defensive: emit a given tool call's start only once, keyed by
		// call_id, in case a row is ever re-observed.
		if e.ToolName != "" && e.ToolCallID != "" {
			if s.seenTool[e.ToolCallID] {
				continue
			}
			s.seenTool[e.ToolCallID] = true
		}
		// Codex writes each assistant message twice — as an event_msg
		// agent_message AND a response_item message with the same text (sometimes
		// at slightly different timestamps). The double-write is systematic, so
		// emit each distinct assistant line once, keyed by text.
		if e.ToolName == "" && e.Text != "" {
			if s.seenContent[e.Text] {
				continue
			}
			s.seenContent[e.Text] = true
		}
		chunk := codexTranscriptEventToChunk(e)
		select {
		case streamChan <- chunk:
		case <-ctx.Done():
			return
		}
	}
}

func codexTranscriptEventToChunk(e codexTranscriptEvent) llmtypes.StreamChunk {
	meta := map[string]interface{}{"codex_cli_stream_source": "transcript"}
	if e.IsToolEnd {
		return llmtypes.StreamChunk{
			Type:         llmtypes.StreamChunkTypeToolCallEnd,
			ToolCallID:   e.ToolCallID,
			ToolResult:   e.ToolResult,
			ToolDuration: e.ToolDuration,
			Metadata:     meta,
		}
	}
	if e.ToolName != "" {
		return llmtypes.StreamChunk{
			Type:       llmtypes.StreamChunkTypeToolCallStart,
			ToolName:   e.ToolName,
			ToolCallID: e.ToolCallID,
			Metadata:   meta,
		}
	}
	return llmtypes.StreamChunk{
		Type:     llmtypes.StreamChunkTypeContent,
		Content:  e.Text,
		Metadata: meta,
	}
}

// readCodexTranscriptEventsFromFile reads new rollout rows starting at byte
// `offset`, returning the structured events plus the offset to resume from. Only
// consumes up to the last complete (newline-terminated) line, holding back a
// partial trailing line. Rows older than turnStart are skipped (but consumed).
// Parses the same rollout schema as readCodexTranscriptMessagesFile:
// response_item → message(assistant output_text) / function_call /
// custom_tool_call / function_call_output / custom_tool_call_output, plus
// event_msg → mcp_tool_call_begin/_end for MCP bridge calls. pendingToolStarts
// carries each call's start timestamp across calls (a call and its
// completion row can land in different polls) so a matching completion can
// compute a real duration instead of reporting zero; pass the same map back
// in on every call.
func readCodexTranscriptEventsFromFile(path string, offset int64, turnStart time.Time, pendingToolStarts map[string]time.Time) ([]codexTranscriptEvent, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, offset, err
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return nil, offset, nil
	}
	consumed := data[:lastNL+1]
	nextOffset := offset + int64(len(consumed))

	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type invocation struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	}
	type rolloutPayload struct {
		Type string `json:"type"`
		// response_item: message
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`
		// response_item: function_call / custom_tool_call
		Name   string `json:"name"`
		CallID string `json:"call_id"`
		// response_item: function_call_output / custom_tool_call_output
		Output string `json:"output"`
		// event_msg: agent_message
		Message string `json:"message"`
		// event_msg: mcp_tool_call_begin / _end
		Invocation *invocation `json:"invocation"`
	}
	type ev struct {
		Type      string         `json:"type"` // "event_msg" | "response_item"
		Timestamp string         `json:"timestamp"`
		Payload   rolloutPayload `json:"payload"`
	}

	var events []codexTranscriptEvent
	for _, raw := range bytes.Split(consumed, []byte{'\n'}) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var e ev
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		var rowTime time.Time
		if e.Timestamp != "" {
			rowTime, _ = time.Parse(time.RFC3339Nano, e.Timestamp)
		}
		if !turnStart.IsZero() && !rowTime.IsZero() && rowTime.Before(turnStart) {
			continue
		}
		switch e.Type {
		case "event_msg":
			// Current Codex (0.144+) records assistant prose as agent_message and
			// MCP calls as mcp_tool_call_begin/_end (not as response_item rows).
			switch e.Payload.Type {
			case "agent_message":
				if e.Payload.Message != "" {
					events = append(events, codexTranscriptEvent{Text: e.Payload.Message, Key: e.Timestamp})
				}
			case "mcp_tool_call_begin":
				if e.Payload.Invocation != nil && e.Payload.Invocation.Tool != "" {
					events = append(events, codexTranscriptEvent{
						ToolName:   e.Payload.Invocation.Tool,
						ToolCallID: e.Payload.CallID,
					})
					// Recorded even when rowTime is zero (missing/unparseable
					// timestamp): the matching _end below treats a zero start
					// as "not measured" and reports 0 rather than a
					// fabricated duration.
					if pendingToolStarts != nil && e.Payload.CallID != "" {
						pendingToolStarts[e.Payload.CallID] = rowTime
					}
				}
			case "mcp_tool_call_end":
				if e.Payload.CallID != "" {
					events = append(events, codexTranscriptEvent{
						IsToolEnd:  true,
						ToolCallID: e.Payload.CallID,
						// No ToolResult: codex's own event stream never
						// includes an MCP call's result text (see the
						// codexTranscriptEvent doc comment).
						ToolDuration: consumeCodexPendingStart(pendingToolStarts, e.Payload.CallID, rowTime),
					})
				}
			}
		case "response_item":
			// Also handle the response_item form (older/other Codex builds, and
			// codex's own shell tool custom_tool_call name:"exec").
			switch e.Payload.Type {
			case "message":
				if e.Payload.Role != "assistant" {
					continue
				}
				for _, b := range e.Payload.Content {
					if b.Type != "output_text" && b.Type != "text" {
						continue
					}
					if b.Text == "" {
						continue
					}
					events = append(events, codexTranscriptEvent{Text: b.Text, Key: e.Timestamp})
				}
			case "function_call", "custom_tool_call":
				events = append(events, codexTranscriptEvent{ToolName: e.Payload.Name, ToolCallID: e.Payload.CallID})
				if pendingToolStarts != nil && e.Payload.CallID != "" {
					pendingToolStarts[e.Payload.CallID] = rowTime
				}
			case "function_call_output", "custom_tool_call_output":
				if e.Payload.CallID != "" {
					events = append(events, codexTranscriptEvent{
						IsToolEnd:    true,
						ToolCallID:   e.Payload.CallID,
						ToolResult:   e.Payload.Output,
						ToolDuration: consumeCodexPendingStart(pendingToolStarts, e.Payload.CallID, rowTime),
					})
				}
			}
		}
	}
	return events, nextOffset, nil
}

// consumeCodexPendingStart looks up and removes callID's recorded start time,
// returning the elapsed duration to endTime, or 0 if the start is missing,
// zero, or the end row's own timestamp couldn't be parsed — "not measured",
// never a fabricated value.
func consumeCodexPendingStart(pendingToolStarts map[string]time.Time, callID string, endTime time.Time) time.Duration {
	if pendingToolStarts == nil {
		return 0
	}
	start, ok := pendingToolStarts[callID]
	if !ok {
		return 0
	}
	delete(pendingToolStarts, callID)
	if start.IsZero() || endTime.IsZero() || !endTime.After(start) {
		return 0
	}
	return endTime.Sub(start)
}
