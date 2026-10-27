package cursorcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// The only stand-in is the @cursor/sdk module: the real bridge and adapter run in node. It models a
// local run that waits at a tool step until steered, as the live SDK does (PLAT-872, owner chose the
// SDK because ACP held a mid-reply message until the reply ended).
const fakeCursorSDK = `
let steered = '';
function agent(id) {
  return {
    agentId: id,
    async send(text, opts) {
      let done, resolveDone = new Promise(r => { done = r; });
      const run = {
        async steer(t) { steered = t; done(); return 'complete_delivered'; },
        async *stream() {
          yield { type: 'tool_call', call_id: 'c1', name: 'shell', status: 'running', args: { command: 'sleep' } };
          await Promise.race([resolveDone, new Promise(r => setTimeout(r, 5000))]);
          yield { type: 'tool_call', call_id: 'c1', name: 'shell', status: 'completed', result: 'SLEPT' };
          opts.onDelta({ update: { type: 'text-delta', text: 'answer to ' + text.slice(-5) + (steered ? ' +' + steered : '') } });
        },
        async wait() { return { status: 'finished', result: '' }; },
        async cancel() {},
      };
      return run;
    },
  };
}
exports.Agent = { create: async () => agent('agent-test'), resume: async id => agent(id) };
`

func TestCursorSDKSteersTheRunningTurn(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	entry := filepath.Join(dir, "fake-sdk.cjs")
	if err := os.WriteFile(entry, []byte(fakeCursorSDK), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURSOR_SDK_PATH", entry)
	c := NewCursorCLIAdapter("", "composer-2.5", nil)
	c.SetSDKTransport(true)
	stream := make(chan llmtypes.StreamChunk, 64)
	owner := "chat-steer-test"
	type result struct {
		resp *llmtypes.ContentResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := c.GenerateContent(context.Background(), []llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "hello world")},
			WithWorkingDir(dir), WithInteractiveSessionID(owner), llmtypes.WithStreamingChan(stream))
		done <- result{resp, err}
	}()
	var chunks []llmtypes.StreamChunk
	steered := false
	for chunk := range stream {
		chunks = append(chunks, chunk)
		if chunk.Type == llmtypes.StreamChunkTypeToolCallStart && !steered {
			steered = true
			if err := SendCursorSDKInput(context.Background(), owner, "STEER"); err != nil {
				t.Fatalf("steer refused while the turn runs: %v", err)
			}
		}
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if text := r.resp.Choices[0].Content; !strings.Contains(text, "+STEER") {
		t.Fatalf("steer did not reach the running turn: %q", text)
	}
	var ended bool
	for _, chunk := range chunks {
		ended = ended || (chunk.Type == llmtypes.StreamChunkTypeToolCallEnd && chunk.ToolResult == "SLEPT")
	}
	if !ended {
		t.Fatal("tool result was not streamed")
	}
	if err := SendCursorSDKInput(context.Background(), owner, "late"); err == nil {
		t.Fatal("a steer with no running turn must report not sent")
	}
}
