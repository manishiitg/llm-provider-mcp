package picli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// writeFakePiScript installs a `pi` on PATH running the given shell script
// body. Unlike writeFakePi in the (reverted) event-based test, this lets each
// test control process exit independently of stdout closing -- the whole
// point of what's being tested here.
func writeFakePiScript(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pi")
	script := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runFakePiScriptTurn(t *testing.T, body string, timeout time.Duration) (*llmtypes.ContentResponse, time.Duration, error) {
	t.Helper()
	writeFakePiScript(t, body)
	adapter := &PiCLIAdapter{}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "hi"}},
		}},
		WithPiStructuredTransport(true),
		WithWorkingDir(t.TempDir()),
	)
	return resp, time.Since(start), err
}

// fdJuggleBackgroundSleep backgrounds a long sleep that keeps holding stdout
// (fd 1) open even after the fake pi's own process exits, reproducing a
// persistent child (e.g. pi's MCP bridge) that inherits the fd. Plain shell
// `&` backgrounding was tried first and found unreliable in this environment
// -- the backgrounded job sometimes did not keep fd 1 open, giving a false
// negative. Duplicating fd 1 to fd 3 first, backgrounding a subshell that
// writes through fd 3, then closing our own fd 3 was verified reliable across
// 5/5 runs (the plain `sleep &` variant was not).
const fdJuggleBackgroundSleep = "exec 3>&1\n( sleep 600 >&3 2>&1 & )\nexec 3>&-\n"

// fdJuggleBackgroundSleepStderr is fdJuggleBackgroundSleep's mirror for fd 2
// instead of fd 1: it backgrounds a long sleep holding STDERR's write end
// open past the fake pi's own exit. This is the actual mechanism of the live
// 2026-08-18 ICICI-BANK-PARSING incident, found via a production goroutine
// dump: pi's own process was confirmed gone, stdout had already closed
// cleanly, and cmd.Wait() itself stayed blocked for 51 minutes because
// os/exec's internal stderr-copy goroutine (from `cmd.Stderr = &bytes.Buffer`,
// which hands os/exec a pipe the adapter never gets a handle to) was still
// parked reading a pipe nothing had closed.
const fdJuggleBackgroundSleepStderr = "exec 3>&2\n( sleep 600 >&3 2>&1 & )\nexec 3>&-\n"

