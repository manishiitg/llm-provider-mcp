package picli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPiWorkspaceLeaseRejectsAliasConflict(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	first, err := acquirePiWorkspaceMCPConfigLease(dir, `{"mcpServers":{"one":{}}}`, &piInteractiveSession{})
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := acquirePiWorkspaceMCPConfigLease(alias, `{"mcpServers":{"two":{}}}`, &piInteractiveSession{})
	if second != nil {
		defer second()
	}
	if err == nil {
		t.Fatal("alias bypassed the workspace MCP configuration lease")
	}
}
