package opencodecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestOpenCodeCLIRealProjectArtifactsLifecycle is an end-to-end test
// that proves the WithWriteProjectInstructionFile lifecycle against a
// real `opencode` binary: pre-seed an operator-owned AGENTS.md AND
// .opencode/plugins/deny-builtin.js, run a tiny adapter call with the
// flag + WithMCPConfig + WithWorkingDir pointing at a tempdir, then
// verify after the call:
//
//   - <workingDir>/AGENTS.md byte-restored to operator pre-seed
//   - <workingDir>/.opencode/plugins/deny-builtin.js byte-restored
//     to operator pre-seed (proves the plugin-file projection
//     restores operator-owned content correctly)
//   - mtime advanced past pre-seed (proves the adapter touched the
//     workspace vs the null hypothesis of "writer never ran")
//
// Skipped unless RUN_OPENCODE_CLI_REAL_E2E=1 and opencode is on PATH.
func TestOpenCodeCLIRealProjectArtifactsLifecycle(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	tmp := t.TempDir()
	pluginsDir := filepath.Join(tmp, ".opencode", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("seed .opencode/plugins dir: %v", err)
	}

	agentsPath := filepath.Join(tmp, "AGENTS.md")
	pluginPath := filepath.Join(pluginsDir, "deny-builtin.js")

	operatorAGENTS := []byte("# Operator AGENTS.md\n\nThis content MUST be restored on cleanup.\n")
	operatorPlugin := []byte("// operator's own deny plugin — must survive cleanup\nexport default async () => ({});\n")

	if err := os.WriteFile(agentsPath, operatorAGENTS, 0o600); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	if err := os.WriteFile(pluginPath, operatorPlugin, 0o600); err != nil {
		t.Fatalf("seed deny-builtin.js: %v", err)
	}
	preInfo, _ := os.Stat(agentsPath)
	preMTime := preInfo.ModTime()
	time.Sleep(10 * time.Millisecond)

	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// MCP config goes through the existing WithMCPConfig path; it lands
	// at opencode.jsonc independently of WithWriteProjectInstructionFile,
	// so we don't pre-seed opencode.jsonc here — its lifecycle is
	// covered by existing tests.
	mcpJSON := `{"mcpServers":{"orchestrator-bridge":{"command":"/tmp/mcpbridge","env":{"MCP_API_URL":"http://localhost:9999"}}}}`

	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly the single word OK and nothing else."},
			},
		},
	},
		WithWorkingDir(tmp),
		WithMCPConfig(mcpJSON),
		WithWriteProjectInstructionFile(true),
	)
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v", callErr)
	}

	postAGENTS, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postAGENTS) != string(operatorAGENTS) {
		t.Errorf("cleanup must restore operator AGENTS.md byte-for-byte\n  want: %s\n  got:  %s", operatorAGENTS, postAGENTS)
	}

	postPlugin, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("deny-builtin.js must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postPlugin) != string(operatorPlugin) {
		t.Errorf("cleanup must restore operator deny-builtin.js byte-for-byte\n  want: %s\n  got:  %s", operatorPlugin, postPlugin)
	}

	postInfo, _ := os.Stat(agentsPath)
	if !postInfo.ModTime().After(preMTime) {
		t.Errorf("AGENTS.md mtime must advance past pre-seed (proves the adapter touched the file mid-session and then restored); preSeed=%v post=%v", preMTime, postInfo.ModTime())
	}
}
