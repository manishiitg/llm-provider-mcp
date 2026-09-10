package musecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestMuseExecLaneMCPMountReachesCLIAndRestores proves the settings-merge
// mount reaches the CLI and leaves no trace, using the CLI's own failure as
// the witness: with an unreachable dummy bridge URL, the run must fail with
// "Required MCP server `api-bridge` failed during startup" (observed live
// 2026-09-10) — proving muse read our merged settings — and the temp config
// home must have no settings.json afterwards (restore runs on failure too).
// A success-path run needs a real reachable bridge and belongs to the live
// mcp_bridge P0, not here.
func TestMuseExecLaneMCPMountReachesCLIAndRestores(t *testing.T) {
	requireMuseBinary(t)
	museEchoTestEnv(t)
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		t.Fatal("museEchoTestEnv must set XDG_CONFIG_HOME")
	}
	adapter := NewMuseCLIAdapter("", "muse-cli", museTestLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "say the word pineapple"}}},
	}, WithMCPConfig(`{"mcpServers": {"api-bridge": {"url": "http://127.0.0.1:9/bridge"}}}`), WithMuseStructuredTransport(true))
	if err == nil {
		t.Fatal("expected failure: dummy bridge URL cannot initialize")
	}
	if !strings.Contains(err.Error(), "api-bridge") {
		t.Fatalf("error = %q, want it to name the mounted server (proof the CLI read our merge)", err)
	}
	if _, statErr := os.Stat(filepath.Join(configHome, "muse", "settings.json")); !os.IsNotExist(statErr) {
		t.Fatal("settings.json left behind after failed mounted run (restore did not run)")
	}
}
