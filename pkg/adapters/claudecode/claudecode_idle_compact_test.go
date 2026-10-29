package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

// PLAT-872 idle compaction on tmux: the compaction record is read from the
// transcript rows written after /compact was pasted. A row Claude is still
// writing (no newline yet) is read on the next poll, and a subagent's
// boundary is not the chat's.
func TestReadClaudeCompactBoundaryWaitsForCompleteMainChainRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	old := `{"type":"system","subtype":"compact_boundary","compactMetadata":{"preTokens":1,"postTokens":1}}` + "\n"
	sidechain := `{"type":"system","subtype":"compact_boundary","isSidechain":true,"compactMetadata":{"preTokens":2,"postTokens":2}}` + "\n"
	boundary := `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":310000,"postTokens":42000}}`
	if err := os.WriteFile(path, []byte(old+sidechain+boundary), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(old))
	if _, found, next := readClaudeCompactBoundary(path, offset); found || next != offset+int64(len(sidechain)) {
		t.Fatalf("partial row: found=%t next=%d", found, next)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n")
	_ = f.Close()
	meta, found, _ := readClaudeCompactBoundary(path, offset+int64(len(sidechain)))
	if pre, post, _ := meta.normalized(); !found || pre != 310000 || post != 42000 {
		t.Fatalf("found=%t pre=%d post=%d", found, pre, post)
	}
}
