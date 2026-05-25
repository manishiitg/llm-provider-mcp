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

	// STATUS: trust-review screen auto-dismiss is partially landed
	// (see waitForCodexPrompt + dismissCodexHookTrustReviewPrompt),
	// but the BEHAVIORAL verification is still flaky:
	//
	//   - The dismiss successfully gets past the menu form (Down +
	//     Enter selects "Trust all and continue") and the expanded
	//     review form (t + Escape) most of the time, so the session
	//     no longer times out at the trust prompt.
	//
	//   - HOWEVER, when the model attempts a Bash tool call,
	//     codex's deny hook stderr ("Built-in tools disabled by
	//     orchestrator policy") does NOT consistently reach the
	//     model's narrated response — the haystack captures only
	//     codex's "1 hook is new or changed" banner, not the
	//     script's stderr.
	//
	// Open questions (for the followup that fully fixes this):
	//   1. Does codex's PreToolUse hook stderr actually surface to
	//      the model under --dangerously-bypass-hook-trust mode?
	//      Or does the bypass also suppress the stderr feedback
	//      that's supposed to teach the model "don't try that
	//      again"?
	//   2. Is the stderr being captured by codex but not streamed
	//      to our adapter's stream-json output? Need to compare
	//      what `codex exec` shows vs what the tmux session emits.
	//   3. Does sending Down+Enter to the menu form actually trust
	//      the hook for runtime use, or does it only register the
	//      visual-trust state? The screen transition we observe
	//      ("Trusting hooks..." then back to a residual menu)
	//      suggests the runtime state may not be persisted within
	//      the same invocation.
	//
	// The unsafe-projections gate + auto-dismiss path is still
	// useful: it gets the session past the visual blocker so the
	// rest of the call works. The behavioral test stays skipped
	// until the stderr-to-model surfacing is figured out.
	t.Skip("known partial: workspace pre-trust (preTrustCodexWorkingDir, https://github.com/openai/codex/issues/14345) and hook trust review auto-dismiss both landed, but the visual hook-review screen still appears intermittently on the first session per hook-content SHA — codex persists per-hook trust in ~/.codex/config.toml under [hooks.state.\"<path>:<event>:0:0\"] trusted_hash = sha256:<hash>, which we don't yet pre-compute. After the FIRST session interactively trusts the hook content, subsequent sessions with the SAME hook content bypass the prompt automatically (cached by codex). Fix: compute SHA256 of the hook script + entry and pre-write the [hooks.state] block; or send-keys handling needs to be more robust for the multi-form prompt sequence.")

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
