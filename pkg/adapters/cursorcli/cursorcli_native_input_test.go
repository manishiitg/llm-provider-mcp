package cursorcli

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestAdoptNativeCursorInputScopesRepeatedQueriesWithoutResending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{`CREATE TABLE meta (key TEXT PRIMARY KEY,value TEXT)`, `CREATE TABLE blobs (id TEXT PRIMARY KEY,data BLOB)`} {
		if _, err := db.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	blobs := []string{
		`{"role":"user","content":[{"type":"text","text":"<user_query>repeat</user_query>"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"old answer"}]}`,
		`{"role":"user","content":[{"type":"text","text":"<user_query>repeat</user_query>"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"new answer"}]}`,
	}
	var ids [][]byte
	for i := range blobs {
		ids = append(ids, synthBlobID(byte(i+1)))
	}
	rootID := hex.EncodeToString(synthBlobID(0xff))
	meta, _ := json.Marshal(map[string]string{"latestRootBlobId": rootID})
	if _, err := db.ExecContext(context.Background(), `INSERT INTO meta VALUES('0',?)`, hex.EncodeToString(meta)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO blobs VALUES(?,?)`, rootID, buildCursorRootBlob(t, ids)); err != nil {
		t.Fatal(err)
	}
	for i, blob := range blobs {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO blobs VALUES(?,?)`, hex.EncodeToString(ids[i]), []byte(blob)); err != nil {
			t.Fatal(err)
		}
	}
	owner := t.Name()
	session := &cursorInteractiveSession{retainedStoreDB: path}
	cursorPersistentRegistry.Set(owner, session)
	t.Cleanup(func() {
		cursorPersistentRegistry.Delete(owner)
		cursorReturnedBlobsMu.Lock()
		delete(cursorReturnedBlobs, cursorTranscriptStreamKey(owner))
		cursorReturnedBlobsMu.Unlock()
	})
	if err := AdoptNativeInput(owner, "repeat", time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(session.retainedInput.baseline) != 2 {
		t.Fatalf("baseline=%+v", session.retainedInput.baseline)
	}
	rows := readCursorRetainedInput(session.retainedInput)
	if len(rows) != 2 || rows[1].Parts[0].(llmtypes.TextContent).Text != "new answer" {
		t.Fatalf("adoption reused previous answer: %+v", rows)
	}
}
