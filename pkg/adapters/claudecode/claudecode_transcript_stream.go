package claudecode

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

// claudeTranscriptStreamPollInterval is how often the transcript tailer checks
// the JSONL sidecar for newly-appended blocks during a turn. Matches the pane
// poll cadence so structured content and terminal snapshots arrive in step.
const claudeTranscriptStreamPollInterval = 250 * time.Millisecond

// claudeInteractiveStreamTranscriptEnabled reports whether to tail the CLI's
// JSONL transcript for structured streaming. Opt-in (default OFF): the existing
// pane-snapshot stream is unaffected when this is disabled. Set per call via
// WithStreamTranscript — there is no environment-variable fallback.
func claudeInteractiveStreamTranscriptEnabled(opts *llmtypes.CallOptions) bool {
	if opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if v, ok := opts.Metadata.Custom[MetadataKeyStreamTranscript].(bool); ok {
			return v
		}
	}
	return false
}

// claudeTranscriptEvent is one structured item recovered from the transcript
// during a turn: assistant text (Text set), the start of a tool call (ToolName
// set), or a tool's result (IsToolEnd set). Kept minimal — this is the mid-turn
// streaming signal, not the authoritative end-of-turn reconstruction
// (readClaudeTranscriptMessages).
type claudeTranscriptEvent struct {
	Text       string
	Reasoning  string
	ToolName   string
	ToolCallID string
	// ToolArgs is the call's arguments, carried onto the START chunk (the only
	// chunk with a field for them — ToolCallEnd has none). Without this the
	// mcpagent layer receives a tool call with an empty body, so a product can
	// render only the bare tool name and an agentic reviewer cannot tell a
	// bridge-routed call from raw native shell use.
	ToolArgs string
	// IsToolEnd distinguishes a tool_result row from a tool_use row; both
	// carry ToolCallID, so this is the only way to tell them apart once
	// mapped onto the shared event shape.
	IsToolEnd    bool
	ToolResult   string
	ToolDuration time.Duration
}

