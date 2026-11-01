package musecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-856-442: use the installed CLI, including its permission-frame replay,
// to prove identity survives both directions, and divergence never overwrites.
func TestMuseNativeHandoffPreservesIDAndRejectsDivergence(t *testing.T) {
	requireMuseBinary(t)
	museEchoTestEnv(t)
	a := NewMuseCLIAdapter("", "muse-cli", museTestLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	from, to := t.TempDir(), t.TempDir()
	homeA, homeB := os.Getenv("XDG_DATA_HOME"), t.TempDir()
	turn := func(home, dir, id, text string) (string, error) {
		t.Setenv("XDG_DATA_HOME", home)
		response, err := a.GenerateContent(ctx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Current test workspace: " + dir}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: text}}},
		}, WithMuseStructuredTransport(true), WithWorkingDir(dir), WithResumeSessionID(id))
		if err != nil {
			return "", err
		}
		return response.Choices[0].GenerationInfo.CodingProviderSessionHandle.NativeSessionID, nil
	}
	id, err := turn(homeA, from, "", "FIRST")
	if err != nil {
		t.Fatal(err)
	}
	first, err := ExportNativeSession(homeA, id, from)
	if err != nil {
		t.Fatal(err)
	}
	if err = ImportNativeSession(homeB, to, first); err != nil {
		t.Fatal(err)
	}
	if got, err := turn(homeB, to, id, "SECOND"); err != nil || got != id {
		t.Fatalf("destination native ID=%q err=%v", got, err)
	}
	if _, err = turn(homeB, t.TempDir(), id, "WRONG_WORKSPACE"); err == nil {
		t.Fatal("handoff allowed a different destination folder")
	}
	second, err := ExportNativeSession(homeB, id, to)
	if err != nil {
		t.Fatal(err)
	}
	if err = ImportNativeSession(homeA, from, second); err != nil {
		t.Fatal(err)
	}
	if got, err := turn(homeA, from, id, "THIRD"); err != nil || got != id {
		t.Fatalf("return native ID=%q err=%v", got, err)
	}
	path := museSessionLogPath(id, homeA)
	before, _ := os.ReadFile(path)
	// PR59: importing must refuse a live writer rather than replace its inode.
	unlock, err := lockNativeHandoff(homeA, filepath.Join(filepath.Dir(path), ".session.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ImportNativeSession(homeA, from, second); err == nil || !strings.Contains(err.Error(), "still using this session") {
		unlock()
		t.Fatalf("active writer was not excluded: %v", err)
	}
	unlock()
	if err = ImportNativeSession(homeA, from, first); err == nil {
		t.Fatal("older divergent session overwrote new local turns")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("refused import changed the journal")
	}
	marker := filepath.Join(filepath.Dir(path), "agentworks-handoff.json")
	info, err := os.Stat(marker)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private handoff binding: %v %v", info, err)
	}
}

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
	descriptor, _, err := inspectNativeJournal(path, true, id)
	if err != nil {
		t.Fatal(err)
	}
	b := &NativeSessionBundle{SessionID: id, Date: "2026/11/01", Workspace: workspace, Root: root, Files: map[string]NativeSessionFile{"session.jsonl": descriptor}}
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
	home, outside := t.TempDir(), t.TempDir()
	parent := filepath.Join(home, "muse", "sessions", "2026", "11")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, "01")); err != nil {
		t.Fatal(err)
	}
	if err := ImportNativeSession(home, workspace, b); err == nil {
		t.Fatal("followed a journal directory symlink")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("import wrote outside the store")
	}
}
