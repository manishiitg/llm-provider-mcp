package codexcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCodexCLIRealReadOnlyHybridP0 certifies Codex's "Native agent tools"
// (hybrid) mode: its native shell and subagents ON (WithReadOnlyHybridTools),
// inside Codex's own OS-enforced read-only sandbox. Codex reads files only through its shell, so this is the
// candidate hybrid: native reads must work, a native write must be refused by
// the sandbox, and the MCP bridge must keep working for everything else.
func TestCodexCLIRealReadOnlyHybridP0(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })
	workDir := t.TempDir()
	secret := "CODEX-READ-" + codexRandomHex(4)
	needle := "CODEX-NEEDLE-" + codexRandomHex(4)
	if err := os.WriteFile(filepath.Join(workDir, "witness.txt"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "deep"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "deep", "notes.md"), []byte("x "+needle+" HIT-"+codexRandomHex(3)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	mcpServerPath := writeCodexSlowContractMCPServer(t, filepath.Join(t.TempDir(), "slow-tool-started"))
	mcpCommandOverride, err := codexStringConfigOverride("mcp_servers.api-bridge.command", mcpServerPath)
	if err != nil {
		t.Fatalf("build MCP command override: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	token := "BRIDGE_" + codexRandomHex(4)
	prompt := fmt.Sprintf("Integration test in a disposable directory. Use your own shell: 1) print witness.txt, 2) search the directory recursively for %s and note the HIT token on that line, "+
		"3) try to create a file with: touch native-write-attempt (report whether it was allowed). Then call the api-bridge slow_contract MCP tool with token %s and delay_ms 100. "+
		"Finally reply on one line: the witness contents, the HIT token, whether the write was allowed, and the MCP result.", needle, token)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}},
		WithInteractiveSessionID("codex-readonly-hybrid-"+codexRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithProjectDirID(workDir),
		WithSandbox("read-only"),
		WithReadOnlyHybridTools(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		WithConfigOverrides([]string{mcpCommandOverride}),
	)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	final := strings.TrimSpace(resp.Choices[0].Content)
	t.Logf("final: %s", final)
	if !strings.Contains(final, secret) {
		t.Fatalf("native read failed: final %q lacks %s", final, secret)
	}
	if !strings.Contains(final, "HIT-") {
		t.Fatalf("native search failed: final %q lacks the HIT token", final)
	}
	if !strings.Contains(final, "SLOW_BRIDGE_TOOL_OK_"+token) && !strings.Contains(final, token) {
		t.Fatalf("MCP bridge call missing from final: %q", final)
	}
	if _, err := os.Stat(filepath.Join(workDir, "native-write-attempt")); !os.IsNotExist(err) {
		t.Fatalf("read-only sandbox allowed a native write: %v", err)
	}
}

// TestCodexCLIRealReadOnlyHybridSubagentP0: in hybrid mode a Codex subagent
// can read, but inherits the read-only sandbox (no native write).
func TestCodexCLIRealReadOnlyHybridSubagentP0(t *testing.T) {
	requireRealCodexCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })
	workDir := t.TempDir()
	secret := "CODEX-CHILD-" + codexRandomHex(4)
	if err := os.WriteFile(filepath.Join(workDir, "witness.txt"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	started := time.Now()
	prompt := "Integration test in a disposable directory. Spawn exactly one subagent (do not do this work yourself). " +
		"Tell it to read witness.txt with its shell and to try: touch child-write-attempt, and to report both outcomes. " +
		"Wait for it, then reply with the witness contents it reported and whether its write was allowed."
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}},
		WithInteractiveSessionID("codex-readonly-hybrid-agent-"+codexRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithProjectDirID(workDir),
		WithSandbox("read-only"),
		WithReadOnlyHybridTools(),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
	)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	final := strings.TrimSpace(resp.Choices[0].Content)
	t.Logf("final: %s", final)
	if !strings.Contains(final, secret) {
		t.Fatalf("subagent read failed: final %q lacks %s", final, secret)
	}
	if _, err := os.Stat(filepath.Join(workDir, "child-write-attempt")); !os.IsNotExist(err) {
		t.Fatalf("read-only sandbox allowed a subagent write: %v", err)
	}
	// Prove a subagent actually ran: Codex records spawned agents in its
	// rollouts (the spawn tool call in the parent's rollout).
	home, _ := os.UserHomeDir()
	rollouts, _ := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"))
	spawned := false
	for _, f := range rollouts {
		info, statErr := os.Stat(f)
		if statErr != nil || info.ModTime().Before(started) {
			continue
		}
		raw, _ := os.ReadFile(f)
		if strings.Contains(string(raw), workDir) && strings.Contains(string(raw), "spawn_agent") {
			spawned = true
			break
		}
	}
	if !spawned {
		t.Fatalf("no subagent spawn recorded in this run's rollouts")
	}
}
