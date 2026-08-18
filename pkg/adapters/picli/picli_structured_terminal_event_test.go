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

// writeFakePi installs a `pi` on PATH that emits a fixed JSONL stream and then
// stays alive until it is killed. Staying alive is the whole point: it
// reproduces a continued native session, which is exactly the case the real
// hang needed. If nothing tears the process down, its stdout never closes, the
// adapter blocks on <-scannerDone, and this test times out — which is the bug.
func writeFakePi(t *testing.T, events []string) {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, e := range events {
		// single-quoted heredoc-free echo; events contain no single quotes
		b.WriteString("printf '%s\\n' '" + e + "'\n")
	}
	// Hold stdout open indefinitely. `sleep 600` outlives any sane test.
	b.WriteString("sleep 600\n")
	path := filepath.Join(dir, "pi")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake pi: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runFakePiTurn(t *testing.T, events []string) (*llmtypes.ContentResponse, error) {
	t.Helper()
	writeFakePi(t, events)
	adapter := &PiCLIAdapter{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "hi"}},
		}},
		WithPiStructuredTransport(true),
		WithWorkingDir(t.TempDir()),
	)
}

// The adapter was verified against pi 0.80.10, whose stream ended
// `agent_end -> agent_settled`, and it treated `agent_settled` as the sole
// teardown trigger. pi 0.84.2 does not emit `agent_settled` at all — measured
// across a day of production logs, it appeared 0 times while `agent_end`
// appeared 57 — so the only trigger was dead code against the installed pi.
//
// Any run where pi did not exit on its own then blocked forever on
// <-scannerDone with no timeout anywhere in the path. Live incident
// 2026-08-18 (ICICI-BANK-PARSING-v2 / manishiitg): real work finished at 09:45,
// agent_end was emitted, and the caller was held 65 minutes until the stack was
// stopped by hand.
//
// Fails before the fix (times out), passes after.
func TestPiStructuredTerminatesOnAgentEndWithoutAgentSettled(t *testing.T) {
	resp, err := runFakePiTurn(t, []string{
		`{"type":"session"}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"done"}}`,
		`{"type":"turn_end"}`,
		`{"type":"agent_end"}`,
	})
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("no choices in response: %#v", resp)
	}
	if got := strings.TrimSpace(resp.Choices[0].Content); got != "done" {
		t.Fatalf("content = %q, want %q", got, "done")
	}
}

// An older pi that still emits agent_settled must keep terminating on it, so
// the fix tolerates version drift in both directions rather than swapping one
// hard version dependency for another.
func TestPiStructuredStillTerminatesOnAgentSettled(t *testing.T) {
	resp, err := runFakePiTurn(t, []string{
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"legacy"}}`,
		`{"type":"turn_end"}`,
		`{"type":"agent_end"}`,
		`{"type":"agent_settled"}`,
	})
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("no choices in response: %#v", resp)
	}
	if got := strings.TrimSpace(resp.Choices[0].Content); got != "legacy" {
		t.Fatalf("content = %q, want %q", got, "legacy")
	}
}
