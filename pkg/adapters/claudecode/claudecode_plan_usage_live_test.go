package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestClaudePlanUsageLive is the CertPlanUsage P0 proof for Claude: after a
// real turn, the account's plan usage windows (5h / 7d used %, reset) are
// readable from Claude's statusline sidecar as structured RateLimitWindows.
// Claude writes rate_limits only after its first API response, which the
// turn provides. Requires a subscription login (API-key auth has no plan
// windows).
//
// Gated behind -coding-cli-p0-live; requires a real claude CLI, node, tmux.
func TestClaudePlanUsageLive(t *testing.T) {
	skipClaudeInteractiveIntegration(t)
	t.Cleanup(func() { _ = CleanupClaudeCodeTmuxSessions(context.Background()) })

	started := time.Now()
	adapter := NewClaudeCodeInteractiveAdapter(defaultClaudeInteractiveTestModel, &MockLogger{})
	marker := "PLAN_" + strings.ToUpper(randomHex(4))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Task "+marker+": what is 2+2? Reply with one line: the answer, a space, then the task ID.")},
		WithWorkingDir(t.TempDir()),
		WithEffort("low"),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if len(resp.Choices) != 1 || !strings.Contains(resp.Choices[0].Content, marker) {
		t.Fatalf("turn did not produce the marker: %+v", resp)
	}

	var windows []llmtypes.RateLimitWindow
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && len(windows) == 0 {
		paths, _ := filepath.Glob(filepath.Join(os.TempDir(), "claude_statusline_*.json"))
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil || info.ModTime().Before(started) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "claude_statusline_"), ".json")
			if found, ok := readClaudeStatuslineRateLimitWindows(name); ok && len(found) > 0 {
				windows = found
				break
			}
		}
		if len(windows) == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	assertLivePlanUsageWindows(t, windows, started)
	if known, _ := UsageLimitState(""); known {
		t.Fatal("UsageLimitState must be unknown for an empty session")
	}
}

// assertLivePlanUsageWindows requires at least one named window with a sane
// used percentage and, when stated, a reset in the future.
func assertLivePlanUsageWindows(t *testing.T, windows []llmtypes.RateLimitWindow, started time.Time) {
	t.Helper()
	if len(windows) == 0 {
		t.Fatal("no plan usage windows after a real turn (is the CLI logged in with a subscription?)")
	}
	for _, w := range windows {
		if strings.TrimSpace(w.Name) == "" || w.UsedPercent < 0 {
			t.Fatalf("malformed window %+v", w)
		}
		if !w.ResetsAt.IsZero() && w.ResetsAt.Before(started.Add(-time.Minute)) {
			t.Fatalf("window %s resets in the past: %v", w.Name, w.ResetsAt)
		}
	}
}
