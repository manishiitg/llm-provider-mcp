package musecli

import (
	"os"
	"path/filepath"
	"testing"
)

// The silent-turn line carries numbers only: the newest log sequence is read without keeping any record text.
func TestMuseNewestLogSequenceAndSignal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if got := museNewestLogSequence(path); got != -1 {
		t.Fatalf("missing log = %d, want -1", got)
	}
	if got := museLogSignal(path); got != "no-log" {
		t.Fatalf("signal for a missing log = %q", got)
	}
	body := "{\"sequence\":3,\"payload\":{\"text\":\"private\"}}\n{\"sequence\":9}\nnot json\n{\"sequence\":5}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := museNewestLogSequence(path); got != 9 {
		t.Fatalf("newest sequence = %d, want 9", got)
	}
	before := museLogSignal(path)
	if err := os.WriteFile(path, []byte(body+"{\"sequence\":10}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if museLogSignal(path) == before {
		t.Fatal("the signal must change when Muse appends")
	}
}
