package codexcli

import (
	"strings"
	"testing"
)

// PLAT-746-941: diagnostics must identify changed categories without logging
// secret-bearing env, system text or dynamic custom metadata names/values.
func TestCodexWarmTraceNeverPrintsFingerprintInputs(t *testing.T) {
	old := codexFingerprintParts(map[string]any{"system": "first", "env": map[string]string{"SECRET": "first"}, "private-key": "first"})
	current := codexFingerprintParts(map[string]any{"system": "second", "env": map[string]string{"SECRET": "second"}, "private-key": "second"})
	got := codexFingerprintChanges(old, current)
	if got != "system,env,custom" {
		t.Fatalf("categories=%q", got)
	}
	for _, private := range []string{"SECRET", "private-key", "first", "second"} {
		if strings.Contains(got, private) {
			t.Fatal("private fingerprint input exposed")
		}
	}
	if codexFingerprintChanges(current, current) != "none" {
		t.Fatal("unchanged fingerprint reported changed")
	}
}
