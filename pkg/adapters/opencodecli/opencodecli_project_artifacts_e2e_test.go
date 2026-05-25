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
// opencode.jsonc, run a tiny adapter call with the flag +
// WithMCPConfig + WithWorkingDir pointing at a tempdir, then verify
// after the call:
//
//   - <workingDir>/AGENTS.md byte-restored to operator pre-seed
//   - <workingDir>/opencode.jsonc byte-restored to operator pre-seed
//     (proves the merged-config write — MCP block + tools-deny —
//     restores operator-owned content correctly)
//   - mtime advanced past pre-seed on both files (proves the adapter
//     touched the workspace vs the null hypothesis of "writer never
//     ran")
//
// Historical note: an earlier version of this test pre-seeded
// .opencode/plugins/deny-builtin.js because the deny mechanism used
// to be a JS plugin auto-loaded from that directory. opencode never
// reliably fired the plugin (test was permanently skipped), so the
// adapter switched to opencode's documented {"tools":{"read":false,
// ...}} config block, which lands in opencode.jsonc. This test now
// guards the opencode.jsonc lifecycle instead.
//
// Skipped unless RUN_OPENCODE_CLI_REAL_E2E=1 and opencode is on PATH.
func TestOpenCodeCLIRealProjectArtifactsLifecycle(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	tmp := t.TempDir()

	agentsPath := filepath.Join(tmp, "AGENTS.md")
	jsoncPath := filepath.Join(tmp, "opencode.jsonc")

	operatorAGENTS := []byte("# Operator AGENTS.md\n\nThis content MUST be restored on cleanup.\n")
	operatorJsonc := []byte("// operator's own opencode.jsonc\n{\n  \"$operator\": true\n}\n")

	if err := os.WriteFile(agentsPath, operatorAGENTS, 0o600); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	if err := os.WriteFile(jsoncPath, operatorJsonc, 0o600); err != nil {
		t.Fatalf("seed opencode.jsonc: %v", err)
	}
	preInfoAgents, _ := os.Stat(agentsPath)
	preMTimeAgents := preInfoAgents.ModTime()
	preInfoJsonc, _ := os.Stat(jsoncPath)
	preMTimeJsonc := preInfoJsonc.ModTime()
	time.Sleep(10 * time.Millisecond)

	adapter := NewOpenCodeCLIAdapter("", freeTierTestModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// MCP config goes through the existing WithMCPConfig path; the
	// adapter merges it into the same opencode.jsonc write that the
	// tools-deny block lands in, so the single restore must put both
	// blocks of operator content back.
	mcpJSON := `{"mcpServers":{"orchestrator-bridge":{"command":"node","args":["/tmp/mcpbridge"],"env":{"MCP_API_URL":"http://localhost:9999"}}}}`

	// System message ensures the AGENTS.md write path inside the
	// structured adapter fires (it's gated on a non-empty systemPrompt).
	// Without this, the AGENTS.md writer would be skipped and mtime
	// would never advance, masking whether the wiring works.
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

	postJsonc, err := os.ReadFile(jsoncPath)
	if err != nil {
		t.Fatalf("opencode.jsonc must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postJsonc) != string(operatorJsonc) {
		t.Errorf("cleanup must restore operator opencode.jsonc byte-for-byte\n  want: %s\n  got:  %s", operatorJsonc, postJsonc)
	}

	postInfoAgents, _ := os.Stat(agentsPath)
	if !postInfoAgents.ModTime().After(preMTimeAgents) {
		t.Errorf("AGENTS.md mtime must advance past pre-seed (proves the writer touched the file mid-session and then restored); preSeed=%v post=%v", preMTimeAgents, postInfoAgents.ModTime())
	}
	postInfoJsonc, _ := os.Stat(jsoncPath)
	if !postInfoJsonc.ModTime().After(preMTimeJsonc) {
		t.Errorf("opencode.jsonc mtime must advance past pre-seed (proves the merged-config writer fired and then restored); preSeed=%v post=%v", preMTimeJsonc, postInfoJsonc.ModTime())
	}
}
