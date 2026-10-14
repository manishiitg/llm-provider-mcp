package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestNativeChildrenPreserveConversationWithinExactParent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	parent := "12345678-1234-1234-1234-123456789012"
	project := filepath.Join(home, ".claude", "projects", claudeTranscriptProjectSlug(cwd))
	childDir := filepath.Join(project, parent, "subagents")
	if err := os.MkdirAll(childDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, parent+".jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	trail := `{"type":"user","timestamp":"2026-10-14T01:00:00Z","message":{"content":"Read the evidence"}}
{"type":"assistant","timestamp":"2026-10-14T01:00:01Z","message":{"id":"a","content":[{"type":"tool_use","id":"read","name":"Read","input":{"file_path":"evidence.txt"}}]}}
{"type":"user","timestamp":"2026-10-14T01:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"read","content":"Recorded facts"}]}}
{"type":"assistant","timestamp":"2026-10-14T01:00:03Z","message":{"id":"b","stop_reason":"end_turn","content":[{"type":"text","text":"Evidence verified"}]}}
`
	if err := os.WriteFile(filepath.Join(childDir, "agent-child.jsonl"), []byte(trail), 0600); err != nil {
		t.Fatal(err)
	}
	children := readClaudeNativeSubagents(parent, cwd, home)
	if len(children) != 1 || children[0].Status != "completed" || len(children[0].Messages) != 4 {
		t.Fatalf("lost child conversation: %+v", children)
	}
	if children[0].Messages[0].Role != llmtypes.ChatMessageTypeHuman || children[0].Messages[2].Role != llmtypes.ChatMessageTypeTool {
		t.Fatal("child prompt or tool output missing")
	}
	if got := readClaudeNativeSubagents("87654321-4321-4321-4321-210987654321", cwd, home); len(got) != 0 {
		t.Fatal("children crossed native parent sessions")
	}
}
