package cursorcli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCursorChatsRootsPrefersTheGivenHomeThenXDGThenLegacy(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	want := []string{
		// The CLI's own XDG home (confined or account-scoped Cursor) comes first: the server's XDG
		// folder can hold an older copy of the same chat.
		filepath.Join(home, ".config", "cursor", "chats"),
		filepath.Join(xdg, "cursor", "chats"),
		filepath.Join(home, ".cursor", "chats"),
	}
	if got := cursorChatsRoots(home); !reflect.DeepEqual(got, want) {
		t.Fatalf("cursorChatsRoots() = %v, want %v", got, want)
	}
}

func TestCursorStoreDBForNativeSessionFindsXDGTranscript(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	workingDir := filepath.Join(t.TempDir(), "workspace")
	sessionID := "native-session"
	dbPath := filepath.Join(xdg, "cursor", "chats", workingDirHashForCursor(workingDir), sessionID, "store.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := cursorStoreDBForNativeSession(home, workingDir, sessionID); got != dbPath {
		t.Fatalf("cursorStoreDBForNativeSession() = %q, want %q", got, dbPath)
	}
}

func TestCursorChatsRootsUsesLegacyWhenXDGIsUnsetOrRelative(t *testing.T) {
	home := t.TempDir()
	want := []string{filepath.Join(home, ".config", "cursor", "chats"), filepath.Join(home, ".cursor", "chats")}
	for _, xdg := range []string{"", "relative/config"} {
		t.Run(xdg, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", xdg)
			if got := cursorChatsRoots(home); !reflect.DeepEqual(got, want) {
				t.Fatalf("cursorChatsRoots() = %v, want %v", got, want)
			}
		})
	}
}

// A chat started before the lock has an older copy under the server's XDG folder; the live one is
// in the confined Cursor's private home and must win (its store was never found, so nothing streamed).
func TestCursorStoreDBForNativeSessionPrefersThePrivateHomeOverAStaleServerCopy(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	workingDir := filepath.Join(t.TempDir(), "workspace")
	sessionID := "native-session"
	hash := workingDirHashForCursor(workingDir)
	live := filepath.Join(home, ".config", "cursor", "chats", hash, sessionID, "store.db")
	stale := filepath.Join(xdg, "cursor", "chats", hash, sessionID, "store.db")
	for _, path := range []string{live, stale} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("db"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := cursorStoreDBForNativeSession(home, workingDir, sessionID); got != live {
		t.Fatalf("store = %q, want the private home's %q", got, live)
	}
}
