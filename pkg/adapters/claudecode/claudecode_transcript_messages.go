package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// readClaudeTranscriptMessages reconstructs the assistant's internal
// tool-use loop from the same sidecar JSONL that readClaudeTranscriptUsage
// reads tokens from. It returns a chronologically ordered
// []llmtypes.MessageContent representing what happened INSIDE the
// claude-code CLI during the turn — text chunks, tool_use calls, and
// the tool_result responses fed back to the model — so callers can
// splice that trail into a workflow conversation log alongside the
// outer user-prompt → final-assistant-text shape that the LLM
// abstraction already exposes.
//
// Shape mapping (claude-code JSONL → llmtypes):
//
//	{type:"assistant", message.content: [{type:"text", text}]}
//	  → {Role: AI, Parts: [TextContent]}
//	{type:"assistant", message.content: [{type:"tool_use", id, name, input}]}
//	  → {Role: AI, Parts: [ToolCall{ID, FunctionCall:{Name, Arguments=json(input)}}]}
//	{type:"user",     message.content: [{type:"tool_result", tool_use_id, content}]}
//	  → {Role: Tool,Parts: [ToolCallResponse{ToolCallID, Content}]}
//
// Claude writes one JSONL row per content BLOCK (text chunk or
// tool_use). Rows from the same LLM call share a message.id. We GROUP
// by message.id so that a single assistant LLM call producing
// "text + tool_use + tool_use" returns as ONE MessageContent with
// three Parts — matching the shape the Anthropic Messages API itself
// would have returned.
//
// Returns nil/empty on any error or if the transcript is missing.
// Best-effort by design — never surfaces IO errors to the caller.
func readClaudeTranscriptMessages(sessionID string, turnStart time.Time) []llmtypes.MessageContent {
	if !isClaudeTranscriptSessionID(sessionID) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	type ev struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	type assistantMessage struct {
		ID      string                        `json:"id"`
		Content []claudeAssistantContentBlock `json:"content"`
	}
	type userContentBlock struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	}
	type userMessage struct {
		Content json.RawMessage `json:"content"`
	}

	// Claude-code writes ONE row per content block of an LLM call.
	// All rows from the same call share message.id; each row carries
	// exactly ONE content block (text, thinking, tool_use, ...). To
	// reconstruct the call as a single AI MessageContent we group by
	// message.id, accumulate blocks across rows, and emit a combined
	// MessageContent at the first sighting (preserving chronological
	// position in `out`).
	var out []llmtypes.MessageContent
	type pendingGroup struct {
		index int // position in out where this group's MessageContent lives
	}
	groups := make(map[string]*pendingGroup)

	for scanner.Scan() {
		var e ev
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if !turnStart.IsZero() && e.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
		}

		switch e.Type {
		case "assistant":
			var am assistantMessage
			if err := json.Unmarshal(e.Message, &am); err != nil {
				continue
			}
			parts := assistantBlocksToParts(am.Content)
			if len(parts) == 0 {
				// thinking blocks, redacted_reasoning, etc. — skip
				// without registering the group; if a later row of
				// the same message.id carries usable content, it
				// still creates the group then.
				continue
			}
			if am.ID == "" {
				// No id to group by: emit standalone.
				out = append(out, llmtypes.MessageContent{
					Role:  llmtypes.ChatMessageTypeAI,
					Parts: parts,
				})
				continue
			}
			if g, ok := groups[am.ID]; ok {
				// Append additional blocks from later rows of the
				// same LLM call to the already-emitted group.
				combined := append(out[g.index].Parts, parts...) //nolint:gocritic
				out[g.index].Parts = combined
				continue
			}
			groups[am.ID] = &pendingGroup{index: len(out)}
			out = append(out, llmtypes.MessageContent{
				Role:  llmtypes.ChatMessageTypeAI,
				Parts: parts,
			})

		case "user":
			var um userMessage
			if err := json.Unmarshal(e.Message, &um); err != nil {
				continue
			}
			// content is either a string (typed user text) or an array
			// of content blocks (tool_results when the CLI feeds tool
			// outputs back into the model). We only care about
			// tool_results here — typed user text is already in the
			// outer conversation_history.
			var blocks []userContentBlock
			if err := json.Unmarshal(um.Content, &blocks); err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				out = append(out, llmtypes.MessageContent{
					Role: llmtypes.ChatMessageTypeTool,
					Parts: []llmtypes.ContentPart{
						llmtypes.ToolCallResponse{
							ToolCallID: b.ToolUseID,
							Content:    flattenToolResultContent(b.Content),
						},
					},
				})
			}
		}
	}
	return out
}

// fullAssistantProseFromTranscript reconstructs the COMPLETE assistant prose for
// a turn from claude-code's JSONL transcript: every text block from every
// assistant message emitted since turnStart, in order, joined with blank lines.
// tool_use blocks are skipped.
//
// The tmux pane scrape keeps only the FINAL assistant text block, so any prose
// the model writes BEFORE a tool call (e.g. a full answer followed by a trailing
// suggest_actions / open_file tool call) is dropped from the captured response.
// The transcript is the authoritative record and preserves all of it; callers
// use this to repair that truncation. Returns "" if the transcript is missing,
// unparsable, or has no assistant text.
func fullAssistantProseFromTranscript(sessionID string, turnStart time.Time) string {
	msgs := readClaudeTranscriptMessages(sessionID, turnStart)
	if len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		if m.Role != llmtypes.ChatMessageTypeAI {
			continue
		}
		for _, p := range m.Parts {
			tc, ok := p.(llmtypes.TextContent)
			if !ok {
				continue
			}
			t := strings.TrimSpace(tc.Text)
			if t == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(t)
		}
	}
	return strings.TrimSpace(b.String())
}

// claudeAssistantContentBlock is one block inside an assistant
// message in claude-code's JSONL transcript. Hoisted to package scope
// so the helper below can take it as a typed parameter.
type claudeAssistantContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// assistantBlocksToParts maps claude content blocks into llmtypes parts.
func assistantBlocksToParts(blocks []claudeAssistantContentBlock) []llmtypes.ContentPart {
	var parts []llmtypes.ContentPart
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			parts = append(parts, llmtypes.TextContent{Text: b.Text})
		case "tool_use":
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			parts = append(parts, llmtypes.ToolCall{
				ID:   b.ID,
				Type: "function",
				FunctionCall: &llmtypes.FunctionCall{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}
	return parts
}

// flattenToolResultContent collapses a tool_result's `content` field
// (either a raw string or an array of {type:"text", text}) into a
// single string suitable for llmtypes.ToolCallResponse.Content.
func flattenToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Then array of {type, text}.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		joined := ""
		for i, b := range blocks {
			if b.Type != "text" {
				continue
			}
			if i > 0 && joined != "" {
				joined += "\n"
			}
			joined += b.Text
		}
		return joined
	}
	// Last resort: return raw JSON so nothing is silently dropped.
	return string(raw)
}
