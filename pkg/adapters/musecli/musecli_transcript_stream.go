package musecli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/toolclock"
)

// Transcript streaming for the muse tmux lane, mirroring cursor's
// cursorcli_transcript_stream.go: without it a tmux turn emits no content
// or tool events at all (the model works invisibly — found live extending
// the layer-2 multi-turn cert to muse: turn 1 answered from native tools
// with tools=0 content=0). Opt-in per call via WithStreamTranscript; the
// orchestrator sets it exactly when it streams (enableStreaming), same as
// cursor.
//
// Source: the native session.jsonl the turn's TUI appends. Records carry a
// monotonically increasing `sequence`, so the tailer tracks one int64 and
// can never replay history (cursor's SparkQuill lesson) or miss a record.
// assistant_message_committed carries full assistant text, not deltas — one
// Content chunk per commit. Tool pairing mirrors the exec lane's pinned
// discipline (TestMuseToolStreamChunksPair): every start pairs with exactly
// one end, joined on call_id.
const museTranscriptStreamPollInterval = 400 * time.Millisecond

// museTranscriptStreamMeta marks chunks synthesized from the native log so
// consumers can tell them apart from live wire chunks.
var museTranscriptStreamMeta = map[string]interface{}{"muse_cli_stream_source": "transcript"}

// museTranscriptEvent is the event-shaped record inside a runtime.session
// payload (assistant commits, reasoning deltas, result batches).
type museTranscriptEvent struct {
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	ToolCalls []struct {
		CallID string `json:"call_id"`
		Name   string `json:"name"`
		Args   string `json:"args"`
	} `json:"tool_calls"`
	Results []struct {
		ToolCallID string `json:"tool_call_id"`
		Text       string `json:"text"`
	} `json:"results"`
}

// museTranscriptRecord is the record-shaped tool effect inside a
// tool_batch_effect payload (started/terminal, no result text).
type museTranscriptRecord struct {
	Kind     string `json:"kind"`
	CallID   string `json:"call_id"`
	ToolName string `json:"tool_name"`
}

// museTranscriptLineToChunks maps one log line to stream chunks. Pure and
// unit-tested — no filesystem, no tmux. seenTool dedups starts by call_id
// (assistant_tool_calls_committed carries name+args and wins ties; the
// sparser tool_batch.effect.started is fallback only). endedTool guarantees
// one end per start: tool_result_batch_committed (real result text) wins,
// tool_batch.effect.terminal (no text) closes only starts that never got a
// result batch.
func museTranscriptLineToChunks(line string, seenTool, endedTool map[string]bool, toolStartedAt map[string]time.Time) []llmtypes.StreamChunk {
	var env struct {
		PayloadType string          `json:"payload_type"`
		Payload     json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil || len(env.Payload) == 0 {
		return nil
	}
	var shell struct {
		Event  museTranscriptEvent  `json:"event"`
		Record museTranscriptRecord `json:"record"`
	}
	if err := json.Unmarshal(env.Payload, &shell); err != nil {
		return nil
	}
	evt, rec := shell.Event, shell.Record
	var out []llmtypes.StreamChunk
	emitStart := func(callID, name, args string) {
		callID = strings.TrimSpace(callID)
		if callID == "" || seenTool[callID] {
			return
		}
		seenTool[callID] = true
		if toolStartedAt != nil {
			toolStartedAt[callID] = time.Now()
		}
		out = append(out, llmtypes.StreamChunk{
			Type: llmtypes.StreamChunkTypeToolCallStart, ToolName: name,
			ToolCallID: callID, ToolArgs: args, Metadata: museTranscriptStreamMeta,
		})
	}
	emitEnd := func(callID, result string) {
		callID = strings.TrimSpace(callID)
		if callID == "" || endedTool[callID] {
			return
		}
		endedTool[callID] = true
		out = append(out, llmtypes.StreamChunk{
			Type: llmtypes.StreamChunkTypeToolCallEnd, ToolCallID: callID,
			ToolResult: result, ToolDuration: toolclock.Elapsed(toolStartedAt, callID),
			Metadata: museTranscriptStreamMeta,
		})
	}
	switch evt.Kind {
	case "assistant_message_committed":
		if strings.TrimSpace(evt.Text) != "" {
			out = append(out, llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: evt.Text, Metadata: museTranscriptStreamMeta})
		}
	case "assistant_tool_calls_committed":
		for _, tc := range evt.ToolCalls {
			emitStart(tc.CallID, tc.Name, tc.Args)
		}
	case "reasoning_summary_delta":
		if text := strings.TrimSpace(evt.Text); text != "" && !museStreamPlumbing(text) {
			out = append(out, llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeReasoning, Content: text, Metadata: museTranscriptStreamMeta})
		}
	case "tool_result_batch_committed":
		for _, r := range evt.Results {
			emitEnd(r.ToolCallID, r.Text)
		}
	}
	switch rec.Kind {
	case "started":
		emitStart(rec.CallID, rec.ToolName, "")
	case "terminal":
		// No result text here; close only starts the result batch never
		// closed, so an end always exists but never doubles.
		if id := strings.TrimSpace(rec.CallID); id != "" && seenTool[id] && !endedTool[id] {
			emitEnd(id, "")
		}
	}
	return out
}

