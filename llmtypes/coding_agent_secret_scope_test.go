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

// MCP_* are transport routes, not per-child credentials, and over-filtering
// them previously produced a silent failure where the child never saw
// MCP_CUSTOM and nothing errored. They must survive a declared scope that
// does not mention them.
func TestMergeKeepsAmbientMCPRoutes(t *testing.T) {
	ambient := []string{"MCP_API_URL=http://bridge.local", "MCP_CUSTOM=http://bridge.local/custom"}
	opts := scopedEnvOpts(map[string]string{"SECRET_MINE": "v"})

	got := envMap(MergeCodingAgentSecretEnvironment(ambient, opts))

	if got["MCP_API_URL"] != "http://bridge.local" || got["MCP_CUSTOM"] != "http://bridge.local/custom" {
		t.Fatalf("transport routes were scrubbed, which silently breaks the bridge: %v", got)
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
