package nativeevents

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestAgyNativeMutableStepCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER, step_payload BLOB, status INTEGER)`); err != nil {
		t.Fatal(err)
	}
	inner := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "Answer")
	payload := protowire.AppendBytes(protowire.AppendTag(nil, 20, protowire.BytesType), inner)
	if _, err := db.Exec(`INSERT INTO steps VALUES(1,15,?,1)`, payload); err != nil {
		t.Fatal(err)
	}
	b, err := Read(context.Background(), "agy-cli", "native", path, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Records) != 0 {
		t.Fatal("partial assistant step was published")
	}
	if _, err := db.Exec(`UPDATE steps SET status=3 WHERE idx=1`); err != nil {
		t.Fatal(err)
	}
	b, err = Read(context.Background(), "agy-cli", "native", path, b.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Records) != 1 || b.Records[0].Text != "Answer" || b.Records[0].State != "committed" {
		t.Fatalf("committed step missing: %+v", b.Records)
	}
}

// An old mutable step must not pin the forward cursor to its first 1024 rows.
func TestAgyPendingStepDoesNotReplayOrStarveLaterRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER, step_payload BLOB, status INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO steps VALUES(0,132,NULL,1)`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 1100; i++ {
		if _, err := db.Exec(`INSERT INTO steps VALUES(?,132,NULL,3)`, i); err != nil {
			t.Fatal(err)
		}
	}
	first, err := Read(t.Context(), "agy-cli", "native", path, Cursor{})
	if err != nil || first.CaughtUp || first.Next.Offset != 1024 {
		t.Fatalf("first batch: %+v %v", first.Next, err)
	}
	second, err := Read(t.Context(), "agy-cli", "native", path, first.Next)
	if err != nil || !second.CaughtUp || second.Next.Offset != 1100 || len(second.Records) != 76 {
		t.Fatalf("pending step blocked or replayed the next batch: records=%d cursor=%+v err=%v", len(second.Records), second.Next, err)
	}
	idle, err := Read(t.Context(), "agy-cli", "native", path, second.Next)
	if err != nil || len(idle.Records) != 0 {
		t.Fatalf("unchanged pending step replayed: %+v %v", idle.Records, err)
	}
	if _, err := db.Exec(`UPDATE steps SET status=6 WHERE idx=0`); err != nil {
		t.Fatal(err)
	}
	stopped, err := Read(t.Context(), "agy-cli", "native", path, idle.Next)
	if err != nil || len(stopped.Records) != 1 || stopped.Records[0].State != "stopped" || len(stopped.Next.Tools) != 0 {
		t.Fatalf("interrupted step remained pending: %+v %v", stopped, err)
	}
	last, err := Read(t.Context(), "agy-cli", "native", path, stopped.Next)
	if err != nil || len(last.Records) != 0 {
		t.Fatalf("settled step replayed: %+v %v", last.Records, err)
	}
	// PR127: unknown is not a terminal status. Keep it pending so a later
	// known completion remains visible after the forward cursor moves past it.
	if _, err := db.Exec(`INSERT INTO steps VALUES(1100,132,NULL,5)`); err != nil {
		t.Fatal(err)
	}
	unknown, err := Read(t.Context(), "agy-cli", "native", path, last.Next)
	if err != nil || unknown.Next.Tools["1100"] == "" || unknown.Gaps != 1 || len(unknown.Records) != 0 {
		t.Fatalf("unknown tool was lost or treated as finished: %+v %v", unknown, err)
	}
	if _, err := db.Exec(`UPDATE steps SET status=3 WHERE idx=1100`); err != nil {
		t.Fatal(err)
	}
	finished, err := Read(t.Context(), "agy-cli", "native", path, unknown.Next)
	if err != nil || len(finished.Records) != 1 || finished.Records[0].State != "finished" || len(finished.Next.Tools) != 0 {
		t.Fatalf("unknown tool's later completion was lost: %+v %v", finished, err)
	}
}

// An invoke_subagent step opens a subagent row per child conversation; it closes only when that child's own
// conversation ends in a finished answer.
func TestAgySubagentRowOpensAndClosesWithItsChild(t *testing.T) {
	dir := t.TempDir()
	open := func(name string) *sql.DB {
		db, err := sql.Open("sqlite", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE steps(idx INTEGER PRIMARY KEY, step_type INTEGER, step_payload BLOB, status INTEGER)`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	parent, child := open("parent.db"), open("child1.db")
	payload := protowire.AppendBytes(protowire.AppendTag(nil, 10, protowire.BytesType), protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "child1"))
	for _, field := range []protowire.Number{143, 2, 6, 2, 140} {
		payload = protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), payload)
	}
	if _, err := parent.Exec(`INSERT INTO steps VALUES(1,14,x'',3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Exec(`INSERT INTO steps VALUES(2,132,?,3)`, payload); err != nil {
		t.Fatal(err)
	}
	const launchTurn = "step:1"
	if _, err := child.Exec(`INSERT INTO steps VALUES(1,15,x'',1)`); err != nil {
		t.Fatal(err)
	}
	states := func(b Batch) (out []string) {
		for _, r := range b.Records {
			if r.Kind == "helper" {
				out = append(out, r.TaskID+":"+r.State)
			}
		}
		return out
	}
	b, err := Read(context.Background(), "agy-cli", "native", filepath.Join(dir, "parent.db"), Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if got := states(b); len(got) != 1 || got[0] != "child1:started" {
		t.Fatalf("launch opens one row and the unfinished child keeps it open, got %v", got)
	}
	if _, err := child.Exec(`UPDATE steps SET status=3 WHERE idx=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Exec(`INSERT INTO steps VALUES(3,14,x'',3)`); err != nil { // a later turn begins
		t.Fatal(err)
	}
	b, err = Read(context.Background(), "agy-cli", "native", filepath.Join(dir, "parent.db"), b.Next)
	if err != nil {
		t.Fatal(err)
	}
	if got := states(b); len(got) != 1 || got[0] != "child1:finished" {
		t.Fatalf("the finished child closes its row once, got %v", got)
	}
	// Both records carry the turn that launched the child: the first turn here, though a later turn was scanned since.
	for _, r := range b.Records {
		if r.Kind == "helper" && r.TurnID != launchTurn {
			t.Fatalf("the finish lost the launching turn: %q, want %q", r.TurnID, launchTurn)
		}
	}
}