// museTranscriptStreamState tails one turn's session.jsonl by sequence and
// emits new records as chunks. lastSeq primes to the file's current max at
// construction so prior turns' history never replays.
type museTranscriptStreamState struct {
	logPath       string
	lastSeq       int64
	seenTool      map[string]bool
	endedTool     map[string]bool
	toolStartedAt map[string]time.Time
	done          chan struct{}
}

func newMuseTranscriptStreamState(logPath string) *museTranscriptStreamState {
	s := &museTranscriptStreamState{
		logPath: logPath, seenTool: map[string]bool{},
		endedTool: map[string]bool{}, toolStartedAt: map[string]time.Time{},
		done: make(chan struct{}),
	}
	s.lastSeq = museTranscriptMaxSequence(logPath)
	return s
}

func museTranscriptMaxSequence(logPath string) int64 {
	raw, err := os.ReadFile(logPath) //nolint:gosec // G304: adapter-resolved session log path
	if err != nil {
		return 0
	}
	var max int64
	for _, line := range strings.Split(string(raw), "\n") {
		var env struct {
			Sequence int64 `json:"sequence"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		if env.Sequence > max {
			max = env.Sequence
		}
	}
	return max
}

// run polls until ctx is cancelled, doing one final flush on stop to catch
// records committed right at the end of the turn, then closes done so the
// turn can return knowing every chunk made the channel. The adapter never
// closes StreamChan itself (caller-owned, exec-lane precedent).
func (s *museTranscriptStreamState) run(ctx context.Context, streamChan chan<- llmtypes.StreamChunk) {
	if streamChan == nil {
		close(s.done)
		return
	}
	ticker := time.NewTicker(museTranscriptStreamPollInterval)
	defer ticker.Stop()
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			s.poll(context.Background(), streamChan) // final flush (single-threaded now)
			return
		case <-ticker.C:
			s.poll(ctx, streamChan)
		}
	}
}

func (s *museTranscriptStreamState) poll(ctx context.Context, streamChan chan<- llmtypes.StreamChunk) {
	raw, err := os.ReadFile(s.logPath) //nolint:gosec // G304: adapter-resolved session log path
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env struct {
			Sequence int64 `json:"sequence"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil || env.Sequence <= s.lastSeq {
			continue
		}
		s.lastSeq = env.Sequence
		for _, chunk := range museTranscriptLineToChunks(line, s.seenTool, s.endedTool, s.toolStartedAt) {
			select {
			case streamChan <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}
}
