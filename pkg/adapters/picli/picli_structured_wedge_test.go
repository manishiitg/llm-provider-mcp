package picli

import (
	"context"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// A wedged pi -- silent, nothing in flight -- must be cut loose in ~the
// silence threshold, NOT left to burn the full piMaxTurnDuration ceiling.
//
// This is the cost that motivated the fast path: the real 2026-08-19 wedge
// (PLAT-153) went silent at 20:30:04 right after a message_end, with no tool
// call outstanding, and was only reclaimed by the 45m ceiling at 20:56:38 --
// 26 minutes of dead time per occurrence, paid again for every step that
// wedges.
func TestPiStructuredKillsAWedgedTurnWithoutWaitingOutTheCeiling(t *testing.T) {
	t.Setenv("PI_STRUCTURED_WEDGE_SILENCE", "1s")
	// Deliberately far larger, so passing CANNOT be the ceiling firing early.
	t.Setenv("PI_STRUCTURED_MAX_TURN_DURATION", "60s")

	// Emits one real pi event (so the stream is proven alive and the adapter
	// has something to have gone silent *after*), then wedges forever.
	writeFakePiScript(t, `echo '{"type":"agent_start"}'
sleep 999999
`)

	adapter := &PiCLIAdapter{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	start := time.Now()
	_, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "hi"}},
		}},
		WithPiStructuredTransport(true),
		WithWorkingDir(t.TempDir()),
	)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a wedged process")
	}
	// The stall poll ticks every 30s, so the kill lands on the first tick
	// after the 1s threshold. Anything approaching the 60s ceiling means the
	// fast path did not fire and the ceiling did the work instead.
	if elapsed > 45*time.Second {
		t.Fatalf("took %s -- the wedge fast-path did not fire; the turn was reclaimed by the ceiling, which is the slow behaviour this exists to avoid", elapsed)
	}
}

// The guard that makes the fast path safe: a tool call may legitimately run
// for a very long time (codingtimeout.LongRunningMCPToolTimeout, 90m by
// default) with pi emitting NOTHING for its whole duration. Killing on
// silence alone would murder healthy long tool calls -- the single most
// damaging false positive this change could introduce, since it would abort
// real work that was progressing fine.
func TestPiStructuredDoesNotKillDuringALongRunningToolCall(t *testing.T) {
	t.Setenv("PI_STRUCTURED_WEDGE_SILENCE", "1s")
	t.Setenv("PI_STRUCTURED_MAX_TURN_DURATION", "8s")

	// Opens a tool call and then goes silent for far longer than the wedge
	// threshold, exactly like a slow tool. The tool never ends, so this is
	// eventually reclaimed by the CEILING (8s) -- correct -- rather than by
	// the wedge path (1s), which must stay out of the way while a tool is in
	// flight.
	writeFakePiScript(t, `echo '{"type":"tool_execution_start","toolCallId":"t1","toolName":"slow_tool"}'
sleep 999999
`)

	adapter := &PiCLIAdapter{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	start := time.Now()
	_, _ = adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "hi"}},
		}},
		WithPiStructuredTransport(true),
		WithWorkingDir(t.TempDir()),
	)
	elapsed := time.Since(start)

	// Must survive well past the 1s wedge threshold. The first stall poll is
	// at 30s, and the 8s ceiling fires before that, so a correct run is
	// bounded by the ceiling -- what must NOT happen is a sub-second kill.
	if elapsed < 5*time.Second {
		t.Fatalf("killed after only %s while a tool call was in flight -- a legitimate long-running tool (allowed up to 90m) would be aborted mid-work", elapsed)
	}
}
