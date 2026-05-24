package codexcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCodexCLIRealProjectArtifactsLifecycle is an end-to-end test that
// proves the WithWriteProjectInstructionFile lifecycle against a real
// `codex` binary: pre-seed an operator-owned AGENTS.md + .codex/hooks.json,
// run a tiny adapter call with the flag + WithMCPServers + WithProjectDirID
// pointing at a tempdir, then verify after the call:
//
//   - <workingDir>/AGENTS.md byte-restored to operator pre-seed
//     (proves codex's per-session AGENTS.md write + restore lifecycle)
//   - <workingDir>/.codex/hooks.json byte-restored to operator pre-seed
//     (proves the hooks.json projection cleanup runs even when the
//     hooks.json was operator-owned, not freshly created)
//   - mtime advanced past pre-seed on at least one artifact (proves
//     the adapter actually touched the workspace mid-session vs the
//     null hypothesis of "writer was never invoked")
//   - .codex/hooks/deny-builtin.sh is gone (we created the dir, so
//     cleanup must remove it — proves dir-removal heuristic works)
//
// Skipped unless RUN_CODEX_CLI_REAL_E2E=1 (or RUN_CODEX_CLI_INTERACTIVE_E2E=1)
// and codex+tmux are on PATH.
func TestCodexCLIRealProjectArtifactsLifecycle(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	tmp := t.TempDir()
	agentsPath := filepath.Join(tmp, "AGENTS.md")
	hooksPath := filepath.Join(tmp, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatalf("seed .codex dir: %v", err)
	}

	operatorAGENTS := []byte("# Operator AGENTS.md\n\nThis content MUST be restored on cleanup.\n")
	operatorHooks := []byte(`{"hooks":{"PreToolUse":[{"matcher":"operator-only","hooks":[{"type":"command","command":"/opt/operator-deny.sh"}]}]}}`)
	if err := os.WriteFile(agentsPath, operatorAGENTS, 0o600); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	if err := os.WriteFile(hooksPath, operatorHooks, 0o600); err != nil {
		t.Fatalf("seed hooks.json: %v", err)
	}
	preAgents, _ := os.Stat(agentsPath)
	preMTime := preAgents.ModTime()
	time.Sleep(10 * time.Millisecond)

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	mcpJSON := `{"orchestrator-bridge":{"command":"/tmp/mcpbridge","env":{"MCP_API_URL":"http://localhost:9999"}}}`

	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly the single word OK and nothing else."},
			},
		},
	},
		WithInteractiveSessionID("codex-project-artifacts-"+codexRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithProjectDirID(tmp),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		WithWriteProjectInstructionFile(true),
		WithMCPServers(mcpJSON),
	)
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v", callErr)
	}

	postAgents, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postAgents) != string(operatorAGENTS) {
		t.Errorf("cleanup must restore operator AGENTS.md byte-for-byte\n  want: %s\n  got:  %s", operatorAGENTS, postAgents)
	}

	postHooks, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("hooks.json must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postHooks) != string(operatorHooks) {
		t.Errorf("cleanup must restore operator hooks.json byte-for-byte\n  want: %s\n  got:  %s", operatorHooks, postHooks)
	}

	postInfo, _ := os.Stat(agentsPath)
	if !postInfo.ModTime().After(preMTime) {
		t.Errorf("AGENTS.md mtime must advance past pre-seed (proves the adapter touched the file mid-session and then restored); preSeed=%v post=%v", preMTime, postInfo.ModTime())
	}

	// Deny script: pre-seed didn't include it, so we created the
	// .codex/hooks/ dir + script. Cleanup must remove both.
	scriptPath := filepath.Join(tmp, ".codex", "hooks", "deny-builtin.sh")
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Errorf("deny-builtin.sh leaked past cleanup; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".codex", "hooks")); !os.IsNotExist(err) {
		t.Errorf(".codex/hooks/ leaked past cleanup; the directory we created should be removed when empty")
	}
}
