package cursorcli

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCursorGitMarkerPreservesExistingMetadata(t *testing.T) {
	for _, kind := range []string{"directory", "worktree-file"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			gitPath := filepath.Join(dir, ".git")
			sentinel := gitPath
			if kind == "directory" {
				if err := os.Mkdir(gitPath, 0700); err != nil {
					t.Fatal(err)
				}
				sentinel = filepath.Join(gitPath, "config")
			}
			const original = "existing repository metadata\n"
			if err := os.WriteFile(sentinel, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			if err := initCursorWorkspaceGitMarker(dir); err == nil {
				t.Error("must refuse pre-existing .git even when git cannot recognize it")
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || string(got) != original {
				t.Fatalf("repository metadata changed: %q, %v", got, err)
			}
		})
	}
}

func TestCursorGitRootAndTranscriptHashCaseAlias(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agentworks")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(dir), "AgentWorks")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("filesystem has case-sensitive directory names")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if !cursorWorkingDirIsGitRoot(alias) {
		t.Error("same filesystem directory rejected as Git root")
	}
	if workingDirHashForCursor(alias) != workingDirHashForCursor(dir) {
		t.Error("same physical cwd produces different transcript hashes")
	}
}

func TestCursorMissingKnownSessionDoesNotAdoptAnotherTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeCursorStoreFixture(t, dir, "unrelated-session", []string{`{"role":"assistant","content":[{"type":"text","text":"wrong chat"}]}`})
	messages, path := readCursorTranscriptMessagesAndStoreDB(time.Now(), dir, "owner", "missing-known-session")
	if path != "" || len(messages) != 0 {
		t.Fatalf("missing saved session adopted another transcript: %s", path)
	}
}

func TestCursorLegacyTranscriptHashWithExactSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "linked-workspace")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	db := writeCursorStoreFixture(t, dir, "saved-session", []string{`{"role":"assistant","content":[{"type":"text","text":"saved answer"}]}`})
	home, _ := os.UserHomeDir()
	hash := md5.Sum([]byte(alias))
	legacy := filepath.Join(home, ".cursor", "chats", hex.EncodeToString(hash[:]), "saved-session", "store.db")
	if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(db, legacy); err != nil {
		t.Fatal(err)
	}
	transcript, ok, err := ReadNativeTranscript(alias, "saved-session")
	if err != nil || !ok || transcript.Path != legacy {
		t.Fatalf("legacy transcript was lost: ok=%v err=%v path=%s", ok, err, transcript.Path)
	}
	messages, path := readCursorTranscriptMessagesAndStoreDB(time.Now(), alias, "owner", "saved-session")
	if len(messages) != 1 || path != legacy {
		t.Fatal("resumed turn lost its legacy transcript")
	}
}

func TestCursorCleanupPreservesUnrelatedFilesAndRestoreOption(t *testing.T) {
	dir := t.TempDir()
	cursor := filepath.Join(dir, ".cursor")
	if err := os.Mkdir(cursor, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(cursor, "user-notes")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(cursor, "cli.json")
	original := `{"user":"original"}`
	if err := os.WriteFile(config, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	opts := &llmtypes.CallOptions{}
	WithRestoreProjectFiles(true)(opts)
	WithMCPConfig(`{"mcpServers":{"fixture":{"command":"fixture-only"}}}`)(opts)
	cleanup, err := prepareCursorProjectFiles(dir, "fixture", opts, "fixture-owner")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	for path, want := range map[string]string{unrelated: "keep", config: original} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("cleanup changed %s: %v", path, err)
		}
	}
}

func TestCursorResumedStreamDoesNotAdoptAnotherTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	state := newCursorTranscriptStreamState(time.Now(), dir, "stream-owner", "missing-saved-session")
	writeCursorStoreFixture(t, dir, "unrelated-session", []string{`{"role":"assistant","content":[{"type":"text","text":"wrong chat"}]}`})
	chunks := make(chan llmtypes.StreamChunk, 10)
	state.poll(context.Background(), chunks)
	if len(chunks) != 0 {
		t.Fatal("resumed stream emitted a different session's response")
	}
}
