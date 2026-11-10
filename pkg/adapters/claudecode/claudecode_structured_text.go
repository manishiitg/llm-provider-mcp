package claudecode

import (
	"strconv"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Claude streams token deltas within messages, but starts a new message after
// tools. Preserve that explicit boundary in both live text and the saved turn;
// punctuation cannot distinguish a message boundary from a split token.
type claudeStructuredText struct {
	partial       bool
	emitted       bool
	completed     strings.Builder
	seenCompleted map[string]bool
}

func (t *claudeStructuredText) observe(ev claudeStreamEvent, emit func(llmtypes.StreamChunk)) {
	switch ev.Type {
	case "stream_event":
		if ev.Event == nil {
			return
		}
		if ev.Event.Type == "message_start" {
			t.partial = false
		}
		if ev.Event.Type != "content_block_delta" || ev.Event.Delta.Type != "text_delta" || ev.Event.Delta.Text == "" {
			return
		}
		text := ev.Event.Delta.Text
		if !t.partial && t.emitted {
			text = "\n\n" + text
		}
		t.partial, t.emitted = true, true
		emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: text, Metadata: map[string]any{llmtypes.ContentDeltaMetadataKey: true}})
	case "assistant":
		if ev.Message == nil {
			return
		}
		for index, block := range ev.Message.Content {
			// PLAT-746-839: the SDK supplies completed thinking blocks separately
			// from text. Forward them on the existing reasoning channel, never
			// into the reply. Partial thinking deltas remain ignored here so the
			// completed block is emitted once, not repeated after its fragments.
			if block.Type == "thinking" && strings.TrimSpace(block.Thinking) != "" {
				emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeReasoning, Content: block.Thinking})
				continue
			}
			if block.Type != "text" || block.Text == "" {
				continue
			}
			// A completed native message is a durable transcript snapshot,
			// not another token delta. Keep it even when tokens already streamed:
			// only the final result survives otherwise (PLAT-746-838).
			if ev.Message.ID != "" {
				if t.seenCompleted == nil {
					t.seenCompleted = make(map[string]bool)
				}
				key := ev.Message.ID + ":" + strconv.Itoa(index)
				if t.seenCompleted[key] {
					continue
				}
				t.seenCompleted[key] = true
			}
			if t.completed.Len() > 0 {
				t.completed.WriteString("\n\n")
			}
			t.completed.WriteString(block.Text)
			t.emitted = true
			emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: block.Text,
				Metadata: map[string]any{"claude_code_stream_source": "transcript"}})
		}
	}
}
