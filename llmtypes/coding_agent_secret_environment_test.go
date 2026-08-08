package llmtypes

import "testing"

func TestCodingAgentSecretEnvironmentIsScopedAndDoesNotMutateBase(t *testing.T) {
	base := []string{"PATH=/bin", "SECRET_PRESENT=old", "OTHER=value"}
	opts := &CallOptions{}
	WithCodingAgentSecretEnvironment(map[string]string{
		"SECRET_PRESENT": "new",
		"SECRET_ONLY":    "value",
		"MCP_CUSTOM":     "http://127.0.0.1/s/session/tools/custom",
		"PATH":           "must-not-pass",
	})(opts)

	got := MergeCodingAgentSecretEnvironment(base, opts)
	if base[1] != "SECRET_PRESENT=old" {
		t.Fatal("base environment was mutated")
	}
	joined := ""
	for _, item := range got {
		joined += item + "\n"
	}
	for _, want := range []string{"PATH=/bin", "SECRET_PRESENT=new", "SECRET_ONLY=value", "MCP_CUSTOM=http://127.0.0.1/s/session/tools/custom"} {
		if !containsEnvironmentEntry(joined, want) {
			t.Fatalf("merged environment missing %q: %q", want, joined)
		}
	}
	if containsEnvironmentEntry(joined, "PATH=must-not-pass") {
		t.Fatalf("non-secret environment entry escaped: %q", joined)
	}
}

func containsEnvironmentEntry(environment, entry string) bool {
	for _, line := range splitEnvironment(environment) {
		if line == entry {
			return true
		}
	}
	return false
}

func splitEnvironment(environment string) []string {
	var out []string
	start := 0
	for i := 0; i < len(environment); i++ {
		if environment[i] == '\n' {
			out = append(out, environment[start:i])
			start = i + 1
		}
	}
	return out
}
