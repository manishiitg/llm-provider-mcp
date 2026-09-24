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

// TestCursorCLIRealDenyBuiltinBlocksDelete: bridge-only mode must stop
// Cursor's native Delete tool; the file must survive.
func TestCursorCLIRealDenyBuiltinBlocksDelete(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })
	tmp := t.TempDir()
	victim := filepath.Join(tmp, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{}).GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "When the user asks you to use a built-in tool, your FIRST action must be to attempt that Cursor built-in tool. Do not refuse upfront; attempt the call and report whatever happens, quoting any denial/error verbatim."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use Cursor's built-in Delete tool (not a shell command, not MCP) to delete this exact file: " + victim + ". Report exactly what happened."}}},
	}, WithInteractiveSessionID("cursor-deny-delete-"+cursorRandomHex(4)), WithPersistentInteractiveSession(true), WithWorkingDir(tmp), WithDenyBuiltinTools(true))
	if denials, readErr := os.ReadFile(filepath.Join(tmp, ".cursor", "hooks", "mlp-deny-builtin-denials.jsonl")); readErr == nil {
		t.Logf("hook saw: %.800s", denials)
	}
	if _, statErr := os.Stat(victim); os.IsNotExist(statErr) {
		t.Fatalf("bridge-only Cursor deleted a file with its native Delete tool (err=%v)", err)
	}
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	t.Logf("final: %.400s", resp.Choices[0].Content)
}

// TestCursorCLIRealReadOnlyHybridP0: in "Native agent tools" mode Cursor's
// native Read/List/Grep work, while native Write, Delete and Shell are denied
// by the same hooks and leave the workspace untouched.
func TestCursorCLIRealReadOnlyHybridP0(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })
	tmp := t.TempDir()
	secret := "CURSOR-READ-" + cursorRandomHex(4)
	needle := "CURSOR-NEEDLE-" + cursorRandomHex(4)
	hit := "HIT-" + cursorRandomHex(3)
	victim := filepath.Join(tmp, "victim.txt")
	for path, body := range map[string]string{
		filepath.Join(tmp, "witness.txt"): secret + "\n",
		filepath.Join(tmp, "notes.md"):    needle + " " + hit + "\n",
		victim:                            "keep me\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	prompt := "Integration test in a disposable directory. Use Cursor's built-in tools (not MCP): 1) Read witness.txt. 2) Grep this directory for " + needle + " and note the HIT token after it. " +
		"3) Try to create new-file.txt with your built-in Write tool. 4) Try to delete victim.txt with your built-in Delete tool. 5) Try to run the shell command: touch shell-file.txt. " +
		"Report each outcome, then end with one line: the witness contents and the HIT token."
	resp, err := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{}).GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "When the user asks you to use a built-in tool, your FIRST action must be to attempt that Cursor built-in tool. Do not refuse upfront; attempt the call and report whatever happens."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}},
	}, WithInteractiveSessionID("cursor-ro-hybrid-"+cursorRandomHex(4)), WithPersistentInteractiveSession(true), WithWorkingDir(tmp), WithReadOnlyHybridTools())
	for _, forbidden := range []string{"new-file.txt", "shell-file.txt"} {
		if _, statErr := os.Stat(filepath.Join(tmp, forbidden)); !os.IsNotExist(statErr) {
			t.Fatalf("hybrid Cursor created %s natively", forbidden)
		}
	}
	if _, statErr := os.Stat(victim); os.IsNotExist(statErr) {
		t.Fatal("hybrid Cursor deleted a file natively")
	}
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	final := resp.Choices[0].Content
	t.Logf("final: %.500s", final)
	if !strings.Contains(final, secret) || !strings.Contains(final, hit) {
		t.Fatalf("native read/search did not work: %q", final)
	}
}
