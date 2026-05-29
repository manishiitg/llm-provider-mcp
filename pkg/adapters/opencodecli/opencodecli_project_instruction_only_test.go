package opencodecli

import (
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestOpenCodeProjectInstructionOnlyOptionThreadsThroughMetadata asserts the
// public WithProjectInstructionOnly option puts the bool on the metadata under
// the expected key so projectInstructionOnlyFromOptions reads it back.
func TestOpenCodeProjectInstructionOnlyOptionThreadsThroughMetadata(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithProjectInstructionOnly(true)(opts)
	if opts.Metadata == nil || opts.Metadata.Custom == nil {
		t.Fatal("WithProjectInstructionOnly must initialize metadata")
	}
	enabled, ok := opts.Metadata.Custom[MetadataKeyProjectInstructionOnly].(bool)
	if !ok || !enabled {
		t.Errorf("expected MetadataKeyProjectInstructionOnly=true on metadata; got %v ok=%v", opts.Metadata.Custom[MetadataKeyProjectInstructionOnly], ok)
	}
}

// TestProjectInstructionOnlyFromOptions covers the helper's three states:
// unset (default off), explicitly true, explicitly false.
func TestProjectInstructionOnlyFromOptions(t *testing.T) {
	t.Run("unset defaults to false", func(t *testing.T) {
		if projectInstructionOnlyFromOptions(&llmtypes.CallOptions{}) {
			t.Fatal("unset key must read as false")
		}
		if projectInstructionOnlyFromOptions(nil) {
			t.Fatal("nil opts must read as false")
		}
	})
	t.Run("explicit true", func(t *testing.T) {
		opts := &llmtypes.CallOptions{}
		WithProjectInstructionOnly(true)(opts)
		if !projectInstructionOnlyFromOptions(opts) {
			t.Fatal("explicit true must read as true")
		}
	})
	t.Run("explicit false", func(t *testing.T) {
		opts := &llmtypes.CallOptions{}
		WithProjectInstructionOnly(false)(opts)
		if projectInstructionOnlyFromOptions(opts) {
			t.Fatal("explicit false must read as false")
		}
	})
}

// TestOpenCodeInBandPrefixGate exercises the exact suppression predicate used
// in generateContentStructured to decide whether the in-band
// "[System Instructions]" prefix is applied. The in-band prefix is SKIPPED
// only when BOTH the project-instruction-only flag is on AND the AGENTS.md
// projection write actually succeeded (projectedToInstructionFile). Otherwise
// the prefix fires so the prompt is never silently dropped.
func TestOpenCodeInBandPrefixGate(t *testing.T) {
	// suppress mirrors the negation inside the `if` guard:
	//   apply in-band prefix UNLESS (flagOn && projected)
	suppress := func(flagOn, projected bool) bool {
		opts := &llmtypes.CallOptions{}
		WithProjectInstructionOnly(flagOn)(opts)
		return projectInstructionOnlyFromOptions(opts) && projected
	}

	cases := []struct {
		name           string
		flagOn         bool
		projected      bool
		wantSuppressed bool // true => in-band prefix dropped (AGENTS.md is sole carrier)
	}{
		{"flag off, projected: prefix fires (default double path is the old behavior, flag off keeps it)", false, true, false},
		{"flag off, not projected: prefix fires", false, false, false},
		{"flag on, projected: prefix suppressed (sole carrier = AGENTS.md)", true, true, true},
		{"flag on, projection failed/skipped: prefix fires (fallback, never drop prompt)", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := suppress(tc.flagOn, tc.projected); got != tc.wantSuppressed {
				t.Fatalf("suppress(flagOn=%v, projected=%v) = %v, want %v", tc.flagOn, tc.projected, got, tc.wantSuppressed)
			}
		})
	}
}
