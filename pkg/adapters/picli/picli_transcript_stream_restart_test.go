package picli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPiMarkerOffsetDoesNotReplayHistoryOnFreshProcess is the pi counterpart of
// cursorcli's restart regression test.
//
// Background: the cursor adapter had a real bug where the first turn of a FRESH
// process replayed the whole chat history as streamed content, because its only
// defence was an in-process map of already-returned message IDs against a
// CUMULATIVE transcript root — a restart emptied the map while the transcript on
// disk still held every prior turn.
//
// Pi is structurally immune for a different reason, and this test pins that
// reason down rather than trusting it: its marker file is append-only, and the
// adapter snapshots the file's SIZE (piMarkerFileSize) as the turn's start
// offset before sending the prompt, then only ever reads markers appended past
// it. Because that offset is derived from on-disk state at turn start rather
// than remembered across turns, a restarted process computes exactly the same
// boundary and cannot re-read history.
//
// Kept as a deliberate "cannot reproduce" test: if the start offset ever becomes
// process-remembered state (or defaults to 0), this fails immediately.
func TestPiMarkerOffsetDoesNotReplayHistoryOnFreshProcess(t *testing.T) {
	const (
		historical = "PRIOR TURN narration that must never be replayed."
		current    = "Current turn narration."
	)

	path := filepath.Join(t.TempDir(), "markers.jsonl")

	// A previous turn's markers are already on disk when this process starts.
	writeMarkerLines(t, path,
		`{"type":"message_update","updateType":"text_delta","delta":"`+historical+`"}`,
	)

	// What the adapter does at the top of a turn (picli_interactive_adapter.go:
	// startOffset, _ := piMarkerFileSize(session.markerPath)) — computed fresh
	// from disk, so a restart lands on the identical boundary.
	startOffset, err := piMarkerFileSize(path)
	if err != nil {
		t.Fatalf("piMarkerFileSize: %v", err)
	}

	// The turn then produces its own output, appended after that boundary.
	writeMarkerLines(t, path,
		`{"type":"message_update","updateType":"text_delta","delta":"`+current+`"}`,
	)

	markers, _, err := readPiMarkersSince(path, startOffset)
	if err != nil {
		t.Fatalf("readPiMarkersSince: %v", err)
	}

	var got strings.Builder
	for _, m := range markers {
		got.WriteString(m.Delta)
	}

	if strings.Contains(got.String(), historical) {
		t.Errorf("history replayed on a fresh process: %q\nread: %q", historical, got.String())
	}
	if !strings.Contains(got.String(), current) {
		t.Errorf("current-turn text was not read; the offset is too aggressive.\nread: %q", got.String())
	}
}

// writeMarkerLines appends newline-delimited marker JSON, the way pi's own
// marker hook writes it.
func writeMarkerLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open marker file: %v", err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
}
