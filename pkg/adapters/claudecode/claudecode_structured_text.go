package claudecode

import (
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Claude streams token deltas within messages, but starts a new message after
// tools. Preserve that explicit boundary in both live text and the saved turn;
// punctuation cannot distinguish a message boundary from a split token.
type claudeStructuredText struct {
	partial   bool
	emitted   bool
	completed strings.Builder
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
		for _, block := range ev.Message.Content {
			if block.Type != "text" || block.Text == "" {
				continue
			}
			if t.completed.Len() > 0 {
				t.completed.WriteString("\n\n")
			}
			t.completed.WriteString(block.Text)
			// The completed message repeats the partial stream. Without partial
			// events (structured transport), it is a complete block instead.
			if !t.partial {
				t.emitted = true
				emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: block.Text})
			}
		}
	}
}
