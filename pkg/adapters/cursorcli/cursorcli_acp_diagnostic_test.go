package cursorcli

import (
	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"testing"
)

// PLAT-872: Cursor's plan refusal must not pass as a completed answer.
func TestCursorACPPlanRefusalIsNotASuccessfulAnswer(t *testing.T) {
	err := cursorACPPlanFailure("Upgrade your plan to continue", "selected-model")
	if llmerrors.KindOf(err) != llmerrors.KindBilling || llmerrors.IsRetryable(err) {
		t.Fatalf("plan refusal must require account action, not a reset wait: %v", err)
	}
	for _, text := range []string{"The UI says Upgrade your plan to continue when the account is limited.", "\"Upgrade your plan to continue\"", "Done."} {
		if err := cursorACPPlanFailure(text, "selected-model"); err != nil {
			t.Fatalf("normal answer misclassified: %v", err)
		}
	}
}

// PLAT-872: stderr writes can split the model error anywhere. Only the fixed
// diagnostic is retained; unrelated stderr must not become a model rejection.
func TestCursorModelRejectionDiagnosticAcrossPipeChunks(t *testing.T) {
	const diagnostic = "Cannot use this model: unavailable. Available models: composer-2.5"
	for split := 0; split <= len(diagnostic); split++ {
		d := &cursorACPLaunchDiagnostic{}
		_, _ = d.Write([]byte(diagnostic[:split]))
		_, _ = d.Write([]byte(diagnostic[split:]))
		if !d.modelUnavailable {
			t.Fatalf("lost model rejection at byte %d", split)
		}
	}
	d := &cursorACPLaunchDiagnostic{}
	_, _ = d.Write([]byte("unrelated private diagnostic"))
	if d.modelUnavailable {
		t.Fatal("unrelated process failure classified as model rejection")
	}
}
