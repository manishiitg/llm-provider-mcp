package codexcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCodexCLIRealDenyBuiltinHookActuallyFires is the behavioral
// counterpart to TestWriteCodexProjectDenyBuiltinHooksLifecycleNoPriorContent:
// the lifecycle test proves the deny hook config + script land and
// get cleaned up; this test proves the hook actually fires when the
// model invokes a built-in (Bash or apply_patch) tool.
//
// MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS=1 is set inside the test so
// the .codex/hooks.json drop actually happens; without it the
// project-artifacts writer skips the hooks block per the safety gate
// (see writeCodexProjectArtifacts). The trade-off: with the env var
// set, codex shows a visual trust-review screen at startup that the
// tmux adapter cannot dismiss, so this test ALSO relies on
// --dangerously-bypass-hook-trust being appended to the codex args
// (which the adapter does when the env var is set, again per the
// gate logic).
//
// Assertion: model attempts a Bash call, codex's PreToolUse hook
// fires and exits 2 with stderr "Built-in tools disabled by
// orchestrator policy", the stderr surfaces to the model, the
// model narrates the failure OR fails to execute. Sentinel value
// MUST NOT appear in any tool output.
//
// Skipped unless RUN_CODEX_CLI_REAL_E2E=1 + codex + tmux.
func TestCodexCLIRealDenyBuiltinHookActuallyFires(t *testing.T) {
	requireRealCodexCLIE2E(t)

	// KNOWN GAP: when this test was first run against codex-cli
	// v0.131.0 with MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS=1 set
	// (so the .codex/hooks.json drop actually happens), codex
	// blocked on its interactive trust-review screen ("⚠ 1 hook
	// needs review before it can run. Press t to trust all; enter
	// to review hooks; esc to close") — exactly the behavior
	// documented in docs/WORKSPACE_PROJECTIONS.md Section 4.
	//
	// --dangerously-bypass-hook-trust IS appended to the codex args
	// when the env var is set (we verified this earlier and codex
	// prints "flag is enabled") but the flag only bypasses the
	// trust check on hook EXECUTION — it does NOT auto-dismiss the
	// visual review screen. The tmux adapter cannot send keystrokes
	// before the prompt fires and blocks waiting for ready state,
	// so the session hangs until the test timeout.
	//
	// Possible fixes (not done here, separate investigation):
	//   1. Add a post-launch tmux send-keys "t" then Enter to
	//      dismiss the review screen, gated on the env var being
	//      set so it only fires when the hooks projection happens.
	//   2. Submit a codex bug/feature request for a flag that
	//      auto-dismisses the review screen in tmux mode.
	//   3. Use codex's user-global ~/.codex/hooks.json instead of
	//      the project-scoped one — user-global hooks may be
	//      auto-trusted on subsequent invocations once approved
	//      once interactively.
	//
	// Until one of these is in, this test is skipped. The lifecycle
	// tests (TestWriteCodexProjectDenyBuiltinHooksLifecycleNoPriorContent
	// and TestWriteCodexProjectArtifactsComposesAllArtifacts with the
	// env var set) still lock in "file lands, cleanup restores" —
	// what's missing is "file is loaded and the hook fires when the
	// model invokes a built-in."
	t.Skip("known gap: codex v0.131.0 blocks on visual trust-review screen even with --dangerously-bypass-hook-trust set; needs send-keys-to-dismiss work before this can pass — see codexcli_interactive_adapter.go buildCodexInteractiveArgs and docs/WORKSPACE_PROJECTIONS.md Section 4")

	t.Setenv("MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS", "1")
	t.Cleanup(func() { _ = CleanupCodexCLIInteractiveSessions(context.Background()) })

	tmp := t.TempDir()
	sentinelPath := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(sentinelPath, []byte("DENY_HOOK_SENTINEL_VALUE_42\n"), 0o600); err != nil {
		t.Fatalf("seed sentinel file: %v", err)
	}

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 256)
	var streamContent strings.Builder
	streamDone := make(chan struct{})
	go func() {
		for chunk := range streamChan {
			streamContent.WriteString(chunk.Content)
		}
		close(streamDone)
	}()

	resp, callErr := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "When the user asks you to read or print a file, your FIRST action must be to call the Bash tool with `cat`. Do not refuse upfront; attempt the call and report whatever happens."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Use the Bash tool to run `cat " + sentinelPath + "` and tell me the output. If the Bash tool fails, quote the exact error message you received."},
			},
		},
	},
		WithInteractiveSessionID("codex-deny-behavioral-"+codexRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithProjectDirID(tmp),
		WithApprovalPolicy("never"),
		WithReasoningEffort("low"),
		WithWriteProjectInstructionFile(true),
		WithMCPServers(`{"placeholder":{"command":"/bin/echo"}}`),
		llmtypes.WithStreamingChan(streamChan),
	)
	<-streamDone

	// Force cleanup so the byte-restore runs before assertions and
	// .codex/hooks.json + script don't leak into the next test's tempdir.
	if err := CleanupCodexCLIInteractiveSessions(context.Background()); err != nil {
		t.Fatalf("force-cleanup of persistent codex session: %v", err)
	}

	if callErr != nil {
		t.Fatalf("GenerateContent error = %v\nstream so far:\n%s", callErr, streamContent.String())
	}

	haystack := streamContent.String()
	if resp != nil && len(resp.Choices) > 0 {
		haystack += "\n" + resp.Choices[0].Content
	}

	// Sentinel MUST NOT appear — would mean `cat` ran successfully
	// via Bash despite our matcher covering ^(Bash|apply_patch)$.
	if strings.Contains(haystack, "DENY_HOOK_SENTINEL_VALUE_42") {
		t.Errorf("sentinel value leaked into response — the Bash tool ran `cat` successfully despite the deny hook matcher; the hook may not be loaded or --dangerously-bypass-hook-trust may have un-installed it\nfull haystack:\n%s", haystack)
	}

	// Look for evidence the deny fired. Script writes to stderr:
	// "Built-in tools disabled by orchestrator policy". Codex surfaces
	// hook stderr to the model. Also accept the codex-specific status
	// message "Enforcing MCP-only tool policy" we embed in hooks.json.
	denyAnchors := []string{
		"disabled by orchestrator policy",
		"Enforcing MCP-only tool policy",
		"System Block",
		"hook denied",
	}
	matched := false
	for _, anchor := range denyAnchors {
		if strings.Contains(haystack, anchor) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("no deny-hook evidence found in stream or response — codex may not have fired the PreToolUse hook on Bash, OR the stderr did not surface to the model\nexpected one of: %v\nfull haystack:\n%s", denyAnchors, haystack)
	}
}
