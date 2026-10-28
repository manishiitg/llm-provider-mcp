package cursorcli

import (
	"context"
	"errors"
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
exports.Cursor = { auth: { status: async () => ({status:'logged-in'}) }, models: {list: async () => [{id:'composer-2.5',variants:[{isDefault:true,params:[{id:'fast',value:'false'}]}]}]} };
let steered = '', cancelled = false, settleAfterWait;
function agent(id) {
  return {
    agentId: id,
    async send(text, opts) {
      check(opts);
      let done, resolveDone = new Promise(r => { done = r; });
      const run = {
        async steer(t) {
          if (t === 'LATER') { done(); return 'revert_to_followup'; }
          // The real SDK settles a steer it had queued only as its run ends, after run.wait() returned.
          if (t === 'QUEUED') { done(); return new Promise(r => { settleAfterWait = () => setTimeout(() => r('revert_to_followup'), 50); }); }
          steered = t; done(); return 'complete_delivered';
        },
        async *stream() {
          yield { type: 'tool_call', call_id: 'c1', name: 'shell', status: 'running', args: { command: 'sleep' } };
          await Promise.race([resolveDone, new Promise(r => setTimeout(r, 5000))]);
          yield { type: 'tool_call', call_id: 'c1', name: 'shell', status: 'completed', result: 'SLEPT' };
          opts.onDelta({ update: { type: 'text-delta', text: 'answer to ' + text.slice(-5) + (steered ? ' +' + steered : '') } });
        },
        async wait() { settleAfterWait?.(); settleAfterWait = undefined; return { status: cancelled ? 'cancelled' : 'finished', result: '' }; },
        async cancel() { cancelled = true; done(); },
      };
      return run;
    },
  };
}
// PLAT-872-64: the real bridge must apply the account's default settings before launch/resume.
function check(opts) { if (JSON.stringify(opts.model.params) !== '[{"id":"fast","value":"false"}]') throw Error('Model unavailable on Plus. Switch to an included model to continue.'); }
exports.Agent = { create: async opts => {check(opts); return agent('agent-test');}, resume: async (id,opts) => {check(opts); return agent(id);} };
`

// PLAT-872-64: discovery failure cannot block a chat; explicit choices win,
// and a second start in the same account process reuses the discovered defaults.
func TestCursorSDKModelDiscoveryDoesNotBlockOrOverrideSelection(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	for _, tc := range []struct{ name, lookup, model, expected string }{
		{"cached default", `if (++calls > 1) throw Error('second lookup'); return [{id:'composer',variants:[{isDefault:true,params:[{id:'fast',value:'false'}]}]}]`, `{"id":"composer"}`, `[{"id":"fast","value":"false"}]`},
		{"explicit selection", `throw Error('must not discover')`, `{"id":"composer","params":[{"id":"fast","value":"true"}]}`, `[{"id":"fast","value":"true"}]`},
		{"unavailable catalog", `throw Error('network unavailable')`, `{"id":"composer"}`, `undefined`},
		{"stalled catalog", `return new Promise(() => {})`, `{"id":"composer"}`, `undefined`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bridge, entry := filepath.Join(dir, "bridge.cjs"), filepath.Join(dir, "sdk.cjs")
			// The SDK looks for its bundled ripgrep above argv[1]; from the temp bridge folder every
			// grep/glob failed "Ripgrep path not configured" on server B (2026-10-28).
			sdk := `if(require('fs').realpathSync(process.argv[1])!==__filename) throw Error('Ripgrep path not configured'); let calls=0; exports.Cursor={auth:{status:async()=>({status:'logged-in'})},models:{list:async()=>{` + tc.lookup + `}}}; exports.Agent={create:async opts=>{if(JSON.stringify(opts.model.params)!==JSON.stringify(` + tc.expected + `))throw Error('wrong model settings');return {agentId:'ready'};}};`
			for name, data := range map[string][]byte{bridge: cursorSDKBridge, entry: []byte(sdk)} {
				if err := os.WriteFile(name, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", bridge, entry)
			start := `{"type":"start","model":` + tc.model + `}` + "\n"
			cmd.Stdin = strings.NewReader(start + start + "{\"type\":\"close\"}\n")
			out, err := cmd.CombinedOutput()
			if err != nil || strings.Count(string(out), `"type":"ready"`) != 2 {
				t.Fatalf("starts failed: %v, %s", err, out)
			}
		})
	}
}

// PLAT-872-64: an owner got a reply, then a plan refusal on the next message
// using the same native agent. Keep the selected params explicit on both sends,
// even when the SDK's retained default differs after a run.
func TestCursorSDKResumedChatKeepsSelectedModelAcrossTwoTurns(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	bridge, entry := filepath.Join(dir, "bridge.cjs"), filepath.Join(dir, "sdk.cjs")
	sdk := `
exports.Cursor={auth:{status:async()=>({status:'logged-in'})},models:{list:async()=>[{id:'composer-2.5',variants:[{isDefault:true,params:[{id:'fast',value:'false'}]}]}]}};
exports.Agent={
  create:async()=>{throw Error('must retain the native conversation');},
  resume:async(id,options)=>{
    let retained=options.model, turns=0;
    return {agentId:id, send:async(text,sendOptions)=>{
      const model=sendOptions.model??retained;
      if(model.id!=='composer-2.5'||JSON.stringify(model.params)!=='[{"id":"fast","value":"false"}]') throw Error('Model unavailable on Plus. Switch to an included model to continue.');
      return {async *stream(){},wait:async()=>{
        retained={id:'composer-2.5'};
        return {status:'finished',result:'reply '+(++turns)};
      }};
    }};
  }
};`
	for name, data := range map[string][]byte{bridge: cursorSDKBridge, entry: []byte(sdk)} {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", bridge, entry)
	cmd.Stdin = strings.NewReader(`{"type":"start","agent_id":"agent-existing","model":{"id":"composer-2.5"}}
{"type":"prompt","text":"hi"}
{"type":"prompt","text":"ok"}
{"type":"close"}
`)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"agent_id":"agent-existing","resumed":true`) || strings.Count(string(out), `"status":"finished"`) != 2 || !strings.Contains(string(out), `"result":"reply 2"`) {
		t.Fatalf("resumed two-turn conversation failed: %v, %s", err, out)
	}
}

