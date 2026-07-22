package llmtypes

import "strings"

// This file holds the "message to the user" helpers for design-first UIs that
// stream/display a coding-agent turn without showing the terminal. There are
// three consumer modes:
//
//  1. Raw tmux        — show the terminal pane directly (StreamTerminalText).
//  2. Non-streaming   — full assistant MESSAGE = narration + final answer, with
//                       TOOLS REMOVED, narration separable (SplitAssistantText).
//  3. Streaming       — stream assistant TEXT live, TOOLS REMOVED
//                       (StreamAssistantText over Content chunks only).
//
// In modes 2 and 3 the tool calls are intentionally dropped — the user sees the
// assistant's words, not its tool activity.

// SplitAssistantText extracts the user-facing assistant message from a
// transcript message list, REMOVING tool calls and tool results. `narration` is
// the intermediate assistant text blocks (what it said while working); `final`
// is the last assistant text block (the concluding answer) — so a UI can
// highlight them separately. Non-streaming mode (2).
func SplitAssistantText(msgs []MessageContent) (narration []string, final string) {
	texts := assistantTextBlocks(msgs)
	if len(texts) == 0 {
		return nil, ""
	}
	return texts[:len(texts)-1], texts[len(texts)-1]
}

// FullAssistantText is narration + final joined — the complete message, tools
// removed. Non-streaming callers that don't need the narration/final split use
// this; it MUST equal the concatenation of a streaming consumer's text chunks.
func FullAssistantText(msgs []MessageContent) string {
	return strings.Join(assistantTextBlocks(msgs), "\n")
}

func assistantTextBlocks(msgs []MessageContent) []string {
	var texts []string
	for _, m := range msgs {
		if m.Role != ChatMessageTypeAI {
			continue
		}
		for _, p := range m.Parts {
			if tc, ok := p.(TextContent); ok {
				if s := strings.TrimSpace(tc.Text); s != "" {
					texts = append(texts, s)
				}
			}
			// ToolCall / ToolCallResponse are intentionally dropped — the user
			// sees the assistant's words, not its tool activity.
		}
	}
	return texts
}

// StreamAssistantText joins the TEXT of a streamed turn, DROPPING tool and
// terminal chunks — the text-only view a streaming UI shows (mode 3). Each
// Content chunk is one assistant text block (the transcript tailer emits one
// chunk per block), so joining with newlines yields the same message as
// FullAssistantText over that turn's transcript.
func StreamAssistantText(chunks []StreamChunk) string {
	var parts []string
	for _, c := range chunks {
		if c.Type == StreamChunkTypeContent {
			if s := strings.TrimSpace(c.Content); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// StreamTerminalText concatenates the raw terminal-snapshot chunks — the "show
// tmux directly" view (mode 1). Content/tool chunks are ignored here.
func StreamTerminalText(chunks []StreamChunk) string {
	var b strings.Builder
	for _, c := range chunks {
		if c.Type == StreamChunkTypeTerminal {
			b.WriteString(c.Content)
		}
	}
	return b.String()
}
