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
