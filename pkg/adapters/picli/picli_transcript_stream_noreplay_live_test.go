package picli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestPiMarkerStreamNoHistoryReplayLive is the live counterpart of
// TestPiMarkerOffsetDoesNotReplayHistoryOnFreshProcess: a real pi turn
// commits real history to a real marker file, then a freshly computed
// start offset (file size snapshotted from disk after the turn — exactly
// what a restarted process computes) must not re-read it. The deterministic
// test proves the offset mechanism against a synthetic marker file; this
// proves the real CLI writes its markers in the shape the mechanism assumes.
// The marker file is settled to quiescence first: unlike the timestamp
// filters, an offset boundary races bytes appended after the snapshot.
//
// Gated behind -coding-cli-p0-live; requires a real pi CLI, node, tmux.
func TestPiMarkerStreamNoHistoryReplayLive(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	owner := "pi-noreplay-live-" + piRandomHex(4)
	workDir := piLiveWorkDir(t)
	marker := "NOREPLAY_" + strings.ToUpper(piRandomHex(4))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Task "+marker+": what is 2+2? Reply with one line: the answer, a space, then the task ID.")},
		WithInteractiveSessionID(owner),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	final := ""
	if len(resp.Choices) == 1 {
		final = strings.TrimSpace(resp.Choices[0].Content)
	}
	if !strings.Contains(final, marker) {
		t.Fatalf("turn did not produce the marker; final=%q", final)
	}

	session, ok := activePiInteractiveSession(owner)
	if !ok || session == nil || strings.TrimSpace(session.markerPath) == "" {
		t.Fatal("no active pi session with a marker file; cannot prove replay suppression")
	}
	markerPath := session.markerPath

	// Settle: the offset boundary must postdate every turn-1 write, so wait
	// until the marker file stops growing.
	waitForPiMarkerFileQuiescent(t, markerPath, 60*time.Second)

	// History + shape proof: reading from 0 must observe the marker in the
	// real marker file. Without this, absence below could mean "nothing to
	// replay".
	allMarkers, _, err := readPiMarkersSince(markerPath, 0)
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	var history strings.Builder
	for _, m := range allMarkers {
		history.WriteString(m.Delta)
	}
	if !strings.Contains(history.String(), marker) {
		t.Fatalf("marker %q not found in %s; cannot prove replay suppression", marker, markerPath)
	}

	// Restart simulation: snapshot the size from disk, as the adapter does
	// at the top of every turn, and read only what follows it.
	startOffset, err := piMarkerFileSize(markerPath)
	if err != nil {
		t.Fatalf("piMarkerFileSize: %v", err)
	}
	if startOffset <= 0 {
		t.Fatal("marker file is empty after a completed turn; the absence assertion would be vacuous")
	}
	freshMarkers, _, err := readPiMarkersSince(markerPath, startOffset)
	if err != nil {
		t.Fatalf("read markers since offset: %v", err)
	}
	var streamed strings.Builder
	for _, m := range freshMarkers {
		streamed.WriteString(m.Delta)
	}
	if strings.Contains(streamed.String(), marker) {
		t.Fatalf("history replayed past a fresh offset: %q\nread: %q", marker, streamed.String())
	}
	t.Logf("fresh offset emitted no history; marker %q verified in %s", marker, markerPath)
}

// waitForPiMarkerFileQuiescent waits until the marker file's size is stable
// across 3s, so a start offset snapshotted afterwards postdates the turn.
func waitForPiMarkerFileQuiescent(t *testing.T, markerPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastSize int64 = -1
	var stableSince time.Time
	for {
		info, err := os.Stat(markerPath)
		if err != nil {
			t.Fatalf("stat marker file: %v", err)
		}
		if info.Size() == lastSize {
			if !stableSince.IsZero() && time.Since(stableSince) >= 3*time.Second {
				return
			}
		} else {
			lastSize = info.Size()
			stableSince = time.Now()
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker file %s still growing after %v", markerPath, timeout)
		}
		time.Sleep(time.Second)
	}
}
