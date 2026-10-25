package cursorcli

import (
	"context"
	"testing"
)

func TestCursorNativeRecordBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeCursorStoreFixture(t, "/project", "native", []string{
		`{"role":"user","content":[{"type":"text","text":"<user_query>Question</user_query>"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"Answer"},{"type":"redacted-reasoning","text":"PRIVATE_THOUGHT"}]}`,
	})
	rows, last, err := ReadNativeRecordRows(context.Background(), path, "native", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].Kind != "reply" || rows[1].Text != "Answer" || last == "" {
		t.Fatalf("unexpected native records: %+v", rows)
	}
	again, _, err := ReadNativeRecordRows(context.Background(), path, "native", last)
	if err != nil || len(again) != 0 {
		t.Fatal("committed blob replayed")
	}
	if _, _, err := ReadNativeRecordRows(context.Background(), path, "native", "missing-boundary"); err == nil {
		t.Fatal("changed native root was silently accepted")
	}
}
