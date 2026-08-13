package claudecode

import (
	"reflect"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestClaudeStructuredProcessEnvUsesAdapterOAuthToken(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=ambient-api-key",
		"ANTHROPIC_AUTH_TOKEN=ambient-auth-token",
		"CLAUDE_CODE_OAUTH_TOKEN=ambient-oauth-token",
		"KEEP=value",
	}
	opts := &llmtypes.CallOptions{}
	llmtypes.WithCodingAgentSecretEnvironment(map[string]string{
		"SECRET_WORKFLOW": "secret-value",
	})(opts)

	got := claudeStructuredProcessEnv(base, opts, "  workflow-oauth-token  ")
	want := []string{
		"PATH=/usr/bin",
		"KEEP=value",
		"SECRET_WORKFLOW=secret-value",
		"CLAUDE_CODE_OAUTH_TOKEN=workflow-oauth-token",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("structured Claude environment = %#v, want %#v", got, want)
	}
}

func TestClaudeStructuredProcessEnvWithoutAdapterTokenPreservesExistingAuth(t *testing.T) {
	base := []string{"PATH=/usr/bin", "CLAUDE_CODE_OAUTH_TOKEN=ambient-oauth-token"}
	got := claudeStructuredProcessEnv(base, nil, "  ")
	if !reflect.DeepEqual(got, base) {
		t.Fatalf("structured Claude environment = %#v, want unchanged %#v", got, base)
	}
}
