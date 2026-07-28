package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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
	ToolName   string
	ToolCallID string
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
func streamClaudeTranscript(ctx context.Context, sessionID string, turnStart time.Time, streamChan chan<- llmtypes.StreamChunk) {
	if streamChan == nil || !isClaudeTranscriptSessionID(sessionID) {
		return
	}
	ticker := time.NewTicker(claudeTranscriptStreamPollInterval)
	defer ticker.Stop()

	var offset int64
	// A tool_use row and its tool_result row can land in different polls (the
	// CLI writes the result once the tool actually finishes, which is the
	// whole point of a duration). This carries each start's transcript
	// timestamp across polls so the matching end can compute a real duration
	// instead of reporting zero.
	pendingToolStarts := map[string]time.Time{}
	for {
		events, next, err := readClaudeTranscriptEventsSince(sessionID, offset, turnStart, pendingToolStarts)
		if err == nil {
			offset = next
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
			Metadata:   meta,
		}
	}
	return llmtypes.StreamChunk{
		Type:     llmtypes.StreamChunkTypeContent,
		Content:  e.Text,
		Metadata: meta,
	}
}

// readClaudeTranscriptEventsSince reads new transcript rows for `sessionID`
// starting at byte `offset`, returning the structured events plus the offset to
// resume from next time. It only consumes up to the last complete
// (newline-terminated) line, holding back any partial trailing line so a row
// being written mid-poll is never parsed half-formed. Rows older than turnStart
// are skipped (but still consumed) so a resumed session's prior turns don't
// replay. Best-effort: any error leaves the offset unadvanced.
func readClaudeTranscriptEventsSince(sessionID string, offset int64, turnStart time.Time, pendingToolStarts map[string]time.Time) ([]claudeTranscriptEvent, int64, error) {
	if !isClaudeTranscriptSessionID(sessionID) {
		return nil, offset, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, offset, err
	}
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		// Transcript not created yet (first turn of a fresh session) — try again
		// next tick.
		return nil, offset, nil
	}
	return readClaudeTranscriptEventsFromFile(matches[0], offset, turnStart, pendingToolStarts)
}

// readClaudeTranscriptEventsFromFile is the file-level core, split out so tests
// can drive it against a growing fixture without a real ~/.claude tree.
func readClaudeTranscriptEventsFromFile(path string, offset int64, turnStart time.Time, pendingToolStarts map[string]time.Time) ([]claudeTranscriptEvent, int64, error) {
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
			// Reuse the same block→parts mapping the end-of-turn reader uses,
			// so streaming and final reconstruction never diverge.
			for _, p := range assistantBlocksToParts(am.Content) {
				switch v := p.(type) {
				case llmtypes.TextContent:
					if v.Text != "" {
						events = append(events, claudeTranscriptEvent{Text: v.Text})
					}
				case llmtypes.ToolCall:
					name := ""
					if v.FunctionCall != nil {
						name = v.FunctionCall.Name
					}
					events = append(events, claudeTranscriptEvent{ToolName: name, ToolCallID: v.ID})
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
