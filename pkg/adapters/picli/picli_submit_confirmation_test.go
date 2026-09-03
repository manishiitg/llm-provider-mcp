package picli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// idlePaneWithDraft renders the pane state that produced the confida-login
// false negative (2026-09-03): the just-sent text still visible in the region
// above the status line with a reverse-video cursor cell, and a status line
// that still says idle because Pi has not emitted its first model event yet.
func idlePaneWithDraft(draft string) string {
	return "some earlier transcript line\n" +
		draft + "\x1b[7m \x1b[0m\n" +
		"────────────────────────\n" +
		"\x1b[1mπ\x1b[0m • 🤖 google/gemini-3.8-flash • 💤 idle\n"
}

func TestIdlePaneWithDraftFixtureReadsAsUnsubmitted(t *testing.T) {
	pane := idlePaneWithDraft("what does validate browser evidence do")
	if !piPaneHasStatusLine(pane) || !piPaneLooksIdle(pane) {
		t.Fatalf("fixture must look idle with a status line: %q", stripPiANSI(pane))
	}
	if !piPaneShowsPromptDraft(pane, "what does validate browser evidence do") {
		t.Fatalf("fixture must show the draft: %q", stripPiANSI(pane))
	}
}

func TestEnsurePiInputSubmittedTrustsMarkerAcknowledgement(t *testing.T) {
	const message = "what does validate browser evidence do"
	markerPath := filepath.Join(t.TempDir(), "markers.jsonl")
	if err := os.WriteFile(markerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	submits := 0
	probes := piSubmitProbes{
		capture:    func(context.Context) (string, error) { return idlePaneWithDraft(message), nil },
		submit:     func(context.Context) error { submits++; return nil },
		markerPath: markerPath,
		wait:       3 * time.Second,
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		f, err := os.OpenFile(markerPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString(`{"type":"message_start","ts":1,"role":"user"}` + "\n")
		_, _ = f.WriteString(`{"type":"message_end","ts":2,"role":"user","text":"what does validate  browser evidence do"}` + "\n")
	}()

	started := time.Now()
	err := ensurePiInputSubmittedWith(context.Background(), message, probes)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("send must be confirmed by Pi's own acknowledgement despite the idle pane: %v", err)
	}
	if elapsed >= piPromptSubmitSettleWait {
		t.Fatalf("confirmation took %s; must not wait out the pane settle budget once the marker arrives", elapsed)
	}
}

func TestEnsurePiInputSubmittedIgnoresAcknowledgementsBeforeTheSend(t *testing.T) {
	const message = "what does validate browser evidence do"
	markerPath := filepath.Join(t.TempDir(), "markers.jsonl")
	earlier := `{"type":"message_end","ts":1,"role":"user","text":"what does validate browser evidence do"}` + "\n"
	if err := os.WriteFile(markerPath, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}
	offset, err := piMarkerFileSize(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	submits := 0
	probes := piSubmitProbes{
		capture:      func(context.Context) (string, error) { return idlePaneWithDraft(message), nil },
		submit:       func(context.Context) error { submits++; return nil },
		markerPath:   markerPath,
		markerOffset: offset,
		wait:         500 * time.Millisecond,
	}
	err = ensurePiInputSubmittedWith(context.Background(), message, probes)
	if err == nil || !strings.Contains(err.Error(), "remained in the prompt") {
		t.Fatalf("an identical message acknowledged BEFORE this send must not confirm it; err = %v", err)
	}
	if submits != 1 {
		t.Fatalf("recovery Enter count = %d, want exactly 1", submits)
	}
}

func TestEnsurePiInputSubmittedWithoutMarkersKeepsPaneVerdict(t *testing.T) {
	const message = "what does validate browser evidence do"
	busyPane := "\x1b[1mπ\x1b[0m • 🤖 google/gemini-3.8-flash • ⠧ working\n"
	err := ensurePiInputSubmittedWith(context.Background(), message, piSubmitProbes{
		capture: func(context.Context) (string, error) { return busyPane, nil },
		wait:    500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("a busy status line is still accepted as submitted: %v", err)
	}
	err = ensurePiInputSubmittedWith(context.Background(), message, piSubmitProbes{
		capture: func(context.Context) (string, error) { return idlePaneWithDraft(message), nil },
		submit:  func(context.Context) error { return nil },
		wait:    400 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("with no marker stream, an idle pane still showing the draft must still be reported as unsubmitted")
	}
}

func TestPiMarkersAcknowledgeUserMessage(t *testing.T) {
	long := strings.Repeat("please explain this in detail ", 4) // > 64 runes
	cases := []struct {
		name    string
		markers []piMarker
		message string
		want    bool
	}{
		{"exact", []piMarker{{Type: "message_end", Role: "user", Text: "hello there"}}, "hello there", true},
		{"whitespace-insensitive", []piMarker{{Type: "message_end", Role: "user", Text: "hello   there\n"}}, " hello there", true},
		{"assistant message does not count", []piMarker{{Type: "message_end", Role: "assistant", Text: "hello there"}}, "hello there", false},
		{"message_start has no text", []piMarker{{Type: "message_start", Role: "user"}}, "hello there", false},
		{"different text", []piMarker{{Type: "message_end", Role: "user", Text: "goodbye"}}, "hello there", false},
		{"long message prefix", []piMarker{{Type: "message_end", Role: "user", Text: long + " trailing"}}, long, true},
		{"short message needs exact match", []piMarker{{Type: "message_end", Role: "user", Text: "hello there friend"}}, "hello there", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := piMarkersAcknowledgeUserMessage(tc.markers, tc.message); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
