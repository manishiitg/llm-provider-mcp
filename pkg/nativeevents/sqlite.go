package nativeevents

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/agycli"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	_ "modernc.org/sqlite"
)

func readDatabase(ctx context.Context, provider, session, path string, c Cursor) (Batch, error) {
	b := Batch{Next: cloneCursor(c), Unknown: map[string]int{}}
	if provider == "cursor-cli" {
		records, last, err := cursorcli.ReadNativeRecordRows(ctx, path, session, c.LastID)
		if err != nil {
			return b, err
		}
		for _, r := range records {
			if r.Kind == "turn" {
				b.Next.TurnID, b.Next.Origin = r.ID, "human"
			}
			emit(&b, r)
		}
		b.Next.LastID = last
		b.CaughtUp = true
		return b, nil
	}
	if provider != "agy-cli" {
		return b, fmt.Errorf("provider has no native SQLite reader")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return b, err
	}
	defer db.Close()
	// Revisit outstanding native steps as well as the newest index: tool status
	// can change in place while another tool runs. No WAL-unsafe immutable mode.
	start := c.Offset
	for key := range c.Tools {
		if n, e := strconv.ParseInt(key, 10, 64); e == nil && n < start {
			start = n
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT idx,step_type,step_payload,status FROM steps WHERE idx>=? ORDER BY idx LIMIT 1024`, start)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var idx int64
		var kind, status int
		var payload []byte
		if err := rows.Scan(&idx, &kind, &payload, &status); err != nil {
			return b, err
		}
		if idx > b.Next.Offset {
			b.Next.Offset = idx
		}
		key := strconv.FormatInt(idx, 10)
		r := llmtypes.NativeRecord{Provider: provider, SessionID: session, ID: fmt.Sprintf("step:%d:%d", idx, status)}
		switch kind {
		case 14:
			b.Next.TurnID, b.Next.Origin = "step:"+key, "human"
			r.Kind, r.State = "turn", "started"
		case 15:
			if status != 3 {
				b.Next.Tools[key] = "pending"
				continue
			}
			r.Text, _, _ = agycli.DecodeNativeStepText(kind, payload)
			r.Kind, r.State = "reply", "committed"
		case 132:
			r.Kind, r.State = "tool", "started"
			if status == 3 {
				r.State = "finished"
			} else if status != 0 && status != 1 && status != 2 {
				b.Gaps++
				continue
			}
			if status != 3 {
				b.Next.Tools[key] = "pending"
			}
		case 17:
			r.Kind, r.State = "error", "failed"
		case 101:
			r.Kind, r.State = "notice", "observed"
		default:
			b.Unknown["step:"+strconv.Itoa(kind)]++
			continue
		}
		if status == 3 {
			delete(b.Next.Tools, key)
		}
		emit(&b, r)
	}
	b.CaughtUp = count < 1024
	return b, rows.Err()
}
