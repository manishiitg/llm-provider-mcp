package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeClaudeTurnEndFixture(t *testing.T, sessionID string, lines ...string) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	projectDir := filepath.Join(tmpHome, ".claude", "projects", "-tmp-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func claudeRow(kind, ts, id, stop, text string) string {
	if kind == "user" {
		return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	}
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"id":"` + id + `","stop_reason":"` + stop + `","content":[{"type":"text","text":"` + text + `"}]}}`
}

// LastIsEndTurn must follow the turn's latest record, not "any end_turn":
// once Claude continues after an end_turn, the turn is not finished.
func TestClaudeTranscriptLastIsEndTurn(t *testing.T) {
	turnStart := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	before := turnStart.Add(-time.Second).Format(time.RFC3339Nano)
	at := func(s int) string { return turnStart.Add(time.Duration(s) * time.Second).Format(time.RFC3339Nano) }
	const sid = "3f1d2c4b-5a6e-4f70-8a91-b2c3d4e5f607"
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"final end_turn", []string{claudeRow("assistant", at(1), "a", "tool_use", "working"), claudeRow("user", at(2), "", "", ""), claudeRow("assistant", at(3), "b", "end_turn", "done")}, true},
		{"tool still running", []string{claudeRow("assistant", at(1), "a", "tool_use", "working")}, false},
		{"tool result not yet answered", []string{claudeRow("assistant", at(1), "a", "tool_use", "working"), claudeRow("user", at(2), "", "", "")}, false},
		{"continued after end_turn", []string{claudeRow("assistant", at(1), "a", "end_turn", "first"), claudeRow("user", at(2), "", "", ""), claudeRow("assistant", at(3), "b", "tool_use", "more")}, false},
		{"only a prior turn ended", []string{claudeRow("assistant", before, "old", "end_turn", "prior")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeClaudeTurnEndFixture(t, sid, tc.lines...)
			got := completedAssistantResponseFromTranscript(sid, "", turnStart)
			if got.LastIsEndTurn != tc.want {
				t.Fatalf("LastIsEndTurn = %v, want %v (%+v)", got.LastIsEndTurn, tc.want, got)
			}
		})
	}
}

// The structured gate ends the turn on a quiet final end_turn with no pane at
// all, and a continuation written inside the quiet window defers it to the
// continuation's own end_turn.
func TestWaitForClaudeTurnEndInTranscript(t *testing.T) {
	const sid = "7a8b9c0d-1e2f-4a3b-8c4d-5e6f7a8b9c0d"
	turnStart := time.Now().UTC().Add(-time.Second)
	now := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
	path := writeClaudeTurnEndFixture(t, sid, claudeRow("assistant", now(), "a", "end_turn", "first answer"))
	go func() {
		time.Sleep(500 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = f.WriteString(claudeRow("user", now(), "", "", "") + "\n")
		time.Sleep(300 * time.Millisecond)
		_, _ = f.WriteString(claudeRow("assistant", now(), "b", "end_turn", "continued answer") + "\n")
		_ = f.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	got, ok := waitForClaudeTurnEndInTranscript(ctx, sid, "", turnStart, time.Second)
	if !ok || got.Text != "continued answer" {
		t.Fatalf("turn end = %+v ok=%v, want the continuation's answer", got, ok)
	}

	// No transcript at all: never reports completion, returns on ctx end.
	short, cancelShort := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancelShort()
	if _, ok := waitForClaudeTurnEndInTranscript(short, "0e1f2a3b-4c5d-4e6f-8a7b-9c0d1e2f3a4b", "", turnStart, time.Second); ok {
		t.Fatal("reported completion without a transcript")
	}
}
