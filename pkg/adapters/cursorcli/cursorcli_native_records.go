package cursorcli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func NativeRecordPath(workingDir, sessionID, home string) string {
	if workingDir == "" || sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return ""
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return cursorStoreDBForNativeSession(home, workingDir, sessionID)
}

// ReadNativeRecordRows walks only the selected database's current root. Cursor
// supplies stable blob identities, but no trustworthy turn times or usage.
func ReadNativeRecordRows(ctx context.Context, path, sessionID, after string) ([]llmtypes.NativeRecord, string, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, after, err
	}
	defer db.Close()
	refs, err := cursorStoreLatestRootRefs(ctx, db)
	if err != nil {
		return nil, after, err
	}
	start := 0
	if after != "" {
		found := false
		for i, ref := range refs {
			if ref == after {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, after, fmt.Errorf("native root changed without the saved blob boundary")
		}
	}
	var records []llmtypes.NativeRecord
	last := after
	for _, ref := range refs[start:] {
		if err := ctx.Err(); err != nil {
			return nil, after, err
		}
		var msg cursorMessage
		if err := json.Unmarshal(readCursorBlob(ctx, db, ref), &msg); err != nil {
			last = ref
			continue
		}
		r := llmtypes.NativeRecord{ID: "blob:" + ref, Provider: "cursor-cli", SessionID: sessionID}
		switch strings.ToLower(msg.Role) {
		case "user":
			if cursorUserQueryFromContent(msg.Content) != "" {
				r.Kind, r.State = "turn", "observed_input"
				records = append(records, r)
			}
		case "assistant":
			if value := cursorAssistantTextFromContent(msg.Content); value != "" {
				r.Kind, r.State, r.Text = "reply", "committed", value
				records = append(records, r)
			}
			var blocks []struct {
				Type       string `json:"type"`
				ToolCallID string `json:"toolCallId"`
				ToolName   string `json:"toolName"`
			}
			if json.Unmarshal(msg.Content, &blocks) == nil {
				for _, block := range blocks {
					if block.Type == "tool-call" && block.ToolCallID != "" {
						out := r
						out.ID += ":" + block.ToolCallID
						out.Text = ""
						out.Kind, out.State, out.ToolID, out.Name = "tool", "started", block.ToolCallID, block.ToolName
						records = append(records, out)
					}
				}
			}
		}
		last = ref
	}
	return records, last, nil
}
