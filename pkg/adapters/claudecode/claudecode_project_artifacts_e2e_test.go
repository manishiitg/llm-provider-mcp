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
// against a real `claude` binary: pre-seed an operator-owned .mcp.json,
// run a tiny adapter call with the flag + MCP config + WithWorkingDir
// pointing at a tempdir, then verify after the call returns:
//
//   - <workingDir>/.mcp.json content matches the operator's pre-seed
//     (proves byte-restore on cleanup ran)
//   - <workingDir>/.mcp.json mtime moved past the pre-seed mtime
//     (proves the orchestrator content was actually installed
//     mid-session and then restored — not skipped)
//   - <workingDir>/.claude/rules/ no longer contains any
//     mlp-session-*.md (proves the session-rule file we wrote was
//     cleaned up)
//
// Why this shape: directly observing the mid-session workspace state
// would require interposing inside the adapter (tmux running). The
// "pre-seed + restore + mtime advance" assertion combo proves the
// whole write→restore lifecycle from the outside without needing to
// race the live session. Without this E2E, the unit tests cover the
// helper but never prove the adapter's buildClaudeArgs actually invokes
// the helper when the flag is set.
//
// Skipped unless RUN_CLAUDE_CODE_EXPERIMENTAL_INTEGRATION=1 and the
// `claude` binary is on PATH (same gate as sibling experimental E2Es).
func TestClaudeCodeExperimentalRealProjectArtifactsLifecycle(t *testing.T) {
	skipClaudeExperimentalIntegration(t)

	tmp := t.TempDir()
	mcpPath := filepath.Join(tmp, ".mcp.json")
	operatorMCP := []byte(`{
  "mcpServers": {
    "operator-pre-existing": {
      "command": "/opt/operator-mcp-do-not-destroy"
    }
  }
}
`)
	if err := os.WriteFile(mcpPath, operatorMCP, 0o600); err != nil {
		t.Fatalf("seed operator .mcp.json: %v", err)
	}
	preSeedInfo, err := os.Stat(mcpPath)
	if err != nil {
		t.Fatalf("stat operator .mcp.json: %v", err)
	}
	preSeedMTime := preSeedInfo.ModTime()

	// Give the filesystem a beat so any modification by the adapter
	// produces a strictly-greater mtime on coarse-grained systems.
	time.Sleep(10 * time.Millisecond)

	adapter := NewClaudeCodeExperimentalAdapter(claudeExperimentalIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	orchestratorMCP := `{"mcpServers":{"orchestrator-session":{"command":"/tmp/orchestrator-mcp"}}}`

	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly the single word OK and nothing else."},
			},
		},
	},
		WithWorkingDir(tmp),
		WithWriteProjectInstructionFile(true),
		WithMCPConfig(orchestratorMCP),
	)
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v", callErr)
	}

	postBody, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf(".mcp.json must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postBody) != string(operatorMCP) {
		t.Errorf("cleanup must restore operator .mcp.json byte-for-byte\n  want: %s\n  got:  %s", operatorMCP, postBody)
	}

	postInfo, _ := os.Stat(mcpPath)
	if !postInfo.ModTime().After(preSeedMTime) {
		t.Errorf("mtime must have advanced past the pre-seed (proves the adapter touched the file mid-session and then restored); preSeed=%v post=%v", preSeedMTime, postInfo.ModTime())
	}

	// The .claude/rules/mlp-session-*.md path uses a per-session hex
	// nonce, so if cleanup missed it we'd see a leftover here.
	rulesDir := filepath.Join(tmp, ".claude", "rules")
	if entries, err := os.ReadDir(rulesDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "mlp-session-") {
				t.Errorf(".claude/rules/%s leaked past cleanup; entire mlp-session-* set should be removed", e.Name())
			}
		}
	}
}
