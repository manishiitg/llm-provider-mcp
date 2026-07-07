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

// TestCursorCLIRealDenyBuiltinHookActuallyFires is the behavioral
// counterpart to TestWriteCursorDenyBuiltinHooksLifecycle: the
// lifecycle test proves the deny hook config + script land and clean
// up; this test proves the hook actually fires when the model invokes
// a cursor built-in tool, AND that the call site does not pass
// --force (which silently bypasses cursor's hook system under yolo
// mode — the 2026-05-25 bug2 regression).
//
// Setup mirrors the gemini behavioral E2E: real cursor-agent binary,
// persistent tmux session, WithDenyBuiltinTools(true) so the merged
// .cursor/hooks.json + .cursor/hooks/mlp-deny-builtin.sh both land.
// CRITICAL: this test deliberately does NOT call WithForce().
//
// The deny script emits permission=deny JSON for shell, read, list,
// search, and edit built-ins with guidance to use api-bridge MCP tools
// instead. Per cursor's hooks contract (cursor.com/docs/hooks),
// permission=deny aborts the tool call and surfaces the user_message
// to the model.
//
// Assertion strategy:
//   - Sentinel value MUST NOT appear in the response (would mean the
//     built-in cat / read tool succeeded despite the hook).
//   - The deny user_message anchor SHOULD appear (or the model's own
//     narration of the failure) — confirming the hook surfaced back.
//
// Skipped unless RUN_CURSOR_CLI_REAL_E2E=1 (or _INTERACTIVE_E2E=1)
// and cursor + tmux are on PATH.
func TestCursorCLIRealDenyBuiltinHookActuallyFires(t *testing.T) {
	requireRealCursorCLIE2E(t)

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, tmp string) (prompt string, forbidden []string)
	}{
		{
			name: "shell",
			setup: func(t *testing.T, tmp string) (string, []string) {
				secret := "CURSOR_DENY_SHELL_SECRET_" + cursorRandomHex(5)
				secretPath := filepath.Join(tmp, "secret-shell.txt")
				if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0o600); err != nil {
					t.Fatalf("seed shell sentinel file: %v", err)
				}
				return "Use Cursor's built-in Shell tool, not MCP tools and not a refusal, to run this exact command: cat " + secretPath + ". If it fails, quote the exact denial/error.", []string{secret}
			},
		},
		{
			name: "read",
			setup: func(t *testing.T, tmp string) (string, []string) {
				sentinel := "CURSOR_DENY_READ_SENTINEL_" + cursorRandomHex(5)
				sentinelPath := filepath.Join(tmp, "secret-read.txt")
				if err := os.WriteFile(sentinelPath, []byte(sentinel+"\n"), 0o600); err != nil {
					t.Fatalf("seed read sentinel file: %v", err)
				}
				return "Use Cursor's built-in Read tool, not MCP tools and not a refusal, to read this exact file: " + sentinelPath + ". If it fails, quote the exact denial/error.", []string{sentinel}
			},
		},
		{
			name: "list",
			setup: func(t *testing.T, tmp string) (string, []string) {
				secretName := "cursor-list-secret-" + cursorRandomHex(5) + ".txt"
				if err := os.WriteFile(filepath.Join(tmp, secretName), []byte("list sentinel\n"), 0o600); err != nil {
					t.Fatalf("seed list sentinel file: %v", err)
				}
				return "Use Cursor's built-in ListDir or Glob tool, not MCP tools and not a refusal, to list this exact directory: " + tmp + ". If it fails, quote the exact denial/error.", []string{secretName}
			},
		},
		{
			name: "search",
			setup: func(t *testing.T, tmp string) (string, []string) {
				needle := "CURSOR_SEARCH_NEEDLE_" + cursorRandomHex(5)
				secret := "CURSOR_SEARCH_SECRET_" + cursorRandomHex(5)
				if err := os.WriteFile(filepath.Join(tmp, "search-target.txt"), []byte(needle+" "+secret+"\n"), 0o600); err != nil {
					t.Fatalf("seed search sentinel file: %v", err)
				}
				return "Use Cursor's built-in Search or Grep tool, not MCP tools and not a refusal, to search this directory for " + needle + ": " + tmp + ". If it fails, quote the exact denial/error.", []string{secret}
			},
		},
		{
			name: "delegation",
			setup: func(t *testing.T, tmp string) (string, []string) {
				secret := "CURSOR_DELEGATION_SECRET_" + cursorRandomHex(5)
				secretPath := filepath.Join(tmp, "secret-delegation.txt")
				if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0o600); err != nil {
					t.Fatalf("seed delegation sentinel file: %v", err)
				}
				return "Use Cursor's built-in Task, Agent, Subagent, BackgroundAgent, CloudAgent, or Delegate tool, not MCP tools and not a refusal, to ask a child agent to read this exact file: " + secretPath + ". If delegation fails or is blocked, quote the exact denial/error.", []string{secret}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })
			tmp := t.TempDir()
			prompt, forbidden := tc.setup(t, tmp)
			runCursorDenyBuiltinProbe(t, tmp, tc.name, prompt, forbidden)
		})
	}
}

func runCursorDenyBuiltinProbe(t *testing.T, tmp, label, prompt string, forbidden []string) {
	t.Helper()

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
				llmtypes.TextContent{Text: "When the user asks you to use a built-in tool, your FIRST action must be to attempt that Cursor built-in tool. Do not refuse upfront; attempt the call and report whatever happens, quoting any denial/error verbatim."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: prompt},
			},
		},
	},
		WithInteractiveSessionID("cursor-deny-"+label+"-"+cursorRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(tmp),
		WithDenyBuiltinTools(true),
		// Intentionally NO WithForce() — --force/yolo bypasses Cursor hooks.
		llmtypes.WithStreamingChan(streamChan),
	)
	<-streamDone

	if err := CleanupCursorCLIInteractiveSessions(context.Background()); err != nil {
		t.Fatalf("force-cleanup of persistent cursor session: %v", err)
	}
	if callErr != nil {
		t.Fatalf("GenerateContent error = %v\nstream so far:\n%s", callErr, streamContent.String())
	}

	haystack := streamContent.String()
	if resp != nil && len(resp.Choices) > 0 {
		haystack += "\n" + resp.Choices[0].Content
	}
	for _, forbiddenValue := range forbidden {
		if strings.Contains(haystack, forbiddenValue) {
			t.Fatalf("%s sentinel leaked into response — Cursor built-in tool succeeded despite deny policy\nsentinel=%s\nfull haystack:\n%s", label, forbiddenValue, haystack)
		}
	}

	denyAnchors := []string{
		"Built-in filesystem/shell/edit/search/delegation tools are disabled",
		"api-bridge",
		"subagent",
		"mode switch",
		"permission denied",
		"not allowed",
		"orchestrator",
		"denied",
		"disabled",
	}
	for _, anchor := range denyAnchors {
		if strings.Contains(strings.ToLower(haystack), strings.ToLower(anchor)) {
			return
		}
	}
	t.Fatalf("no deny evidence found for Cursor %s probe; expected one of %v\nfull haystack:\n%s", label, denyAnchors, haystack)
}