// streamClaudeTranscript tails the claude-code JSONL transcript for `sessionID`
// and emits assistant-text and tool-call-start StreamChunks onto streamChan as
// each block is written, until ctx is cancelled. Best-effort and additive: it
// never affects the turn's final response (built from the pane parse) and
// swallows all IO errors. Designed for design-first UIs that never render the
// terminal pane — the caller typically consumes these Content chunks and
// ignores the terminal-snapshot chunks.
//
// It relies on the transcript being append-live (claude-code writes one JSONL
// row per content block during the turn) and on `sessionID` (a pre-generated
// UUID passed to the CLI) being known before the turn starts.
func streamClaudeTranscript(ctx context.Context, sessionID, workingDir string, turnStart time.Time, streamChan chan<- llmtypes.StreamChunk) {
	if streamChan == nil || !isClaudeTranscriptSessionID(sessionID) {
		return
	}
	ticker := time.NewTicker(claudeTranscriptStreamPollInterval)
	defer ticker.Stop()
	tailer := newClaudeTranscriptTailer(sessionID, workingDir)
	defer tailer.Close()

	// A tool_use row and its tool_result row can land in different polls (the
	// CLI writes the result once the tool actually finishes, which is the
	// whole point of a duration). This carries each start's transcript
	// timestamp across polls so the matching end can compute a real duration
	// instead of reporting zero.
	pendingToolStarts := map[string]time.Time{}
	for {
		events, err := tailer.Read(time.Now(), turnStart, pendingToolStarts)
		if err == nil {
			for _, e := range events {
				chunk := transcriptEventToChunk(sessionID, e)
				select {
				case streamChan <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// claudeTranscriptTailer owns transcript discovery and the open file for one
// streaming turn. Normal polling performs only a seek+read on this file. Before
// the transcript exists, path discovery backs off; the expensive compatibility
// glob is delayed when workingDir is known, then runs at most every 30s.
type claudeTranscriptTailer struct {
	sessionID      string
	workingDir     string
	path           string
	file           *os.File
	offset         int64
	nextResolveAt  time.Time
	resolveBackoff time.Duration
	nextFallbackAt time.Time
}

func newClaudeTranscriptTailer(sessionID, workingDir string) *claudeTranscriptTailer {
	return &claudeTranscriptTailer{
		sessionID:      sessionID,
		workingDir:     workingDir,
		resolveBackoff: claudeTranscriptStreamPollInterval,
	}
}

func (t *claudeTranscriptTailer) Close() {
	if t == nil || t.file == nil {
		return
	}
	_ = t.file.Close()
	t.file = nil
}

func (t *claudeTranscriptTailer) Read(now, turnStart time.Time, pendingToolStarts map[string]time.Time) ([]claudeTranscriptEvent, error) {
	if t == nil {
		return nil, nil
	}
	if err := t.ensureOpen(now); err != nil || t.file == nil {
		return nil, err
	}
	events, next, err := readClaudeTranscriptEventsFromOpenFile(t.file, t.offset, turnStart, pendingToolStarts)
	if err != nil {
		t.Close()
		forgetClaudeTranscriptPath(t.sessionID, t.path)
		t.path = ""
		t.scheduleResolve(now)
		return nil, err
	}
	t.offset = next
	return events, nil
}

func (t *claudeTranscriptTailer) ensureOpen(now time.Time) error {
	if t.file != nil {
		return nil
	}
	if !t.nextResolveAt.IsZero() && now.Before(t.nextResolveAt) {
		return nil
	}

	allowFallback := false
	if t.nextFallbackAt.IsZero() {
		// A known working directory is the normal case. Give Claude two seconds
		// to create its directly-addressable transcript before paying for the
		// compatibility scan. Callers without a working directory retain one
		// immediate fallback for legacy use.
		allowFallback = strings.TrimSpace(t.workingDir) == ""
		t.nextFallbackAt = now.Add(2 * time.Second)
	} else if !now.Before(t.nextFallbackAt) {
		allowFallback = true
		t.nextFallbackAt = now.Add(30 * time.Second)
	}
	path, err := resolveClaudeTranscriptPath(t.sessionID, t.workingDir, allowFallback)
	if err != nil || path == "" {
		t.scheduleResolve(now)
		return err
	}
	f, err := claudeTranscriptOpen(path)
	if err != nil {
		if os.IsNotExist(err) {
			forgetClaudeTranscriptPath(t.sessionID, path)
		}
		t.scheduleResolve(now)
		return err
	}
	t.path = path
	t.file = f
	t.resolveBackoff = claudeTranscriptStreamPollInterval
	t.nextResolveAt = time.Time{}
	return nil
}

func (t *claudeTranscriptTailer) scheduleResolve(now time.Time) {
	if t.resolveBackoff <= 0 {
		t.resolveBackoff = claudeTranscriptStreamPollInterval
	}
	t.nextResolveAt = now.Add(t.resolveBackoff)
	t.resolveBackoff *= 2
	if t.resolveBackoff > 2*time.Second {
		t.resolveBackoff = 2 * time.Second
	}
}

func transcriptEventToChunk(sessionID string, e claudeTranscriptEvent) llmtypes.StreamChunk {
	meta := map[string]interface{}{
		"claude_code_session_id":    sessionID,
		"claude_code_stream_source": "transcript",
	}
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
			ToolArgs:   e.ToolArgs,
			Metadata:   meta,
		}
	}
	if e.Reasoning != "" {
		return llmtypes.StreamChunk{
			Type:     llmtypes.StreamChunkTypeReasoning,
			Content:  e.Reasoning,
			Metadata: meta,
		}
	}
	return llmtypes.StreamChunk{
		Type:     llmtypes.StreamChunkTypeContent,
		Content:  e.Text,
		Metadata: meta,
	}
}

// readClaudeTranscriptEventsFromFile is the file-level core, split out so tests
// can drive it against a growing fixture without a real ~/.claude tree.
func readClaudeTranscriptEventsFromFile(path string, offset int64, turnStart time.Time, pendingToolStarts map[string]time.Time) ([]claudeTranscriptEvent, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	return readClaudeTranscriptEventsFromOpenFile(f, offset, turnStart, pendingToolStarts)
}

func readClaudeTranscriptEventsFromOpenFile(f *os.File, offset int64, turnStart time.Time, pendingToolStarts map[string]time.Time) ([]claudeTranscriptEvent, int64, error) {
	// Seek even when offset is zero. A previous poll may have read an
	// incomplete first line to EOF without advancing the logical offset; the
	// next poll must reread that partial line together with its appended tail.
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		// No complete line appended yet — hold the offset and wait.
		return nil, offset, nil
	}
	consumed := data[:lastNL+1]
	nextOffset := offset + int64(len(consumed))

	var events []claudeTranscriptEvent
	for _, raw := range bytes.Split(consumed, []byte{'\n'}) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var e struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Type != "assistant" && e.Type != "user" {
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
		case "assistant":
			var am struct {
				Content []claudeAssistantContentBlock `json:"content"`
			}
			if err := json.Unmarshal(e.Message, &am); err != nil {
				continue
			}
			// Thinking is deliberately not part of the replayed conversation
			// history, but it is a first-class structured progress stream.
			// Text/tool blocks still use the common reconstruction mapping.
			for _, block := range am.Content {
				if block.Type == "thinking" && strings.TrimSpace(block.Thinking) != "" {
					events = append(events, claudeTranscriptEvent{Reasoning: strings.TrimSpace(block.Thinking)})
				}
			}
			// Reuse the same block→parts mapping the end-of-turn reader uses,
			// so streaming and final reconstruction never diverge.
			for _, p := range assistantBlocksToParts(am.Content) {
				switch v := p.(type) {
				case llmtypes.TextContent:
					if v.Text != "" {
						events = append(events, claudeTranscriptEvent{Text: v.Text})
					}
				case llmtypes.ToolCall:
					name, args := "", ""
					if v.FunctionCall != nil {
						name = v.FunctionCall.Name
						args = v.FunctionCall.Arguments
					}
					events = append(events, claudeTranscriptEvent{ToolName: name, ToolCallID: v.ID, ToolArgs: args})
					// Recorded even when rowTime is zero (missing/unparseable
					// timestamp): the matching tool_result below treats a
					// zero start as "not measured" and reports 0 rather than
					// a fabricated duration.
					if pendingToolStarts != nil && v.ID != "" {
						pendingToolStarts[v.ID] = rowTime
					}
				}
			}

		case "user":
			// Claude feeds a tool's result back to the model as a LATER
			// "user"-role row (verified against the same CLI this adapter's
			// structured transport drives — see claudecode_structured_adapter.go).
			// This is the transcript's only record of that result and its
			// real duration; without it, a tmux-transport tool call has a
			// start and nothing else.
			var um struct {
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(e.Message, &um); err != nil {
				continue
			}
			var blocks []struct {
				Type      string          `json:"type"`
				ToolUseID string          `json:"tool_use_id"`
				Content   json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(um.Content, &blocks); err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_result" || b.ToolUseID == "" {
					continue
				}
				var duration time.Duration
				if pendingToolStarts != nil {
					if start, ok := pendingToolStarts[b.ToolUseID]; ok {
						if !start.IsZero() && !rowTime.IsZero() && rowTime.After(start) {
							duration = rowTime.Sub(start)
						}
						delete(pendingToolStarts, b.ToolUseID)
					}
				}
				events = append(events, claudeTranscriptEvent{
					IsToolEnd:    true,
					ToolCallID:   b.ToolUseID,
					ToolResult:   flattenToolResultContent(b.Content),
					ToolDuration: duration,
				})
			}
		}
	}
	return events, nextOffset, nil
}
