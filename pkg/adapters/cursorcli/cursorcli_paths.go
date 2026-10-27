package cursorcli

import (
	"os"
	"path/filepath"
	"strings"
)

// Native ACP sessions are addressed by the selected account and exact session ID.
func cursorACPStoredSessionPath(home, native string) string {
	if native == "" || strings.ContainsAny(native, `/\\`) {
		return ""
	}
	roots := []string{filepath.Join(home, ".cursor"), filepath.Join(home, ".config", "cursor")}
	if ambient, _ := os.UserHomeDir(); home == ambient {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
			roots = append([]string{filepath.Join(xdg, "cursor")}, roots...)
		}
	}
	for _, root := range roots {
		path := filepath.Join(root, "acp-sessions", native, "store.db")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func cursorChatsRoots(home string) []string {
	var roots []string
	// The given home's own folders come first. A confined or account-scoped Cursor keeps its chats
	// under its own home; the server's XDG folder can hold an older copy of the same chat (one
	// started before the lock), and preferring it read a stale store and streamed nothing
	// (RTS 2026-09-30).
	if strings.TrimSpace(home) != "" {
		roots = append(roots, filepath.Join(home, ".config", "cursor", "chats"))
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); filepath.IsAbs(xdg) {
		roots = append(roots, filepath.Join(xdg, "cursor", "chats"))
	}
	legacy := filepath.Join(home, ".cursor", "chats")
	for _, root := range roots {
		if filepath.Clean(root) == filepath.Clean(legacy) {
			return roots
		}
	}
	return append(roots, legacy)
}
