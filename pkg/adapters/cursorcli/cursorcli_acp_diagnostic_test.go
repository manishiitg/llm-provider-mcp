package cursorcli

import "testing"

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