// This is the failure the adapter fix in this file addresses: stdout closes
// and drains normally, but stderr's write end is still held open by a
// lingering process, and cmd.Wait() -- which used to depend on an os/exec
// -owned stderr pipe (cmd.Stderr = &bytes.Buffer) that this adapter had no
// handle to and could not force closed -- must still return in bounded time.
// Switching to cmd.StderrPipe() (this file, generateContentStructured) gives
// the adapter its own handle, closed by os/exec the moment pi's own process
// exits, exactly like stdout.
//
// Fails before the fix (times out at the 8s deadline below, cmd.Wait()
// permanently blocked on the internal stderr copy goroutine); passes after.
func TestPiStructuredTerminatesWhenChildHoldsStderrAfterProcessExits(t *testing.T) {
	body := "printf '%s\\n' '{\"type\":\"agent_start\"}' >&2\n" +
		"printf '%s\\n' '{\"type\":\"agent_start\"}'\n" +
		"printf '%s\\n' '{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"done\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"turn_end\"}'\n" +
		"printf '%s\\n' '{\"type\":\"agent_settled\"}'\n" +
		fdJuggleBackgroundSleepStderr + "exit 0\n"

	resp, elapsed, err := runFakePiScriptTurn(t, body, 8*time.Second)
	if err != nil {
		t.Fatalf("GenerateContent error = %v (elapsed %v)", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %v to return -- cmd.Wait() should complete within ~milliseconds of pi's own process exit, not wait on a lingering child holding stderr", elapsed)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("no choices in response: %#v", resp)
	}
	if got := strings.TrimSpace(resp.Choices[0].Content); got != "done" {
		t.Fatalf("content = %q, want %q", got, "done")
	}
}

// This is the actual bug mechanism, not a proxy for it: pi's own process can
// exit while a child it spawned (its MCP bridge, in production) keeps stdout's
// write end open. The adapter used to block on <-scannerDone (waiting for
// EOF from every holder) before ever calling cmd.Wait, so this exact shape
// hung forever with no timeout -- live incident 2026-08-18
// (ICICI-BANK-PARSING-v2): real work finished at 09:45, pi's process exited,
// and the caller was held for 65 minutes until the stack was stopped by hand.
//
// Fails before the fix (times out at the 8s deadline below); passes after.
func TestPiStructuredTerminatesWhenChildHoldsStdoutAfterProcessExits(t *testing.T) {
	body := "printf '%s\\n' '{\"type\":\"agent_start\"}'\n" +
		"printf '%s\\n' '{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"done\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"turn_end\"}'\n" +
		"printf '%s\\n' '{\"type\":\"agent_end\"}'\n" +
		fdJuggleBackgroundSleep +
		"exit 0\n"

	resp, elapsed, err := runFakePiScriptTurn(t, body, 8*time.Second)
	if err != nil {
		t.Fatalf("GenerateContent error = %v (elapsed %v)", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %v to return -- should complete within ~milliseconds of pi's own process exit, not wait on the lingering child", elapsed)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("no choices in response: %#v", resp)
	}
	if got := strings.TrimSpace(resp.Choices[0].Content); got != "done" {
		t.Fatalf("content = %q, want %q", got, "done")
	}
}

// Same shape, but pi's stream ends on a type this codebase has never heard of
// -- proving completion is driven by process exit, not by recognizing any
// particular event name (which is exactly what made the adapter fragile to
// pi CLI version drift in the first place: agent_settled disappeared between
// pi 0.80.10 and 0.84.2, and agent_end -- the plausible substitute -- turned
// out to fire per model turn rather than once per run).
func TestPiStructuredTerminatesRegardlessOfFinalEventName(t *testing.T) {
	body := "printf '%s\\n' '{\"type\":\"agent_start\"}'\n" +
		"printf '%s\\n' '{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"done\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"turn_end\"}'\n" +
		"printf '%s\\n' '{\"type\":\"some_future_event_type_v2\"}'\n" +
		fdJuggleBackgroundSleep +
		"exit 0\n"

	resp, elapsed, err := runFakePiScriptTurn(t, body, 8*time.Second)
	if err != nil {
		t.Fatalf("GenerateContent error = %v (elapsed %v)", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %v to return -- completion must not depend on recognizing any specific event type", elapsed)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("no choices in response: %#v", resp)
	}
	if got := strings.TrimSpace(resp.Choices[0].Content); got != "done" {
		t.Fatalf("content = %q, want %q", got, "done")
	}
}

// Sanity check for the common case: no lingering child at all, pi exits
// cleanly on its own and stdout closes naturally. Must keep working exactly
// as before -- this is the path essentially every real run takes.
func TestPiStructuredTerminatesOnCleanExitNoLingeringChild(t *testing.T) {
	body := "printf '%s\\n' '{\"type\":\"agent_start\"}'\n" +
		"printf '%s\\n' '{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"clean\"}}'\n" +
		"printf '%s\\n' '{\"type\":\"turn_end\"}'\n" +
		"printf '%s\\n' '{\"type\":\"agent_end\"}'\n" +
		"printf '%s\\n' '{\"type\":\"agent_settled\"}'\n" +
		"exit 0\n"

	resp, elapsed, err := runFakePiScriptTurn(t, body, 8*time.Second)
	if err != nil {
		t.Fatalf("GenerateContent error = %v (elapsed %v)", err, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %v to return for a clean exit with no lingering child", elapsed)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("no choices in response: %#v", resp)
	}
	if got := strings.TrimSpace(resp.Choices[0].Content); got != "clean" {
		t.Fatalf("content = %q, want %q", got, "clean")
	}
}
