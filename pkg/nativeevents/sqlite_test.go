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
