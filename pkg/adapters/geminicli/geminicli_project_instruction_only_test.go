package geminicli

import (
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func applyGeminiOpts(t *testing.T, options ...llmtypes.CallOption) *llmtypes.CallOptions {
	t.Helper()
	opts := &llmtypes.CallOptions{}
	for _, o := range options {
		o(opts)
	}
	return opts
}

// TestGeminiProjectInstructionOnlyFromOptionsDefaultFalse verifies the flag is
// OFF when the metadata key was never set, so the GEMINI_SYSTEM_MD injection
// continues to fire by default.
func TestGeminiProjectInstructionOnlyFromOptionsDefaultFalse(t *testing.T) {
	if geminiProjectInstructionOnlyFromOptions(nil) {
		t.Fatal("expected false for nil opts")
	}
	if geminiProjectInstructionOnlyFromOptions(&llmtypes.CallOptions{}) {
		t.Fatal("expected false for empty opts")
	}
	opts := applyGeminiOpts(t, WithGeminiModel("gemini-2.5-pro"))
	if geminiProjectInstructionOnlyFromOptions(opts) {
		t.Fatal("expected false when flag never set")
	}
}

// TestGeminiProjectInstructionOnlyFromOptionsRoundTrip verifies
// WithProjectInstructionOnly(true)/(false) round-trips through the metadata.
func TestGeminiProjectInstructionOnlyFromOptionsRoundTrip(t *testing.T) {
	on := applyGeminiOpts(t, WithProjectInstructionOnly(true))
	if !geminiProjectInstructionOnlyFromOptions(on) {
		t.Fatal("expected true after WithProjectInstructionOnly(true)")
	}
	if on.Metadata.Custom[MetadataKeyProjectInstructionOnly] != true {
		t.Fatalf("expected metadata key set to true, got %v", on.Metadata.Custom[MetadataKeyProjectInstructionOnly])
	}

	off := applyGeminiOpts(t, WithProjectInstructionOnly(false))
	if geminiProjectInstructionOnlyFromOptions(off) {
		t.Fatal("expected false after WithProjectInstructionOnly(false)")
	}
}

// TestRemoveGeminiSystemMDEnv verifies only GEMINI_SYSTEM_MD entries are
// stripped and every other env entry (including same-prefix-looking keys) is
// preserved in order.
func TestRemoveGeminiSystemMDEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GEMINI_SYSTEM_MD=/tmp/gemini-system-abc.md",
		"GEMINI_CLI_TRUST_WORKSPACE=true",
		"GEMINI_SYSTEM_MD_OTHER=keepme",
		"GEMINI_PROJECT_DIR=/tmp/proj",
	}
	out := removeGeminiSystemMDEnv(in)

	for _, kv := range out {
		if kv == "GEMINI_SYSTEM_MD=/tmp/gemini-system-abc.md" {
			t.Fatal("GEMINI_SYSTEM_MD entry was not removed")
		}
	}
	want := []string{
		"PATH=/usr/bin",
		"GEMINI_CLI_TRUST_WORKSPACE=true",
		"GEMINI_SYSTEM_MD_OTHER=keepme",
		"GEMINI_PROJECT_DIR=/tmp/proj",
	}
	if len(out) != len(want) {
		t.Fatalf("expected %d entries, got %d: %v", len(want), len(out), out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("entry %d: want %q, got %q", i, want[i], out[i])
		}
	}
}

// TestRemoveGeminiSystemMDEnvNoMatch verifies env is returned unchanged when no
// GEMINI_SYSTEM_MD entry is present (the projection-disabled/failed fallback
// must keep the injection intact).
func TestRemoveGeminiSystemMDEnvNoMatch(t *testing.T) {
	in := []string{"PATH=/usr/bin", "GEMINI_CLI_TRUST_WORKSPACE=true"}
	out := removeGeminiSystemMDEnv(in)
	if len(out) != len(in) {
		t.Fatalf("expected %d entries, got %d", len(in), len(out))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("entry %d: want %q, got %q", i, in[i], out[i])
		}
	}
}
