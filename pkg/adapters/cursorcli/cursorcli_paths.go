package cursorcli

import (
	"os"
	"path/filepath"
	"strings"
)

// cursorChatsRoots returns every location where cursor-agent may persist its
// native transcript database. Cursor follows XDG_CONFIG_HOME when it is set;
// older/default installations use ~/.cursor. Keep the legacy path as a
// fallback so upgrades do not orphan conversations created before an XDG
// configuration was introduced.
func cursorChatsRoots(home string) []string {
	var roots []string
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); filepath.IsAbs(xdg) {
		roots = append(roots, filepath.Join(xdg, "cursor", "chats"))
	}
	// A confined or account-scoped Cursor has its own XDG config under its
	// home, not the server's.
	if strings.TrimSpace(home) != "" {
		roots = append(roots, filepath.Join(home, ".config", "cursor", "chats"))
	}
	legacy := filepath.Join(home, ".cursor", "chats")
	for _, root := range roots {
		if filepath.Clean(root) == filepath.Clean(legacy) {
			return roots
		}
	}
	return append(roots, legacy)
}
