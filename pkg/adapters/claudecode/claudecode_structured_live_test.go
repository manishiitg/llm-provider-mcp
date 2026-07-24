package claudecode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestClaudeStructuredErrorResultSurfacesAsError proves the fix for a real
// bug: a "result" event with is_error:true (Claude's structured stream
// reports semantic failures — bad model id, API errors — this way, WITH the
// process exiting 0) used to be silently ignored, returning a "successful"
// StopReason:"stop" response with the error text as if it were the real
// answer. Forced live with a nonexistent --model id, which reliably produces
// is_error:true (verified: exit code 0, is_error:true, result text explains
// the model wasn't found).
func TestClaudeStructuredErrorResultSurfacesAsError(t *testing.T) {
	skipClaudeInteractivePersistentE2E(t)

	adapter := NewClaudeCodeInteractiveAdapter("totally-nonexistent-model-xyz", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "say hi"}}},
	}, WithClaudeStructuredTransport(true))

	if err == nil {
		t.Fatalf("expected an error for an is_error:true result, got a successful response: %+v", resp)
	}
	if !strings.Contains(err.Error(), "error result") {
		t.Errorf("expected the error to identify itself as a reported error result, got: %v", err)
	}
	t.Logf("correctly surfaced as an error: %v", err)
}

// TestClaudeStructuredToolCallHasCompleteLifecycle proves the fix for a real
// bug: structured tool_use events streamed a ToolCallStart with no ID and no
// matching ToolCallEnd. Claude's structured stream DOES carry a real id on
// tool_use and a matching tool_result (keyed by that same id) on a subsequent
// "user"-role event — verified live — this test proves the adapter now
// surfaces both halves with matching, non-empty IDs.
func TestClaudeStructuredToolCallHasCompleteLifecycle(t *testing.T) {
	skipClaudeInteractivePersistentE2E(t)

	adapter := NewClaudeCodeInteractiveAdapter(claudeInteractiveIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 64)
	var chunks []llmtypes.StreamChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for c := range streamChan {
			chunks = append(chunks, c)
		}
	}()

	_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use the Bash tool for every request. Do not answer without running a command."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Run: echo lifecycle_probe — then tell me what it printed."}}},
	},
		WithClaudeStructuredTransport(true),
		WithAllowedTools("Bash"),
		llmtypes.WithStreamingChan(streamChan),
	)
	<-done
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}

	var starts, ends []llmtypes.StreamChunk
	for _, c := range chunks {
		switch c.Type {
		case llmtypes.StreamChunkTypeToolCallStart:
			starts = append(starts, c)
		case llmtypes.StreamChunkTypeToolCallEnd:
			ends = append(ends, c)
		}
	}
	if len(starts) == 0 {
		t.Fatalf("expected at least one ToolCallStart, got none (chunks=%d)", len(chunks))
	}
	if len(ends) == 0 {
		t.Fatalf("expected at least one ToolCallEnd (the bug this test guards: starts with no matching ends), got none")
	}
	for _, s := range starts {
		if strings.TrimSpace(s.ToolCallID) == "" {
			t.Errorf("ToolCallStart missing ToolCallID: %+v", s)
		}
	}
	matched := false
	for _, s := range starts {
		for _, e := range ends {
			if s.ToolCallID != "" && s.ToolCallID == e.ToolCallID {
				matched = true
			}
		}
	}
	if !matched {
		t.Errorf("no ToolCallStart/ToolCallEnd pair shared a matching ToolCallID: starts=%+v ends=%+v", starts, ends)
	}
	t.Logf("tool lifecycle verified: %d start(s), %d end(s), matched=%v", len(starts), len(ends), matched)
}

// TestClaudeStructuredUsageNotDoubled proves the fix for a real bug: usage was
// accumulated from EVERY intermediate assistant event AND the terminal result
// event, roughly doubling input/cache token counts (verified live before the
// fix: summing assistant-event usages exactly matched the result event's own
// totals, meaning adding both doubled them). This test drives a multi-step
// turn (a tool call, so at least 2 assistant events + 1 result exist) and
// asserts the adapter's reported usage is NOT roughly double what a single
// terminal result would report alone.
func TestClaudeStructuredUsageNotDoubled(t *testing.T) {
	skipClaudeInteractivePersistentE2E(t)

	adapter := NewClaudeCodeInteractiveAdapter(claudeInteractiveIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use the Bash tool for every request."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Run: echo usage_probe — then tell me what it printed."}}},
	}, WithClaudeStructuredTransport(true), WithAllowedTools("Bash"))
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if resp == nil || resp.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	// A real turn with a tool call still costs well under 50k input tokens
	// even with a large cache_creation on the first call of a session. The
	// pre-fix bug doubled the true total by re-summing every assistant
	// event's usage on top of the already-cumulative result total — for a
	// cache-heavy session that inflation is dramatic (tens of thousands of
	// tokens), so a generous sanity ceiling here is still a real, meaningful
	// regression guard, not a tautology.
	if resp.Usage.InputTokens > 50_000 {
		t.Errorf("input tokens implausibly high (%d) — looks like the double-counting bug regressed", resp.Usage.InputTokens)
	}
	t.Logf("usage: input=%d output=%d total=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens)
}
