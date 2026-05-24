package geminicli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestGeminiCLIRealProjectArtifactsLifecycle is an end-to-end test
// that proves the WithWriteProjectInstructionFile lifecycle against a
// real `gemini` binary: pre-seed an operator-owned GEMINI.md and
// .gemini/settings.json, run a tiny adapter call with the flag +
// WithProjectSettings + WithWorkingDir pointing at a tempdir, then
// verify after the call returns:
//
//   - <workingDir>/GEMINI.md byte-restored to operator pre-seed
//   - <workingDir>/.gemini/settings.json byte-restored to operator
//     pre-seed (with our hook merge having happened mid-session — we
//     can't observe that here, only the bracketing restore)
//   - mtime advanced past pre-seed (proves the adapter touched the
//     workspace vs the null hypothesis of "writer never ran")
//   - .gemini/hooks/deny-builtin.sh removed (we created the dir, so
//     cleanup must clean it up)
//
// Skipped unless RUN_GEMINI_CLI_REAL_E2E=1 and gemini+tmux+GEMINI_API_KEY
// are set.
func TestGeminiCLIRealProjectArtifactsLifecycle(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY required for real Gemini E2E")
	}

	tmp := t.TempDir()
	geminiDir := filepath.Join(tmp, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("seed .gemini dir: %v", err)
	}

	geminiMDPath := filepath.Join(tmp, "GEMINI.md")
	settingsPath := filepath.Join(geminiDir, "settings.json")

	operatorMD := []byte("# Operator GEMINI.md\n\nThis content MUST be restored on cleanup.\n")
	operatorSettings := []byte(`{
  "theme": "Default",
  "hooks": {
    "BeforeTool": [
      {"matcher":"operator-only","hooks":[{"type":"command","command":"/opt/operator-hook.sh"}]}
    ]
  }
}`)
	if err := os.WriteFile(geminiMDPath, operatorMD, 0o600); err != nil {
		t.Fatalf("seed GEMINI.md: %v", err)
	}
	if err := os.WriteFile(settingsPath, operatorSettings, 0o600); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	preMD, _ := os.Stat(geminiMDPath)
	preMTime := preMD.ModTime()
	time.Sleep(10 * time.Millisecond)

	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Operator-supplied project settings (with mcpServers) that the
	// adapter is expected to MERGE with the deny hook into the
	// projected .gemini/settings.json — and then restore the operator
	// file on cleanup.
	orchestratorProjectSettings := `{"mcpServers":{"orchestrator-bridge":{"command":"/tmp/mcpbridge"}}}`

	_, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly the single word OK and nothing else."},
			},
		},
	},
		WithInteractiveSessionID("gemini-project-artifacts-"+geminiRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(tmp),
		WithApprovalMode("yolo"),
		WithWriteProjectInstructionFile(true),
		WithProjectSettings(orchestratorProjectSettings),
	)
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v", callErr)
	}

	postMD, err := os.ReadFile(geminiMDPath)
	if err != nil {
		t.Fatalf("GEMINI.md must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postMD) != string(operatorMD) {
		t.Errorf("cleanup must restore operator GEMINI.md byte-for-byte\n  want: %s\n  got:  %s", operatorMD, postMD)
	}

	postSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json must still exist after cleanup (byte-restored): %v", err)
	}
	if string(postSettings) != string(operatorSettings) {
		t.Errorf("cleanup must restore operator settings.json byte-for-byte\n  want: %s\n  got:  %s", operatorSettings, postSettings)
	}

	postMDInfo, _ := os.Stat(geminiMDPath)
	if !postMDInfo.ModTime().After(preMTime) {
		t.Errorf("GEMINI.md mtime must advance past pre-seed (proves the adapter touched the file mid-session and then restored); preSeed=%v post=%v", preMTime, postMDInfo.ModTime())
	}

	scriptPath := filepath.Join(geminiDir, "hooks", "deny-builtin.sh")
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Errorf("deny-builtin.sh leaked past cleanup; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(geminiDir, "hooks")); !os.IsNotExist(err) {
		t.Errorf(".gemini/hooks/ leaked past cleanup; the directory we created should be removed when empty")
	}
}
