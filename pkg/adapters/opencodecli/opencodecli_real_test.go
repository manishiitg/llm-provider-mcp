package opencodecli

import (
	"os"
	"testing"
)

func requireRealOpenCodeCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_OPENCODE_CLI_REAL_E2E") == "" {
		t.Skip("set RUN_OPENCODE_CLI_REAL_E2E=1 to run real OpenCode CLI e2e tests")
	}
	if _, err := opencodeBinaryPath(); err != nil {
		t.Fatalf("real OpenCode CLI tests require opencode: %v", err)
	}
}
