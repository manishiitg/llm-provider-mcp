package llmtypes

import (
	"strings"
	"testing"
)

func scopedEnvOpts(environment map[string]string) *CallOptions {
	opts := &CallOptions{}
	WithCodingAgentSecretEnvironment(environment)(opts)
	return opts
}

func envMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key, value, found := strings.Cut(entry, "="); found {
			out[key] = value
		}
	}
	return out
}

// The isolation guarantee is about the environment a child process actually
// receives. Overlaying declared values on os.Environ() without removing
// UNDECLARED scoped credentials left that guarantee untrue: a child could read
// whatever secrets happened to be in the launcher's environment.
func TestMergeDropsUndeclaredAmbientCredentials(t *testing.T) {
	ambient := []string{
		"PATH=/usr/bin",
		"SECRET_OTHER_TENANT=leaked-value",
		"VAR_SOMEONE_ELSES_CONFIG=leaked-config",
		"SECRET_MINE=stale-ambient",
	}
	opts := scopedEnvOpts(map[string]string{"SECRET_MINE": "declared-value"})

	got := envMap(MergeCodingAgentSecretEnvironment(ambient, opts))

	if _, leaked := got["SECRET_OTHER_TENANT"]; leaked {
		t.Fatalf("undeclared ambient secret reached the child: %v", got)
	}
	if _, leaked := got["VAR_SOMEONE_ELSES_CONFIG"]; leaked {
		t.Fatalf("undeclared ambient VAR_ reached the child: %v", got)
	}
	if got["SECRET_MINE"] != "declared-value" {
		t.Fatalf("declared value did not win over the ambient one: %q", got["SECRET_MINE"])
	}
	if got["PATH"] != "/usr/bin" {
		t.Fatalf("an unrelated ambient variable was scrubbed: %v", got)
	}
}

// Address-style MCP routes are still inherited: over-filtering them previously
// produced a silent failure where the child never saw MCP_CUSTOM and nothing
// errored, and an address grants nothing on its own.
func TestMergeKeepsAmbientMCPAddressRoutes(t *testing.T) {
	ambient := []string{
		"MCP_API_URL=http://bridge.local",
		"MCP_CUSTOM=http://bridge.local/custom",
		"MCP_MCP=http://bridge.local/mcp",
		"MCP_VIRTUAL=http://bridge.local/virtual",
	}
	opts := scopedEnvOpts(map[string]string{"SECRET_MINE": "v"})

	got := envMap(MergeCodingAgentSecretEnvironment(ambient, opts))

	for _, key := range []string{"MCP_API_URL", "MCP_CUSTOM", "MCP_MCP", "MCP_VIRTUAL"} {
		if got[key] == "" {
			t.Fatalf("address route %s was scrubbed, which silently breaks the bridge: %v", key, got)
		}
	}
}

// The credential half of the MCP_* prefix is NOT routing metadata: an ambient
// bearer token or session id lets a child act with another session's authority
// and binding. Treating the whole prefix as inheritable was the gap.
func TestMergeDropsAmbientMCPCredentialsAndSessionIdentity(t *testing.T) {
	ambient := []string{
		"MCP_API_URL=http://bridge.local",
		"MCP_API_TOKEN=another-sessions-token",
		"MCP_AUTH=Bearer another-sessions-token",
		"MCP_SESSION_ID=another-session",
	}
	opts := scopedEnvOpts(map[string]string{"SECRET_MINE": "v"})

	got := envMap(MergeCodingAgentSecretEnvironment(ambient, opts))

	for _, key := range []string{"MCP_API_TOKEN", "MCP_AUTH", "MCP_SESSION_ID"} {
		if _, leaked := got[key]; leaked {
			t.Fatalf("child inherited another session's %s: %v", key, got)
		}
	}
	if got["MCP_API_URL"] != "http://bridge.local" {
		t.Fatalf("scrubbing credentials must not take the address route with it: %v", got)
	}
}

// A caller that DOES declare its own session credentials must get exactly
// those, not the ambient ones.
func TestMergeDeclaredMCPCredentialsWinOverAmbient(t *testing.T) {
	ambient := []string{"MCP_API_TOKEN=another-sessions-token", "MCP_SESSION_ID=another-session"}
	opts := scopedEnvOpts(map[string]string{"MCP_API_TOKEN": "my-token", "MCP_SESSION_ID": "my-session"})

	got := envMap(MergeCodingAgentSecretEnvironment(ambient, opts))

	if got["MCP_API_TOKEN"] != "my-token" || got["MCP_SESSION_ID"] != "my-session" {
		t.Fatalf("declared session credentials did not win: %v", got)
	}
}

// With nothing declared the caller has opted out of scoping entirely, so the
// environment must pass through untouched -- scrubbing here would break every
// existing caller that never used this feature.
func TestMergeWithoutADeclaredScopeIsPassthrough(t *testing.T) {
	ambient := []string{"PATH=/usr/bin", "SECRET_AMBIENT=kept"}
	got := envMap(MergeCodingAgentSecretEnvironment(ambient, &CallOptions{}))
	if got["SECRET_AMBIENT"] != "kept" || got["PATH"] != "/usr/bin" {
		t.Fatalf("passthrough changed the environment: %v", got)
	}
}
