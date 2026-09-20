package cursorcli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorTokenUsageLive is the CertTokenUsage P0 proof for Cursor: a
// real turn reports input/output token counts through GenerationInfo, the
// surface the cost ledger reads. Cursor's tmux counts are heuristic, so the
// turn must also carry the token_usage_estimated marker from its
// "estimated" contract source.
//
// Gated behind -coding-cli-p0-live; requires a real cursor CLI, node, tmux.
func TestCursorTokenUsageLive(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	owner := "cursor-usage-live-" + cursorRandomHex(4)
	workDir := t.TempDir()
	marker := "USAGE_" + strings.ToUpper(cursorRandomHex(4))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Task "+marker+": what is 2+2? Reply with one line: the answer, a space, then the task ID.")},
		WithInteractiveSessionID(owner),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	final := ""
	if len(resp.Choices) == 1 {
		final = strings.TrimSpace(resp.Choices[0].Content)
	}
	if !strings.Contains(final, marker) {
		t.Fatalf("turn did not produce the marker; final=%q", final)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].GenerationInfo == nil {
		t.Fatal("turn reported no GenerationInfo")
	}
	gi := resp.Choices[0].GenerationInfo
	usage := llmtypes.ExtractUsageFromGenerationInfo(gi)
	if usage == nil || usage.InputTokens <= 0 || usage.OutputTokens <= 0 {
		t.Fatalf("usage = %+v, want live input/output counts", usage)
	}
	if estimated, _ := gi.Additional["token_usage_estimated"].(bool); !estimated {
		t.Fatal("estimated-source turn must set token_usage_estimated so cost reports flag it approximate")
	}
}
