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

// museScreenStreamPollInterval paces raw pane snapshots. Snapshots are
// lossy by nature (only changes emit), so a coarse ticker is fine.
const museScreenStreamPollInterval = 2 * time.Second

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
			out = append(out, llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeReasoning, Content: text, Metadata: map[string]interface{}{"muse_cli_stream_source": "transcript", "presentation": "assistant_update"}})
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
// construction so prior turns' history never replays. When screenEnabled it
// also snapshots the live pane on a coarse ticker, emitting Terminal chunks
// only on change (the mode1 raw-terminal view) — same split as cursor's
// transcript + tmux-screen flags.
type museTranscriptStreamState struct {
	logPath           string
	tmuxName          string
	transcriptEnabled bool
	screenEnabled     bool
	lastSeq           int64
	seenTool          map[string]bool
	endedTool         map[string]bool
	toolStartedAt     map[string]time.Time
	lastScreen        string
	done              chan struct{}
}

// newMuseTranscriptStreamState leaves lastScreen at its zero value, exactly
// like cursor-cli's streamCursorTerminalSnapshot (var lastTerminalSnapshot
// string): no priming/discard step at all, so the very first capture always
// qualifies as "changed" and emits. A prior version of this code treated the
// first captured sample as a "priming" throwaway meant to avoid replaying
// the pre-turn pane -- but a turn fast enough to finish before
// museScreenStreamPollInterval's first tick made that discarded sample the
// ALREADY-COMPLETE pane, not the pre-turn one, permanently emptying the
// "main terminal" UI panel for that turn despite a live tmux session backing
// it (observed live 2026-09-11). Matching cursor's simpler, priming-free
// pattern fixes this the same way cursor never had the bug.
func newMuseTranscriptStreamState(logPath, tmuxName string, transcriptEnabled, screenEnabled bool) *museTranscriptStreamState {
	s := &museTranscriptStreamState{
		logPath: logPath, tmuxName: tmuxName,
		transcriptEnabled: transcriptEnabled, screenEnabled: screenEnabled,
		seenTool:  map[string]bool{},
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
	screenTicker := time.NewTicker(museScreenStreamPollInterval)
	defer screenTicker.Stop()
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			if s.transcriptEnabled {
				s.poll(context.Background(), streamChan) // final flush (single-threaded now)
			}
			s.pollScreen(streamChan) // final flush: catches any change since the last periodic tick
			return
		case <-ticker.C:
			if s.transcriptEnabled {
				s.poll(ctx, streamChan)
			}
		case <-screenTicker.C:
			s.pollScreen(streamChan)
		}
	}
}

// pollScreen captures the live pane and emits it as a Terminal chunk when
// changed since the last snapshot (seeded at construction, see
// newMuseTranscriptStreamState). Dropped on backpressure (snapshots are
// lossy) and silent on capture errors — screen streaming must never fail a
// turn.
func (s *museTranscriptStreamState) pollScreen(streamChan chan<- llmtypes.StreamChunk) {
	if !s.screenEnabled || strings.TrimSpace(s.tmuxName) == "" || streamChan == nil {
		return
	}
	pane, err := museTmuxCapturePane(context.Background(), s.tmuxName)
	if err != nil {
		return
	}
	snapshot := strings.TrimRight(pane, "\n")
	if strings.TrimSpace(snapshot) == "" || snapshot == s.lastScreen {
		return
	}
	s.lastScreen = snapshot
	select {
	case streamChan <- llmtypes.StreamChunk{
		Type:    llmtypes.StreamChunkTypeTerminal,
		Content: snapshot,
		Metadata: map[string]interface{}{
			"tmux_session":             s.tmuxName,
			"muse_cli_stream_source":   "tmux-screen",
			"muse_interactive_session": s.tmuxName,
		},
	}:
	default:
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
