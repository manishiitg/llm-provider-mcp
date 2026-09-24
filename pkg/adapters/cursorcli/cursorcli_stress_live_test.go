package cursorcli

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

var codingCLIStress = flag.Bool("coding-cli-stress", false, "run the opt-in live hybrid-mode stress scenario")

// TestCursorCLIStressReadOnlyHybrid: Cursor in "Native agent tools" mode has
// no subagents, so the stress is breadth in one turn: a todo list, native
// reads + a grep across several files, a slow MCP call, blocked native
// write/delete attempts and a mid-turn steer, then a follow-up turn on the
// retained session. Nothing may change on disk natively.
func TestCursorCLIStressReadOnlyHybrid(t *testing.T) {
	requireRealCursorCLIE2E(t)
	if !*codingCLIStress {
		t.Skip("opt-in: pass -coding-cli-stress")
	}
	iterations := 2
	if n, err := strconv.Atoi(os.Getenv("CODING_CLI_STRESS_ITERATIONS")); err == nil && n > 0 {
		iterations = n
	}
	for i := 1; i <= iterations; i++ {
		t.Run(fmt.Sprintf("iteration-%d", i), func(t *testing.T) {
			t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })
			workDir := t.TempDir()
			tokens := map[string]string{}
			for _, part := range []string{"A", "B", "C"} {
				tokens[part] = "PART-" + part + "-" + cursorRandomHex(4)
				if err := os.WriteFile(filepath.Join(workDir, "part-"+strings.ToLower(part)+".txt"), []byte(tokens[part]+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			needle := "NEEDLE-" + cursorRandomHex(4)
			hit := "HIT-" + cursorRandomHex(3)
			if err := os.MkdirAll(filepath.Join(workDir, "deep", "dir"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workDir, "deep", "dir", "notes.md"), []byte(needle+" "+hit+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(workDir, "victim.txt")
			if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mcpServerPath := writeCursorSlowContractMCPServer(t, filepath.Join(t.TempDir(), "slow-tool-started"))
			mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
			preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")
			owner := "cursor-stress-" + cursorRandomHex(4)
			opts := []llmtypes.CallOption{
				WithInteractiveSessionID(owner), WithPersistentInteractiveSession(true), WithWorkingDir(workDir),
				WithMCPConfig(mcpConfig), WithApproveMCPs(), WithReadOnlyHybridTools(),
			}
			bridgeToken := "BRIDGE_" + cursorRandomHex(4)
			steerToken := "STEER-" + cursorRandomHex(4)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			prompt := "Stress test in a disposable directory. 1) Write a todo list for this task. " +
				"2) With your built-in tools read part-a.txt, part-b.txt and part-c.txt, and grep the directory for " + needle + " noting the HIT token after it. " +
				"3) Call the api-bridge MCP tool contract_echo_token with token " + bridgeToken + " and delay_ms 8000. " +
				"4) Try once to create new-file.txt with your built-in Write tool and to delete victim.txt with your built-in Delete tool (report if refused). " +
				"5) Finally reply with exactly one line: A=<token> B=<token> C=<token> HIT=<hit token> MCP=<tool result>."
			started := time.Now()
			go func() {
				time.Sleep(20 * time.Second)
				sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer sendCancel()
				if err := SendCursorInteractiveInput(sendCtx, owner, "Additional requirement: append STEER="+steerToken+" to the end of your final line."); err != nil {
					t.Logf("steer send error: %v", err)
				}
			}()
			adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
			resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}}, opts...)
			if _, statErr := os.Stat(filepath.Join(workDir, "new-file.txt")); !os.IsNotExist(statErr) {
				t.Fatal("hybrid Cursor created a file natively")
			}
			if _, statErr := os.Stat(victim); os.IsNotExist(statErr) {
				t.Fatal("hybrid Cursor deleted a file natively")
			}
			if err != nil {
				t.Fatalf("turn 1: %v", err)
			}
			final := resp.Choices[0].Content
			source := ""
			if gi := resp.Choices[0].GenerationInfo; gi != nil {
				source, _ = gi.Additional["cursor_completion_source"].(string)
			}
			t.Logf("turn 1 in %s; completion_source=%s; final %.400s", time.Since(started).Round(time.Second), source, final)
			for part, tok := range tokens {
				if !strings.Contains(final, tok) {
					t.Fatalf("final missing %s=%s: %q", part, tok, final)
				}
			}
			for _, want := range []string{hit, bridgeToken} {
				if !strings.Contains(final, want) {
					t.Fatalf("final missing %s: %q", want, final)
				}
			}
			// Cursor answers a mid-turn steer as its own queued follow-up; the
			// turn result is the original request's answer. The steer must still
			// be honoured, proven from Cursor's store: an assistant message
			// carrying the steer token.
			if !strings.Contains(final, steerToken) && !cursorStoreHasAssistantText(t, workDir, started, steerToken, 90*time.Second) {
				t.Fatalf("mid-turn steer %s never answered (not in final or store)", steerToken)
			}
			resp2, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply with only the B token from your previous answer."}}}}, opts...)
			if err != nil {
				t.Fatalf("turn 2: %v", err)
			}
			if got := resp2.Choices[0].Content; !strings.Contains(got, tokens["B"]) || strings.Contains(got, tokens["A"]) {
				t.Fatalf("turn 2 = %q, want only %s", got, tokens["B"])
			}
		})
	}
}

// cursorStoreHasAssistantText polls this workdir's Cursor stores for an
// assistant message containing needle.
func cursorStoreHasAssistantText(t *testing.T, workDir string, since time.Time, needle string, wait time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		storeDB := freshestCursorStoreDBSince(workDir, since.Add(-time.Minute))
		if storeDB != "" {
			for _, msg := range readCursorRetainedInputAll(storeDB) {
				if strings.Contains(msg, needle) {
					return true
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// readCursorRetainedInputAll returns the text of every assistant message in
// the store's latest root.
func readCursorRetainedInputAll(storeDB string) []string {
	db, err := sql.Open("sqlite", "file:"+storeDB+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	ctx := context.Background()
	refs, err := cursorStoreLatestRootRefs(ctx, db)
	if err != nil {
		return nil
	}
	var texts []string
	for _, ref := range refs {
		var msg cursorMessage
		if json.Unmarshal(readCursorBlob(ctx, db, ref), &msg) != nil || !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			continue
		}
		for _, part := range cursorAssistantPartsFromContent(msg.Content) {
			if text, ok := part.(llmtypes.TextContent); ok {
				texts = append(texts, text.Text)
			}
		}
	}
	return texts
}
