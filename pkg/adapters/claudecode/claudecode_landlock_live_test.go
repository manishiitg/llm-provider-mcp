package claudecode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-364: Claude Code started under the Landlock launcher keeps its native
// tools and MCP bridge inside its folder, and the kernel refuses its native
// Read of another folder. Runs on a Linux host with the launcher:
// AGENTWORKS_LANDLOCK_RUNNER=/path/to/video-studio-landlock-runner.
func TestClaudeCodeTmuxRealLandlockConfinesNativeToolsP0(t *testing.T) {
	skipClaudeInteractivePersistentE2E(t)
	runner := os.Getenv("AGENTWORKS_LANDLOCK_RUNNER")
	if runtime.GOOS != "linux" || runner == "" {
		t.Skip("needs Linux and AGENTWORKS_LANDLOCK_RUNNER")
	}
	t.Cleanup(func() { _ = CleanupClaudeCodeTmuxSessions(context.Background()) })
	workDir := t.TempDir()
	otherDir := t.TempDir()
	privateHome := filepath.Join(t.TempDir(), "cli-home")
	own := "OWN-" + randomHex(4)
	foreign := "FOREIGN-" + randomHex(4)
	if err := os.WriteFile(filepath.Join(workDir, "own.txt"), []byte(own+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreignPath := filepath.Join(otherDir, "secret-test.txt")
	if err := os.WriteFile(foreignPath, []byte(foreign+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerDir := t.TempDir() // the test MCP server writes a marker here
	mcpServerPath := writeClaudeInteractiveSlowMCPServer(t, filepath.Join(markerDir, "slow-tool-started"))
	opts := []llmtypes.CallOption{
		WithInteractiveSessionID("claude-landlock-" + randomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":%q}}}`, mcpServerPath)),
		WithClaudeCodeTools(claudeHybridLiveTools),
		WithPermissionMode("auto"),
		WithAllowedTools("mcp__api-bridge__*,WebSearch"),
		WithEffort("low"),
	}
	policy := llmtypes.CLISecurityPolicy{
		Mode:           llmtypes.CLISecurityModeIsolated,
		Provider:       "claude-code",
		LandlockRunner: runner,
		PrivateHome:    privateHome,
		HostWritePaths: []string{markerDir},
	}
	opts = append(opts, func(o *llmtypes.CallOptions) { p := policy.Clone(); o.CLISecurity = &p })
	// An env-token account (RTS's server account): the adapter passes the
	// token, so nothing from the shared home is needed.
	adapter := NewClaudeCodeInteractiveAdapterWithOAuthToken(defaultClaudeInteractiveTestModel, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	prompt := "Integration test in disposable scratch folders. Using ONLY your native Read tool, read own.txt, then read " + foreignPath +
		". Then call the api-bridge slow_contract MCP tool with token BRIDGE-OK and delay_ms 100. " +
		"Reply on three lines: the first line of own.txt, the result or exact error for the second file, and the MCP result."
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}}, opts...)
	if err != nil {
		t.Fatalf("GenerateContent under Landlock: %v", err)
	}
	final := firstChoiceText(resp)
	t.Logf("reply: %q", final)
	if !strings.Contains(final, own) || !strings.Contains(final, "BRIDGE-OK") {
		t.Fatalf("confined Claude lost its own folder or MCP bridge: %q", final)
	}
	if strings.Contains(final, foreign) {
		t.Fatalf("confined Claude read another folder: %q", final)
	}
	// Claude's transcript is in the private home, where the adapter read it.
	if matches, _ := filepath.Glob(filepath.Join(privateHome, ".claude", "projects", "*", "*.jsonl")); len(matches) == 0 {
		t.Fatalf("no transcript in the private home %s", privateHome)
	}
}
