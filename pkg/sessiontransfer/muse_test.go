package sessiontransfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Transfers cannot turn journal names or filesystem links into writes outside
// the selected account store, even when the root journal itself is valid.
func TestMuseNativeHandoffRejectsPathsLinksAndForeignSession(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	workspace := t.TempDir()
	row := map[string]any{"stream": map[string]string{"kind": "session", "id": id}, "payload_type": "runtime.session.metadata", "payload": map[string]any{"record": map[string]any{"workspace_root": workspace, "build": map[string]string{"semver": "1.4.14"}}}}
	raw, _ := json.Marshal(row)
	raw = append(raw, '\n')
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	descriptor, _, err := inspectMuseJournal(path, true, id)
	if err != nil {
		t.Fatal(err)
	}
	b := &Bundle{Provider: "muse-cli", Format: "muse-jsonl", Transport: "structured", SessionID: id, Date: "2026/11/01", Workspace: workspace, Root: root, Files: map[string]File{"session.jsonl": descriptor}}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	b.Files["../../auth.json"] = descriptor
	if err := b.Validate(); err == nil {
		t.Fatal("accepted a credential path")
	}
	delete(b.Files, "../../auth.json")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), id, "22222222-2222-4222-8222-222222222222")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.ValidateFiles(); err == nil {
		t.Fatal("accepted a different native session")
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	lockHome := t.TempDir()
	if err := ImportMuse(lockHome, workspace, b); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(lockHome, "muse", "sessions", "2026", "11", "01", id, ".session.lock")
	unlock, err := lockMuseSession(lockHome, locked)
	if err != nil {
		t.Fatal(err)
	}
	if err := ImportMuse(lockHome, workspace, b); err == nil || !strings.Contains(err.Error(), "still using this session") {
		unlock()
		t.Fatalf("active writer was not excluded: %v", err)
	}
	unlock()

	home, outside := t.TempDir(), t.TempDir()
	parent := filepath.Join(home, "muse", "sessions", "2026", "11")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, "01")); err != nil {
		t.Fatal(err)
	}
	if err := ImportMuse(home, workspace, b); err == nil {
		t.Fatal("followed a journal directory symlink")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("import wrote outside the store")
	}
}
