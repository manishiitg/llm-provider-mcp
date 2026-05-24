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

// TestClaudeCodeExperimentalRealProjectArtifactsLifecycle is an end-to-
// end test that proves the WithWriteProjectInstructionFile lifecycle
// against a real `claude` binary by exercising the SAFE projections
// (the ones not gated behind MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS).
//
// The current safe set for Claude Code is just .claude/rules/
// mlp-session-<hex>.md — the .mcp.json projection is disabled by
// default because it triggers Claude Code v2.1.150's "New MCP server
// found in .mcp.json — approve?" discovery prompt that the tmux
// adapter cannot dismiss. See claudecode_experimental_adapter.go for
// the gate explanation.
//
// Verifications after the call returns:
//   - <workingDir>/.claude/rules/ no longer contains any
//     mlp-session-*.md (proves the session-rule file we wrote was
//     cleaned up — without write→cleanup, leftover would persist)
//   - the call itself completed without timing out (proves the
//     session-rule file write does not trigger a prompt; vs the
//     .mcp.json write that does)
//
// Skipped unless RUN_CLAUDE_CODE_EXPERIMENTAL_INTEGRATION=1 and the
// `claude` binary is on PATH (same gate as sibling experimental E2Es).
func TestClaudeCodeExperimentalRealProjectArtifactsLifecycle(t *testing.T) {
	skipClaudeExperimentalIntegration(t)

	tmp := t.TempDir()
	rulesDir := filepath.Join(tmp, ".claude", "rules")

	adapter := NewClaudeCodeExperimentalAdapter(claudeExperimentalIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	// Provide a system prompt so the .claude/rules/*.md write path
	// fires inside buildClaudeArgs. Without a non-empty system prompt
	// the projection helper short-circuits and the test wouldn't be
	// exercising the feature.
	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply concisely."}},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly the single word OK and nothing else."},
			},
		},
	},
		WithWorkingDir(tmp),
		WithWriteProjectInstructionFile(true),
	)
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v", callErr)
	}

	// The .claude/rules/mlp-session-*.md path uses a per-session hex
	// nonce. If cleanup missed it we'd see a leftover here.
	if entries, err := os.ReadDir(rulesDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "mlp-session-") {
				t.Errorf(".claude/rules/%s leaked past cleanup; the mlp-session-* set should be removed", e.Name())
			}
		}
	}

	// Whether or not the rules dir exists after cleanup depends on
	// what else is in it. The key contract is "our files are gone."
	// If the rulesDir was created by us and is now empty, that's also
	// fine — Claude Code will recreate it on next launch if needed.
}
