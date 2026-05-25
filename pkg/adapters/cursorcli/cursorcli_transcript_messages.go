package cursorcli

import (
	"context"
	"crypto/md5" //nolint:gosec // md5 here mirrors cursor CLI's own directory naming, not crypto
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// cursorReturnedBlobs tracks the set of message blob IDs already
// returned to a caller per owner-session. Cursor's store.db root
// accumulates messages across all turns of a chat — if we re-walked
// the entire root every turn, the splice would re-record prior-turn
// messages each new turn. This map lets us return only the *new*
// messages since the last call for the same session.
//
// Eviction: we trust the workspace store-clean cycle to evict stale
// session IDs. The keys are short UUIDs so the memory cost is small.
var (
	cursorReturnedBlobsMu sync.Mutex
	cursorReturnedBlobs   = map[string]map[string]struct{}{}
)

// readCursorTranscriptMessages reconstructs the cursor CLI's
// internal turn-by-turn trail (assistant text, tool-call, tool-result
// blocks) from the local sqlite store the CLI writes at
//
//	~/.cursor/chats/<md5(cwd)>/<agentId>/store.db
//
// Cursor stores chat blobs in a content-addressed (SHA256) form:
//   - `meta` table: one JSON row with `latestRootBlobId`.
//   - `blobs` table: id → bytes. JSON-encoded message blobs sit
//     alongside protobuf wrapper blobs.
//
// The latest root blob is a protobuf whose `field 1` carries 32-byte
// refs to message blobs in chronological order, plus turn metadata
// (working_dir at f9, interface tag at f22="cli", unix-millis ts at
// f26). We parse the root, follow the refs, and JSON-decode each
// message blob.
//
// Shape mapping (cursor JSON → llmtypes):
//
//	{role:"assistant", content:[{type:"text", text}]}
//	  → {Role: AI, Parts: [TextContent]}
//	{role:"assistant", content:[{type:"tool-call", toolCallId, toolName, args}]}
//	  → {Role: AI, Parts: [ToolCall{ID:toolCallId, FunctionCall:{Name, Arguments=json(args)}}]}
//	{role:"tool",      content:[{type:"tool-result", toolCallId, toolName, result}]}
//	  → {Role: Tool, Parts: [ToolCallResponse{ToolCallID, Content}]}
//
// Skipped (noise / non-content):
//   - system role: already in mcpagent's outer system prompt
//   - first user blob: provider-options context (cwd / mcp / rules)
//   - assistant:redacted-reasoning: encrypted CoT, no usable text
//
// ownerSessionID scopes the per-session "already returned" cache used
// to dedup across multi-turn chats (cursor's root is cumulative).
// Pass an empty string to disable diffing and return the full root —
// useful for one-shot workflow phases or for diagnostic smoke tests.
//
// Returns nil on any error or when no store.db is found for the
// given workingDir. Best-effort.
func readCursorTranscriptMessages(turnStart time.Time, workingDir string, ownerSessionID string) []llmtypes.MessageContent {
	msgs, _ := readCursorTranscriptMessagesAndStoreDB(turnStart, workingDir, ownerSessionID)
	return msgs
}

// readCursorTranscriptMessagesAndStoreDB does the same work as
// readCursorTranscriptMessages but also returns the picked store.db
// path. Callers that need cursor's native session ID (the path's
// parent dir, used for --resume) want both at once because picking
// the freshest store.db twice would race when cursor writes another
// commit in between.
func readCursorTranscriptMessagesAndStoreDB(turnStart time.Time, workingDir string, ownerSessionID string) ([]llmtypes.MessageContent, string) {
	if strings.TrimSpace(workingDir) == "" {
		return nil, ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, ""
	}
	hash := workingDirHashForCursor(workingDir)
	if hash == "" {
		return nil, ""
	}
	chatsDir := filepath.Join(home, ".cursor", "chats", hash)
	info, err := os.Stat(chatsDir)
	if err != nil || !info.IsDir() {
		return nil, ""
	}

	// Cursor may keep multiple agent dirs per workspace; pick the
	// store.db whose mtime is freshest at-or-after turnStart-30s.
	cutoff := turnStart.Add(-30 * time.Second)
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	_ = filepath.WalkDir(chatsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(p) != "store.db" {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.ModTime().Before(cutoff) {
			return nil
		}
		cands = append(cands, cand{path: p, mod: fi.ModTime()})
		return nil
	})
	if len(cands) == 0 {
		return nil, ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	pickedPath := cands[0].path

	// Cursor commits its sqlite root blob asynchronously, sometimes
	// many seconds AFTER the tmux pane settles (observed 19s on a
	// trivial "Reply OK" turn — cursor does background bookkeeping).
	// Poll for up to ~4s, returning as soon as we see an assistant
	// or tool message. For trivial fast turns the commit may never
	// arrive in time and we return empty; for real tool-using turns
	// commits happen mid-turn so the data is usually there by
	// return. This is best-effort by design.
	deadline := time.Now().Add(4 * time.Second)
	for {
		msgs := readCursorStoreDBMessages(pickedPath, ownerSessionID)
		if hasUsableCursorTurn(msgs) || time.Now().After(deadline) {
			return msgs, pickedPath
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// cursorNativeSessionIDFromStoreDBPath extracts cursor's agentId —
// which is what `cursor-agent --resume <id>` accepts — from a
// transcript store.db path. The cursor layout is:
//
//	~/.cursor/chats/<md5(cwd)>/<agentId>/store.db
//
// So the parent dir's basename is the agentId. Returns "" for any
// path that doesn't match (defensive: callers may pass empty path
// when no transcript was located).
func cursorNativeSessionIDFromStoreDBPath(storeDBPath string) string {
	if strings.TrimSpace(storeDBPath) == "" {
		return ""
	}
	if filepath.Base(storeDBPath) != "store.db" {
		return ""
	}
	return filepath.Base(filepath.Dir(storeDBPath))
}

// hasUsableCursorTurn reports whether the message list contains any
// content beyond the system/user prefix — i.e., at least one
// assistant or tool message indicating cursor has committed the
// current turn's response.
func hasUsableCursorTurn(msgs []llmtypes.MessageContent) bool {
	for _, m := range msgs {
		if m.Role == llmtypes.ChatMessageTypeAI || m.Role == llmtypes.ChatMessageTypeTool {
			return true
		}
	}
	return false
}

// workingDirHashForCursor mirrors cursor's own workspace-dir naming:
// md5 of the absolute, symlink-resolved working directory.
func workingDirHashForCursor(workingDir string) string {
	path := strings.TrimSpace(workingDir)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	path = filepath.Clean(path)
	sum := md5.Sum([]byte(path)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// readCursorStoreDBMessages opens one cursor store.db and returns
// its chronologically-ordered message list. When ownerSessionID is
// non-empty, message blob IDs already returned for that session are
// skipped (multi-turn dedup against cursor's cumulative root) and
// any new IDs are recorded for the next call. Best-effort: returns
// nil on any error.
func readCursorStoreDBMessages(dbPath string, ownerSessionID string) []llmtypes.MessageContent {
	// Open read-only so a concurrent cursor process can't be disturbed.
	// NB: modernc.org/sqlite returns empty results when `immutable=1`
	// is set on the URI (driver quirk — different from mattn). Stick
	// to `mode=ro` only.
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()

	// meta.value affinity varies across cursor versions: some writes
	// store raw JSON bytes (BLOB), some store hex-encoded JSON (TEXT).
	// Scan as any and normalize.
	ctx := context.Background()
	var metaRaw any
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta LIMIT 1`).Scan(&metaRaw); err != nil {
		return nil
	}
	var metaBytes []byte
	switch v := metaRaw.(type) {
	case []byte:
		// Could be raw JSON bytes OR hex bytes-as-text. Try JSON first.
		if len(v) > 0 && (v[0] == '{' || v[0] == '[') {
			metaBytes = v
		} else if decoded, err := hex.DecodeString(string(v)); err == nil {
			metaBytes = decoded
		} else {
			metaBytes = v
		}
	case string:
		if len(v) > 0 && (v[0] == '{' || v[0] == '[') {
			metaBytes = []byte(v)
		} else if decoded, err := hex.DecodeString(v); err == nil {
			metaBytes = decoded
		} else {
			metaBytes = []byte(v)
		}
	default:
		return nil
	}
	var meta struct {
		LatestRootBlobID string `json:"latestRootBlobId"`
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil || meta.LatestRootBlobID == "" {
		return nil
	}

	// Fetch the root blob; rows are stored as BLOB but the cursor
	// CLI stores them hex-encoded as TEXT — handle both.
	rootData := readCursorBlob(ctx, db, meta.LatestRootBlobID)
	if len(rootData) == 0 || rootData[0] != 0x0a {
		// Not a protobuf root we know how to parse.
		return nil
	}

	// Pull field-1 (length-delimited, 32-byte) refs in source order.
	refs := extractCursorRootChildRefs(rootData)
	if len(refs) == 0 {
		return nil
	}

	// If we have an owner session, snapshot the already-seen set and
	// prepare to record this turn's additions. Cursor's root is
	// cumulative, so prior-turn refs reappear on every new turn.
	var seenForSession map[string]struct{}
	if ownerSessionID != "" {
		cursorReturnedBlobsMu.Lock()
		seen := cursorReturnedBlobs[ownerSessionID]
		if seen == nil {
			seen = make(map[string]struct{})
			cursorReturnedBlobs[ownerSessionID] = seen
		}
		// Copy so we don't mutate while iterating refs above.
		seenForSession = seen
		cursorReturnedBlobsMu.Unlock()
	}

	var out []llmtypes.MessageContent
	skippedFirstUserContext := false
	for _, ref := range refs {
		if seenForSession != nil {
			if _, already := seenForSession[ref]; already {
				continue
			}
		}
		data := readCursorBlob(ctx, db, ref)
		if len(data) == 0 || data[0] != '{' {
			continue
		}
		var msg cursorMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "system":
			// Mcpagent maintains the outer system prompt; cursor's
			// internal "You are Composer..." duplicates that.
			continue
		case "user":
			// Cursor stores TWO user blobs per turn: the first
			// carries provider-options context (cwd, mcp config,
			// rules — recognizable as a long string payload with
			// no <user_query> wrapper); the second carries the
			// actual user query. Skip the first, keep nothing
			// from the second (the outer history already has the
			// user message).
			if !skippedFirstUserContext {
				skippedFirstUserContext = true
				continue
			}
			continue
		case "assistant":
			parts := cursorAssistantPartsFromContent(msg.Content)
			if len(parts) == 0 {
				continue
			}
			out = append(out, llmtypes.MessageContent{
				Role:  llmtypes.ChatMessageTypeAI,
				Parts: parts,
			})
		case "tool":
			parts := cursorToolPartsFromContent(msg.Content)
			if len(parts) == 0 {
				continue
			}
			out = append(out, llmtypes.MessageContent{
				Role:  llmtypes.ChatMessageTypeTool,
				Parts: parts,
			})
		}
	}

	// Record every ref we walked under this session — including the
	// system/user blobs we skipped — so we never re-emit anything
	// from this root's prefix on the next turn.
	if ownerSessionID != "" {
		cursorReturnedBlobsMu.Lock()
		bucket := cursorReturnedBlobs[ownerSessionID]
		if bucket == nil {
			bucket = make(map[string]struct{})
			cursorReturnedBlobs[ownerSessionID] = bucket
		}
		for _, r := range refs {
			bucket[r] = struct{}{}
		}
		cursorReturnedBlobsMu.Unlock()
	}
	return out
}

type cursorMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// cursorAssistantPartsFromContent maps a cursor assistant message's
// `content` (either a string or a list of typed blocks) into
// llmtypes parts. Returns nil if no usable parts remain.
func cursorAssistantPartsFromContent(raw json.RawMessage) []llmtypes.ContentPart {
	if len(raw) == 0 {
		return nil
	}
	// String content (rare on assistant, but possible).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []llmtypes.ContentPart{llmtypes.TextContent{Text: s}}
	}
	var blocks []struct {
		Type       string          `json:"type"`
		Text       string          `json:"text"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var parts []llmtypes.ContentPart
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			parts = append(parts, llmtypes.TextContent{Text: b.Text})
		case "tool-call":
			args := "{}"
			if len(b.Args) > 0 {
				args = string(b.Args)
			}
			parts = append(parts, llmtypes.ToolCall{
				ID:   b.ToolCallID,
				Type: "function",
				FunctionCall: &llmtypes.FunctionCall{
					Name:      b.ToolName,
					Arguments: args,
				},
			})
			// redacted-reasoning, redacted-* — silently skipped.
		}
	}
	return parts
}

// cursorToolPartsFromContent maps a tool-role message into a
// ToolCallResponse part.
func cursorToolPartsFromContent(raw json.RawMessage) []llmtypes.ContentPart {
	if len(raw) == 0 {
		return nil
	}
	var blocks []struct {
		Type       string          `json:"type"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var parts []llmtypes.ContentPart
	for _, b := range blocks {
		if b.Type != "tool-result" {
			continue
		}
		// `result` is typically a string but can be a structured
		// object; preserve as a JSON-encoded string when not a
		// plain string so nothing is dropped.
		var rs string
		if err := json.Unmarshal(b.Result, &rs); err != nil {
			rs = string(b.Result)
		}
		parts = append(parts, llmtypes.ToolCallResponse{
			ToolCallID: b.ToolCallID,
			Name:       b.ToolName,
			Content:    rs,
		})
	}
	return parts
}

// readCursorBlob reads one row from `blobs` by id. Cursor stores
// blobs as BLOB but sqlite3 driver may return TEXT (hex) depending
// on the affinity; handle both.
func readCursorBlob(ctx context.Context, db *sql.DB, id string) []byte {
	var raw any
	if err := db.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, id).Scan(&raw); err != nil {
		return nil
	}
	switch v := raw.(type) {
	case []byte:
		return v
	case string:
		if b, err := hex.DecodeString(v); err == nil {
			return b
		}
		return []byte(v)
	}
	return nil
}

// extractCursorRootChildRefs parses a cursor root protobuf blob and
// returns the ordered list of 32-byte child blob refs (field 1,
// wire type 2, length 32). Other fields are ignored.
func extractCursorRootChildRefs(buf []byte) []string {
	var refs []string
	off := 0
	for off < len(buf) {
		tag, n := varintUint64(buf[off:])
		if n == 0 {
			break
		}
		off += n
		field := tag >> 3
		wt := tag & 0x7
		switch wt {
		case 0: // varint
			_, n := varintUint64(buf[off:])
			if n == 0 {
				return refs
			}
			off += n
		case 2: // length-delimited
			ln, n := varintUint64(buf[off:])
			if n == 0 {
				return refs
			}
			off += n
			end := off + int(ln)
			if end > len(buf) || end < off {
				return refs
			}
			payload := buf[off:end]
			off = end
			if field == 1 && len(payload) == 32 {
				refs = append(refs, hex.EncodeToString(payload))
			}
		default:
			// fixed-32 (5), fixed-64 (1), or unknown — stop
			// rather than risk misaligned reads.
			return refs
		}
	}
	return refs
}

// varintUint64 decodes a protobuf varint and returns (value, bytes
// consumed). Returns (0, 0) on truncated/invalid input.
func varintUint64(buf []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i, b := range buf {
		if i > 9 {
			return 0, 0
		}
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, i + 1
		}
		shift += 7
	}
	return 0, 0
}
