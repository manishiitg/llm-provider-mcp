package picli

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func writePiMarkerFixture(t *testing.T, lines ...string) *piInteractiveSession {
	t.Helper()
	markerPath := t.TempDir() + "/markers.jsonl"
	if err := os.WriteFile(markerPath, []byte(strings.Join(append(lines, ""), "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	return &piInteractiveSession{tmuxSessionName: "missing-session", markerPath: markerPath}
}

// agent_end fires inside Pi's post-run loop; a mid-run agent_end must not end
// the turn with the pre-retry text.
func TestPiTurnEndsOnAgentSettledNotMidRunAgentEnd(t *testing.T) {
	session := writePiMarkerFixture(t,
		`{"type":"agent_start"}`,
		`{"type":"message_end","role":"assistant","text":"interim"}`,
		`{"type":"agent_end"}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","role":"assistant","text":"final answer"}`,
		`{"type":"agent_end"}`,
		`{"type":"agent_settled"}`,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := waitForPiInteractiveResponse(ctx, session, 0, nil)
	if err != nil || got != "final answer" || session.completionSource != "marker_agent_settled" {
		t.Fatalf("got (%q, %v, %q), want final answer via marker_agent_settled", got, err, session.completionSource)
	}
}

func TestPiTurnAgentEndQuietFallback(t *testing.T) {
	prev := piAgentEndQuietFallback
	piAgentEndQuietFallback = 300 * time.Millisecond
	t.Cleanup(func() { piAgentEndQuietFallback = prev })
	session := writePiMarkerFixture(t,
		`{"type":"message_end","role":"assistant","text":"done"}`,
		`{"type":"agent_end"}`,
	)
	// The wait also checks the tmux session is alive; give it a real one.
	session.tmuxSessionName = "mlp-pi-quiet-fallback-test"
	if err := exec.CommandContext(context.Background(), "tmux", "new-session", "-d", "-s", session.tmuxSessionName, "sleep", "30").Run(); err != nil {
		t.Skipf("tmux unavailable: %v", err)
	}
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", session.tmuxSessionName).Run() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := waitForPiInteractiveResponse(ctx, session, 0, nil)
	if err != nil || got != "done" || session.completionSource != "marker_agent_end_quiet" {
		t.Fatalf("got (%q, %v, %q), want done via marker_agent_end_quiet", got, err, session.completionSource)
	}
}
