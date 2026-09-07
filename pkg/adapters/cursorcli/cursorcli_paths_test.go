package cursorcli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCursorChatsRootsPrefersXDGAndKeepsLegacyFallback(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	want := []string{
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
	want := []string{filepath.Join(home, ".cursor", "chats")}
	for _, xdg := range []string{"", "relative/config"} {
		t.Run(xdg, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", xdg)
			if got := cursorChatsRoots(home); !reflect.DeepEqual(got, want) {
				t.Fatalf("cursorChatsRoots() = %v, want %v", got, want)
			}
		})
	}
}