// PLAT-872: a server restart killed the bridge mid-run (Excellence 2026-10-27 17:11), the run stayed
// active in Cursor's local store, and every later turn of that chat failed "Agent ... already has active
// run". A resumed agent's stale run is cancelled; if it cannot be, the chat starts a new native agent.
func TestCursorSDKResumeClearsStaleActiveRun(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	for _, tc := range []struct{ name, cancel, want string }{
		{"cancelled", `active=false`, `"agent_id":"agent-existing","resumed":true,"stale":"cancelled"`},
		{"new agent", `throw Error('store locked')`, `"agent_id":"agent-new","resumed":false,"stale":"new_agent","stale_agent":"agent-existing"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bridge, entry := filepath.Join(dir, "bridge.cjs"), filepath.Join(dir, "sdk.cjs")
			sdk := `
let active=true;
const agent=id=>({agentId:id, send:async()=>{
  if(id==='agent-existing'&&active) throw Error('Agent '+id+' already has active run');
  return {async *stream(){}, wait:async()=>({status:'finished',result:'reply'})};
}});
exports.Cursor={auth:{status:async()=>({status:'logged-in'})},models:{list:async()=>[]}};
exports.Agent={
  resume:async id=>agent(id), create:async()=>agent('agent-new'),
  listRuns:async id=>({items:id==='agent-existing'&&active?[{id:'run-stale',status:'running'}]:[{id:'run-old',status:'finished'}]}),
  cancelRun:async runId=>{if(runId!=='run-stale') throw Error('wrong run'); ` + tc.cancel + `},
};`
			for name, data := range map[string][]byte{bridge: cursorSDKBridge, entry: []byte(sdk)} {
				if err := os.WriteFile(name, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", bridge, entry)
			cmd.Stdin = strings.NewReader(`{"type":"start","agent_id":"agent-existing","model":{"id":"composer-2.5"}}
{"type":"prompt","text":"hi"}
{"type":"close"}
`)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), tc.want) || !strings.Contains(string(out), `"status":"finished"`) {
				t.Fatalf("stale active run was not cleared: %v, %s", err, out)
			}
		})
	}
}

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
// QUEUED: on Excellence (2026-10-28) a second chat message was accepted mid-turn, the SDK settled it
// only after the run ended, and the bridge had already closed the turn, so it was never answered.
func TestCursorSDKFollowUpRunStartsNewParagraph(t *testing.T) {
	for _, steer := range []string{"LATER", "QUEUED"} {
		t.Run(steer, func(t *testing.T) { testCursorSDKFollowUpRun(t, steer) })
	}
}

func testCursorSDKFollowUpRun(t *testing.T, steer string) {
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
	owner := "chat-followup-paragraph-" + steer
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
			if err := SendCursorSDKInput(context.Background(), owner, steer); err != nil {
				t.Fatal(err)
			}
		}
	}
	resp := <-done
	if resp == nil || !strings.Contains(resp.Choices[0].Content, "answer to world\n\nanswer to "+steer[len(steer)-5:]) {
		t.Fatalf("follow-up text was glued to the first answer: %#v", resp)
	}
}

// Reviewer 2026-10-27: a steer sent while the bridge launches must never be acknowledged and then lost
// when the launch fails; it is reported not sent so the host keeps it for the next turn.
func TestCursorSDKSteerDuringFailedLaunchIsNotSent(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	entry := filepath.Join(dir, "fake-sdk.cjs")
	broken := "exports.Cursor = { auth: { status: async () => ({status: 'logged-in'}) }, models: {list: async () => []} }; exports.Agent = { create: async () => { await new Promise(r => setTimeout(r, 300)); throw new Error('login required'); } };"
	if err := os.WriteFile(entry, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURSOR_SDK_PATH", entry)
	c := NewCursorCLIAdapter("", "composer-2.5", nil)
	c.SetSDKTransport(true)
	owner := "chat-failed-launch"
	done := make(chan error, 1)
	go func() {
		_, err := c.GenerateContent(context.Background(), []llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "hello")},
			WithWorkingDir(dir), WithInteractiveSessionID(owner))
		done <- err
	}()
	var steerErr error
	for start := time.Now(); time.Since(start) < 3*time.Second; time.Sleep(10 * time.Millisecond) {
		cursorSDKSessions.Lock()
		s := cursorSDKSessions.sessions[owner]
		cursorSDKSessions.Unlock()
		if s != nil {
			s.mu.Lock()
			running := s.running
			s.mu.Unlock()
			if running {
				steerErr = SendCursorSDKInput(context.Background(), owner, "keep me")
				break
			}
		}
	}
	if err := <-done; err == nil {
		t.Fatal("a failed launch must fail the turn")
	}
	if !errors.Is(steerErr, llmtypes.ErrCodingAgentInputNotSent) {
		t.Fatalf("steer during a failed launch = %v, want not sent", steerErr)
	}
}
