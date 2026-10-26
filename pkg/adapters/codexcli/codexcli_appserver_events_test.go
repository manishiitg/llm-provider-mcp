package codexcli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-872-19: the live agentic capture joined distinct messages into
// "now.I found...content.ZEBRA". Preserve boundaries without replaying deltas.
func TestCodexAppServerSeparatesMessagesWithoutDuplicatingCompletedText(t *testing.T) {
	r := &codexRPC{events: make(chan codexRPCMessage, 10), done: make(chan struct{})}
	send := func(method, fields string) {
		r.events <- codexRPCMessage{Method: method, Params: json.RawMessage(`{"threadId":"thread","turnId":"turn",` + fields + `}`)}
	}
	send("item/agentMessage/delta", `"itemId":"a","delta":"I will "`)
	send("item/agentMessage/delta", `"itemId":"a","delta":"read it."`)
	send("item/completed", `"item":{"id":"a","type":"agentMessage","phase":"commentary","text":"I will read it."}`)
	// A message received whole must get the same boundary as streamed deltas.
	send("item/completed", `"item":{"id":"b","type":"agentMessage","phase":"commentary","text":"Read complete.\n\n"}`)
	send("item/agentMessage/delta", `"itemId":"c","delta":"done\n\n"`)
	send("item/agentMessage/delta", `"itemId":"c","delta":"`+"```text\\nx\\n```"+`"`)
	send("item/completed", `"item":{"id":"c","type":"agentMessage","phase":"final_answer","text":"done\n\n`+"```text\\nx\\n```"+`"}`)
	send("turn/completed", `"turn":{"id":"turn","status":"completed"}`)
	var output strings.Builder
	response, err := (&CodexCLIAdapter{}).consumeAppTurn(context.Background(), &codexAppSession{threadID: "thread"}, r, "thread", "turn", func(ch llmtypes.StreamChunk) {
		if ch.Type == llmtypes.StreamChunkTypeContent {
			if ch.Metadata[llmtypes.ContentDeltaMetadataKey] != true {
				t.Fatal("token deltas are mislabeled as whole messages")
			}
			output.WriteString(ch.Content)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	final := "done\n\n```text\nx\n```"
	if output.String() != "I will read it.\n\nRead complete.\n\n"+final || response.Choices[0].Content != final {
		t.Fatalf("message boundaries or final changed: %q / %q", output.String(), response.Choices[0].Content)
	}
}
