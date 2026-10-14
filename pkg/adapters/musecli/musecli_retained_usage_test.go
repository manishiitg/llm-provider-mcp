package musecli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Runs of a Muse terminal after the Run's own are reported once, with the Run's run id left to the Run.
func TestMuseRetainedUsageReportsEachRunOnceAndSkipsTheRunsOwn(t *testing.T) {
	row := func(seq int, run string, in, out int) string {
		return `{"sequence":` + strconv.Itoa(seq) + `,"payload":{"kind":"run","event":{"kind":"goal_usage_attribution","record":{"owner":{"run_id":"` + run + `"},"quantity":{"reported":true,"input_tokens":` + strconv.Itoa(in) + `,"output_tokens":` + strconv.Itoa(out) + `,"cached_tokens":0}}}}}`
	}
	log := filepath.Join(t.TempDir(), "session.jsonl")
	lines := []string{row(10, "run-first", 100, 10), row(20, "run-steer-1", 200, 20), row(30, "run-steer-2", 300, 30)}
	if err := os.WriteFile(log, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const owner = "code:project:test"
	key, err := musePersistentKey(owner)
	if err != nil {
		t.Fatal(err)
	}
	musePersistentPool.Lock()
	musePersistentPool.m[key] = &musePersistentSession{logPath: log, usageFloorSet: true, usageFloorSequence: 0}
	musePersistentPool.Unlock()
	t.Cleanup(func() {
		musePersistentPool.Lock()
		delete(musePersistentPool.m, key)
		musePersistentPool.Unlock()
	})
	// The Run reads its own run id first (as the bounded Run does).
	if _, ok := readMuseTranscriptUsage(log, "run-first"); !ok {
		t.Fatal("the Run could not read its own run")
	}
	got := ReadRetainedTurnUsage(owner, "")
	if len(got) != 2 || got[0].ID != "run-steer-1" || got[1].ID != "run-steer-2" || got[0].Usage.InputTokens != 200 || got[1].Usage.OutputTokens != 30 {
		t.Fatalf("expected the two steer runs once, got %+v", got)
	}
	if again := ReadRetainedTurnUsage(owner, ""); len(again) != 0 {
		t.Fatalf("a second read repeated runs: %+v", again)
	}
}
