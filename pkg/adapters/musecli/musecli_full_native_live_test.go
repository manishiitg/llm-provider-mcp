package musecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Full CLI Muse (no allowlist, --yolo): the TUI must settle and take the first
// prompt, its own shell must create a file in the working directory, and the
// MCP bridge must keep working. Nothing exercised this launch before
// Excellence 2026-10-03, where Muse's failed sandbox probe kept the TUI from
// settling and the prompt was never typed in.
func TestMuseCLIRealFullNative(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	for _, structured := range []bool{false, true} {
		name := "tmux"
		if structured {
			name = "structured"
		}
		t.Run(name, func(t *testing.T) {
			var called atomic.Int32
			stub := museProbeMCPStub(&called)
			defer stub.Close()
			workDir := t.TempDir()
			secret := "FULL-READ-" + museRandomHex(t, 4)
			if err := os.WriteFile(filepath.Join(workDir, "witness.txt"), []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			token := "FULL-MCP-" + museRandomHex(t, 4)
			prompt := "Integration test in a disposable directory. Use your own shell to run: cat witness.txt && touch native-made.txt. " +
				"Then call the probe-stub MCP tool with token " + token + ". Reply with the witness contents and the MCP result."
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			resp, err := museLiveAdapter().GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}},
				WithMCPConfig(`{"mcpServers":{"probe-stub":{"url":"`+stub.URL+`/mcp"}}}`), WithWorkingDir(workDir), WithMuseStructuredTransport(structured), WithTmuxTransport(!structured), llmtypes.WithReasoningEffort("low"))
			if err != nil {
				t.Fatalf("full-mode round trip: %v", err)
			}
			if handle := resp.Choices[0].GenerationInfo.CodingProviderSessionHandle; handle != nil && handle.TmuxSession != "" {
				t.Cleanup(func() { CloseMuseCLIInteractiveSessionByTmux(handle.TmuxSession, "full-mode probe complete") })
			}
			final := resp.Choices[0].Content
			if !strings.Contains(final, secret) {
				t.Fatalf("native read failed: %q", final)
			}
			if _, statErr := os.Stat(filepath.Join(workDir, "native-made.txt")); statErr != nil {
				t.Fatalf("Muse's own shell could not create a file: %v", statErr)
			}
			if called.Load() == 0 || !strings.Contains(final, token) {
				t.Fatalf("MCP bridge not used: calls=%d final=%q", called.Load(), final)
			}
		})
	}
}
