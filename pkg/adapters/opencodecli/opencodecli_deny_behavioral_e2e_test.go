package opencodecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestOpenCodeCLIRealDenyBuiltinHookActuallyFires proves the built-in
// deny mechanism actually fires when the model invokes a disabled tool.
//
// Implementation history: an earlier version of this code dropped a JS
// plugin at .opencode/plugins/deny-builtin.js that threw from
// tool.execute.before. In practice opencode either didn't load the
// plugin or didn't honor the throw — the sentinel kept leaking through.
// The current adapter switched to opencode's documented
// `{"tools": {"read": false, ...}}` config field
// (opencode.ai/docs/config), which the structured adapter merges into
// opencode.jsonc when WithWriteProjectInstructionFile(true) is set.
//
// Shape:
//   1. Run a real opencode call with WithWriteProjectInstructionFile(true)
//      so the tools-deny block gets merged into opencode.jsonc.
//   2. Prompt opencode to invoke a built-in tool (we use `read` because
//      it's in the deny set and unambiguous).
//   3. Assert the sentinel file's content does NOT leak into the
//      response — proof that the built-in `read` did not execute.
//
// Why model behavior makes this less brittle than the old assertion:
// opencode/the model might refuse upfront, try the tool and report
// failure, or try then route around it. The only assertion we make
// is that the sentinel value never appears — that's the security
// contract callers actually depend on.
//
// Skipped unless RUN_OPENCODE_CLI_REAL_E2E=1 and opencode is on PATH.
// No API key is required: the test pins the model to opencode's hosted
// free tier (opencode/deepseek-v4-flash-free) so the security contract
// is verifiable on any workstation with the opencode binary installed.
func TestOpenCodeCLIRealDenyBuiltinHookActuallyFires(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	tmp := t.TempDir()

	// Create a sentinel file the model would naturally want to read,
	// so the prompt direction "read this file" is concrete and
	// model-friendly. The deny hook should block the read attempt
	// regardless of file existence.
	sentinelPath := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(sentinelPath, []byte("DENY_HOOK_SENTINEL_VALUE_42\n"), 0o600); err != nil {
		t.Fatalf("seed sentinel file: %v", err)
	}

	adapter := NewOpenCodeCLIAdapter("", freeTierTestModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Capture stream chunks so we can assert against tool-call results
	// (where the deny error message lands) AND the model's narration.
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
				llmtypes.TextContent{Text: "When the user asks you to read a file, your FIRST action must be to call the `read` tool. Do not refuse upfront; attempt the call and report whatever happens."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Read the file at " + sentinelPath + " using the read tool and tell me what it contains. If the read tool fails, quote the exact error message you received."},
			},
		},
	},
		WithWorkingDir(tmp),
		WithWriteProjectInstructionFile(true),
		llmtypes.WithStreamingChan(streamChan),
	)
	<-streamDone

	// Combine streamed chunks + final response into the assertion
	// haystack. We capture this BEFORE checking callErr because the
	// security contract is about what the model saw, not whether
	// opencode produced a clean exit: even a terminated/error run is
	// evidence-bearing if we got tool events back. Free-tier sessions
	// sometimes get killed mid-flow (response budget, internal
	// step-cap); that's a separate fragility we don't gate the
	// security claim on.
	haystack := streamContent.String()
	if resp != nil && len(resp.Choices) > 0 {
		haystack += "\n" + resp.Choices[0].Content
	}
	if callErr != nil {
		// Non-fatal: log so the surrounding context is visible if the
		// sentinel assertion below fails. Don't t.Fatalf — that would
		// mask the actual security contract.
		t.Logf("GenerateContent returned non-fatal error (process may have hit free-tier limits): %v", callErr)
	}
	if haystack == "" {
		// Defensive: if nothing was streamed AND no response, opencode
		// failed to even start — that's not a deny-mechanism signal,
		// it's environmental.
		t.Fatalf("no stream content and no response — opencode never produced output; callErr=%v", callErr)
	}

	// Sentinel must NOT appear — proves the built-in read tool did not
	// execute. The adapter writes opencode.jsonc with
	// {"tools":{"read": false, ...}}, so opencode itself refuses to run
	// the tool. The exact wording of opencode's refusal is an
	// implementation detail we don't pin; the only assertion that
	// matters for the security contract is that the sentinel never
	// reaches the model's output stream.
	if strings.Contains(haystack, "DENY_HOOK_SENTINEL_VALUE_42") {
		t.Errorf("sentinel value leaked into response — the built-in read tool succeeded despite opencode.jsonc tools-deny; check that buildOpenCodeProjectConfigJSON wired through and opencode honored the tools block\nfull haystack:\n%s", haystack)
	}
}
