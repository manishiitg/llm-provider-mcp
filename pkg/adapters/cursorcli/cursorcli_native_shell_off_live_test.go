package cursorcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorCLIRealFullModeNativeShellOff (PLAT-491): in Full mode Cursor's own
// shell is refused by default, its native Write still works, and
// AGENTWORKS_CLI_NATIVE_SHELL=on brings the shell back. Both transports.
func TestCursorCLIRealFullModeNativeShellOff(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })
	for _, transport := range []string{"interactive", "structured"} {
		for _, shellOn := range []bool{false, true} {
			name := transport + "/shell-off"
			if shellOn {
				name = transport + "/shell-on"
			}
			t.Run(name, func(t *testing.T) {
				if shellOn {
					t.Setenv("AGENTWORKS_CLI_NATIVE_SHELL", "on")
				} else {
					t.Setenv("AGENTWORKS_CLI_NATIVE_SHELL", "")
				}
				tmp := t.TempDir()
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
				defer cancel()
				prompt := "Integration test in a disposable directory. Use Cursor's built-in tools (not MCP). " +
					"1) Run the shell command: id -un > id-out.txt . 2) Create native-file.txt containing the word ok using your built-in Write tool. " +
					"Report each outcome and quote any denial or error verbatim."
				opts := []llmtypes.CallOption{WithWorkingDir(tmp), WithFullNativeTools(), liveConfined(t, "cursor-cli", tmp)}
				if transport == "structured" {
					opts = append(opts, WithCursorStructuredTransport(true))
				} else {
					opts = append(opts, WithInteractiveSessionID("cursor-nsh-"+cursorRandomHex(4)), WithPersistentInteractiveSession(true))
				}
				resp, err := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{}).GenerateContent(ctx, []llmtypes.MessageContent{
					{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "When the user asks you to use a built-in tool, your FIRST action must be to attempt that Cursor built-in tool. Do not refuse upfront; attempt the call and report whatever happens."}}},
					{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}},
				}, opts...)
				if denials, readErr := os.ReadFile(filepath.Join(tmp, ".cursor", "hooks", "mlp-deny-builtin-denials.jsonl")); readErr == nil {
					t.Logf("hook saw: %.600s", denials)
				}
				if err != nil {
					t.Fatalf("GenerateContent: %v", err)
				}
				t.Logf("final: %.700s", resp.Choices[0].Content)
				if _, statErr := os.Stat(filepath.Join(tmp, "native-file.txt")); statErr != nil {
					t.Errorf("native Write did not work: %v", statErr)
				}
				out, statErr := os.ReadFile(filepath.Join(tmp, "id-out.txt"))
				ran := statErr == nil && strings.TrimSpace(string(out)) != ""
				// Structured (--print) mode never ran the built-in shell in Full mode, even before the
				// switch existed: Cursor asks for an approval nobody can give ("Rejected"). Only the
				// interactive transport is expected to run it with the switch on.
				if shellOn && !ran && transport != "structured" {
					t.Errorf("shell on: the built-in shell did not run (%v)", statErr)
				}
				if !shellOn && ran {
					t.Errorf("shell off: the built-in shell ran: %q", out)
				}
			})
		}
	}
}
