package codexcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// codexInteractiveStreamTranscriptEnabled reports whether to tail Codex's
// rollout JSONL for structured streaming. Opt-in (default OFF): the existing
// pane-snapshot stream is unaffected when this is disabled.
func codexInteractiveStreamTranscriptEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvCodexInteractiveStreamTranscript))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// codexTranscriptEvent is one structured item recovered from the rollout during
// a turn: assistant text (Text set) or the start of a tool call (ToolName set).
type codexTranscriptEvent struct {
	Text       string
	ToolName   string
	ToolCallID string
	Key        string // dedup key for content (row timestamp); tools dedup by ToolCallID
}

// codexTranscriptStreamState tails the current turn's rollout JSONL and emits
// content/tool-call-start StreamChunks as new rows are written. It resolves the
// rollout path once (mirroring codexTurnCompletionTracker) and then advances by
// byte offset. Used INSIDE waitForCodexInteractiveResponse — which returns
// before the adapter closes the StreamChan — so there is no send-on-closed-chan
// race that a detached goroutine would risk.
type codexTranscriptStreamState struct {
	turnStart  time.Time
	workingDir string
	path       string
	offset      int64
	seenTool    map[string]bool // dedup tool starts by call_id (begin+end are two rows)
	seenContent map[string]bool // dedup assistant text (codex writes it as BOTH agent_message and response_item message)
}

func newCodexTranscriptStreamState(turnStart time.Time, workingDir string) *codexTranscriptStreamState {
	return &codexTranscriptStreamState{turnStart: turnStart, workingDir: workingDir, seenTool: map[string]bool{}, seenContent: map[string]bool{}}
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
	events, next, err := readCodexTranscriptEventsFromFile(s.path, s.offset, s.turnStart)
	if err != nil {
		return
	}
	s.offset = next
	for _, e := range events {
		// A tool call can appear twice (mcp_tool_call_begin + _end); emit its
		// start only once, keyed by call_id.
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
// custom_tool_call; tool outputs (results) are intentionally not streamed.
func readCodexTranscriptEventsFromFile(path string, offset int64, turnStart time.Time) ([]codexTranscriptEvent, int64, error) {
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
		if !turnStart.IsZero() && e.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
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
			case "mcp_tool_call_begin", "mcp_tool_call_end":
				if e.Payload.Invocation != nil && e.Payload.Invocation.Tool != "" {
					events = append(events, codexTranscriptEvent{
						ToolName:   e.Payload.Invocation.Tool,
						ToolCallID: e.Payload.CallID,
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
			}
		}
	}
	return events, nextOffset, nil
}
