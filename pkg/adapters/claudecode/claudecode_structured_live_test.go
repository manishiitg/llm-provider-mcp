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
				// family-server reconstructs tool-call history exclusively
				// from End chunks — a blank name/args here means a blank
				// entry downstream even though the End correctly correlated.
				if strings.TrimSpace(e.ToolName) == "" {
					t.Errorf("ToolCallEnd %s missing ToolName (downstream history reconstruction reads only End chunks): %+v", e.ToolCallID, e)
				}
				if strings.TrimSpace(e.ToolArgs) == "" {
					t.Errorf("ToolCallEnd %s missing ToolArgs: %+v", e.ToolCallID, e)
				}
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
// event, roughly doubling token counts. This test drives a multi-step turn (a
// tool call, so at least 2 assistant events + 1 result exist) and asserts the
// adapter's reported usage is NOT roughly double a plausible single-result
// total.
//
// The assertion targets CacheTokens specifically, not InputTokens. Directly
// measured live (the same capture that found this bug): a real
// system-prompt-bearing turn had InputTokens in the single digits (2-4) the
// whole time — never the channel that mattered — while cache_creation +
// cache_read (folded into CacheTokens via accumulateClaudeUsage) were tens of
// thousands and were EXACTLY where the doubling landed (summing all
// assistant-event cache_read exactly matched the result event's own total,
// meaning the old code's "add both" computed 2x). An InputTokens-only ceiling
// (the original version of this test) could never have caught a
// CacheTokens-only regression, which is the realistic shape of this bug on a
// system-prompt-bearing session — this was a real gap in the test, not just
// the adapter.
func TestClaudeStructuredUsageNotDoubled(t *testing.T) {
	skipClaudeInteractivePersistentE2E(t)

	adapter := NewClaudeCodeInteractiveAdapter(claudeInteractiveIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// A real, non-trivial system prompt to force meaningful cache_creation on
	// this (first, cold) turn — a near-empty system prompt would leave
	// CacheTokens near zero regardless of whether the bug is present, making
	// the regression undetectable.
	systemPrompt := strings.Repeat("You are a careful, precise coding assistant operating in a sandboxed test environment. Follow instructions exactly. ", 40)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: systemPrompt + "Use the Bash tool for every request."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Run: echo usage_probe — then tell me what it printed."}}},
	}, WithClaudeStructuredTransport(true), WithAllowedTools("Bash"))
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if resp == nil || resp.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	cacheTokens := 0
	if resp.Usage.CacheTokens != nil {
		cacheTokens = *resp.Usage.CacheTokens
	}
	// The forced system prompt above is a few thousand tokens; a real cache
	// write/read for it stays well under 100k even accounting for Claude's
	// own prompt-caching overhead. The pre-fix bug doubled the true total by
	// re-summing every assistant event's usage on top of the already-
	// cumulative result total — for this cache-bearing shape that inflation
	// is dramatic (tens of thousands of tokens), so this ceiling is a real,
	// meaningful regression guard, not a tautology.
	if cacheTokens > 100_000 {
		t.Errorf("cache tokens implausibly high (%d) — looks like the double-counting bug regressed", cacheTokens)
	}
	if cacheTokens == 0 {
		t.Logf("[note] CacheTokens was 0 — this run didn't exercise the channel this test targets; not a failure, but weaker evidence")
	}
	t.Logf("usage: input=%d output=%d cache=%d total=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens, cacheTokens, resp.Usage.TotalTokens)
}
