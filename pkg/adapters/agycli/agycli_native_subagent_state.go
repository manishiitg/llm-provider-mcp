package agycli

import (
	"context"
	"database/sql"
	"net/url"
)

// NativeSubagentIDs are the child conversations an invoke_subagent step launched.
func NativeSubagentIDs(payload []byte) []string { return agyNativeSubagentIDs(payload) }

// NativeSubagentSettled reports whether a child conversation's database ends in a finished assistant answer. A
// missing or unreadable database is not settled, so a row never closes on a guess.
func NativeSubagentSettled(dbPath string) bool {
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return false
	}
	defer db.Close()
	var stepType, status int
	if db.QueryRowContext(context.Background(), `SELECT step_type, status FROM steps ORDER BY idx DESC LIMIT 1`).Scan(&stepType, &status) != nil {
		return false
	}
	return stepType == agyStepAssistant && status == 3
}
