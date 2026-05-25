package opencodecli

import (
	"os"
	"strings"
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

// freeTierTestModel returns the model id real-binary tests should pass
// to NewOpenCodeCLIAdapter so the whole package runs key-free by
// default. opencode's hosted "deepseek-v4-flash-free" is part of the
// opencode-cli-free sub-provider tile: no API key required, supports
// tool use, fast enough for short prompts. Tests that need a specific
// paid model can set OPENCODE_CLI_REAL_E2E_MODEL to override.
//
// Why this exists: when these tests used the bare "opencode-cli" tile
// they fell through to opencode's locally-configured default
// (~/.opencode/opencode.jsonc), which usually points at a paid model
// and required an API key in the env. Pinning to the free tier here
// means anyone with opencode installed can run the full real-test
// suite with RUN_OPENCODE_CLI_REAL_E2E=1 alone.
//
// Sub-provider tile tests (opencodecli_subproviders_real_test.go) are
// the exception — those exist specifically to validate per-key
// routing for Kimi/DeepSeek/Qwen/MiniMax/GLM and intentionally require
// the matching paid keys; they do not call this helper.
func freeTierTestModel() string {
	if override := strings.TrimSpace(os.Getenv("OPENCODE_CLI_REAL_E2E_MODEL")); override != "" {
		return override
	}
	return "opencode/deepseek-v4-flash-free"
}
