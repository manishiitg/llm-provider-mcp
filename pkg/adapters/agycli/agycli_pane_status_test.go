package agycli

import (
	"strings"
	"testing"
)

func TestAgyPaneStatusIgnoresOldAnswerText(t *testing.T) {
	quoted := "The answer quotes Requesting permission for: and esc to cancel."
	pane := quoted + strings.Repeat("\nold output", 25) + "\n? for shortcuts\n> "
	if !PaneReadyForInput(pane) {
		t.Fatal("old busy text blocked a ready pane")
	}
	if marker := agyApprovalMarkerShown(pane); marker != "" {
		t.Fatalf("old approval quote blocked a ready pane: %q", marker)
	}
	if agyPaneShowsTrustGate(pane) {
		t.Fatal("old trust text blocked a ready pane")
	}
	blocked := quoted + strings.Repeat("\nold output", 25) + "\nRequesting permission for:\nAllow calling this tool?"
	if marker := agyApprovalMarkerShown(blocked); marker == "" {
		t.Fatal("active approval was missed")
	}
	if PaneReadyForInput(blocked) {
		t.Fatal("approval pane appeared ready")
	}
}
