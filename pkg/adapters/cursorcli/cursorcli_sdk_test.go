package cursorcli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// The only stand-in is the @cursor/sdk module: the real bridge and adapter run in node. It models a
// local run that waits at a tool step until steered, as the live SDK does (PLAT-872, owner chose the
// SDK because ACP held a mid-reply message until the reply ended).
const fakeCursorSDK = `
let steered = '', cancelled = false;
function agent(id) {
  return {
    agentId: id,
    async send(text, opts) {
      let done, resolveDone = new Promise(r => { done = r; });
      const run = {
        async steer(t) { if (t === 'LATER') { done(); return 'revert_to_followup'; } steered = t; done(); return 'complete_delivered'; },
        async *stream() {
          yield { type: 'tool_call', call_id: 'c1', name: 'shell', status: 'running', args: { command: 'sleep' } };
          await Promise.race([resolveDone, new Promise(r => setTimeout(r, 5000))]);
          yield { type: 'tool_call', call_id: 'c1', name: 'shell', status: 'completed', result: 'SLEPT' };
          opts.onDelta({ update: { type: 'text-delta', text: 'answer to ' + text.slice(-5) + (steered ? ' +' + steered : '') } });
        },
        async wait() { return { status: cancelled ? 'cancelled' : 'finished', result: '' }; },
        async cancel() { cancelled = true; done(); },
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

// The Stop button (Escape) must reach the SDK bridge, not only ACP sessions.
func TestCursorSDKStopCancelsTheRunningTurn(t *testing.T) {
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
	owner := "chat-stop-test"
	done := make(chan error, 1)
	go func() {
		_, err := c.GenerateContent(context.Background(), []llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "hello world")},
			WithWorkingDir(dir), WithInteractiveSessionID(owner), llmtypes.WithStreamingChan(stream))
		done <- err
	}()
	for chunk := range stream {
		if chunk.Type == llmtypes.StreamChunkTypeToolCallStart {
			if err := InterruptACP(context.Background(), owner, "Escape"); err != nil {
				t.Fatalf("stop did not reach the SDK turn: %v", err)
			}
		}
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("turn was not cancelled: %v", err)
	}
}

// RTS 2026-10-27: a message sent 2s into a turn, before the SDK run had started, fell back to an
// after-turn injection and showed twice. It must join this turn, and text after a tool step must not
// be glued to the previous sentence ("ready.Yes.").
func TestCursorSDKEarlySteerJoinsTheSameTurn(t *testing.T) {
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
	stream := make(chan llmtypes.StreamChunk, 256)
	owner := "chat-early-steer"
	done := make(chan *llmtypes.ContentResponse, 1)
	go func() {
		resp, err := c.GenerateContent(context.Background(), []llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "hello world")},
			WithWorkingDir(dir), WithInteractiveSessionID(owner), llmtypes.WithStreamingChan(stream))
		if err != nil {
			t.Error(err)
		}
		done <- resp
	}()
	sent := false
	for start := time.Now(); !sent && time.Since(start) < 3*time.Second; time.Sleep(20 * time.Millisecond) {
		sent = SendCursorSDKInput(context.Background(), owner, "STEER") == nil
	}
	if !sent {
		t.Fatal("an early steer was refused while the turn was starting")
	}
	for range stream {
	}
	resp := <-done
	if resp == nil {
		return
	}
	if text := resp.Choices[0].Content; !strings.Contains(text, "STEER") {
		t.Fatalf("early steer was not answered in the same turn: %q", text)
	}
}

// A steer the SDK does not take becomes a follow-up run in the same turn; its text starts a new paragraph.
func TestCursorSDKFollowUpRunStartsNewParagraph(t *testing.T) {
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
	stream := make(chan llmtypes.StreamChunk, 256)
	owner := "chat-followup-paragraph"
	done := make(chan *llmtypes.ContentResponse, 1)
	go func() {
		resp, _ := c.GenerateContent(context.Background(), []llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "hello world")},
			WithWorkingDir(dir), WithInteractiveSessionID(owner), llmtypes.WithStreamingChan(stream))
		done <- resp
	}()
	steered := false
	for chunk := range stream {
		if chunk.Type == llmtypes.StreamChunkTypeToolCallStart && !steered {
			steered = true
			if err := SendCursorSDKInput(context.Background(), owner, "LATER"); err != nil {
				t.Fatal(err)
			}
		}
	}
	resp := <-done
	if resp == nil || !strings.Contains(resp.Choices[0].Content, "answer to world\n\nanswer to LATER") {
		t.Fatalf("follow-up text was glued to the first answer: %#v", resp)
	}
}
