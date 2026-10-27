package cursorcli

import (
	"crypto/md5" //nolint:gosec // md5 here mirrors cursor CLI's own directory naming, not crypto
	"encoding/hex"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/pathidentity"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	cursorReturnedBlobsMu sync.Mutex
	cursorReturnedBlobs   = map[string]map[string]struct{}{}
)

func readCursorTranscriptMessages(turnStart time.Time, workingDir string, baseline map[string]struct{}) []llmtypes.MessageContent {
	msgs, _ := readCursorTranscriptMessagesAndStoreDB(turnStart, workingDir, baseline, "")
	return msgs
}

func readCursorTranscriptMessagesAndStoreDB(turnStart time.Time, workingDir string, baseline map[string]struct{}, knownNativeSessionID string, accountHome ...string) ([]llmtypes.MessageContent, string) {
	if strings.TrimSpace(workingDir) == "" {
		return nil, ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, ""
	}
	if len(accountHome) > 0 && accountHome[0] != "" {
		home = accountHome[0]
	}
	var pickedPath string
	// A persisted native ID is authoritative. Never fall back to a different
	// chat merely because this transcript is missing or has not flushed yet.
	if id := strings.TrimSpace(knownNativeSessionID); id != "" {
		pickedPath = cursorStoreDBForNativeSession(home, workingDir, id)
		if pickedPath == "" {
			return nil, ""
		}
	}
	if pickedPath == "" {
		// Cursor may keep multiple agent dirs per workspace; pick the
		// store.db whose mtime is freshest at-or-after turnStart-30s. Only
		// reached with no known session id — see this function's own
		// doc comment for why that's a narrower case than it used to be.
		cutoff := turnStart.Add(-30 * time.Second)
		type cand struct {
			path string
			mod  time.Time
		}
		var cands []cand
		walkCandidate := func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if filepath.Base(p) != "store.db" {
				return nil
			}
			// Use the WAL-aware mtime, NOT store.db's own. Cursor opens the db in WAL
			// mode, so writes land in store.db-wal and store.db's mtime stays frozen
			// until a checkpoint — observed live at a 24-minute gap (store.db 19:30,
			// -wal 19:54). A store.db-only check therefore rejects the very file that
			// holds the current turn, the transcript reads as "missing", and callers
			// silently fall back to the wrapped tmux pane. The streaming tailer already
			// learned this (see cursorStoreDBEffectiveModTime); this path had not.
			modTime := cursorStoreDBEffectiveModTime(p, d)
			if modTime.IsZero() || modTime.Before(cutoff) {
				return nil
			}
			cands = append(cands, cand{path: p, mod: modTime})
			return nil
		}
		for _, root := range cursorChatsRoots(home) {
			_ = filepath.WalkDir(filepath.Join(root, workingDirHashForCursor(workingDir)), walkCandidate)
		}
		if len(cands) == 0 {
			return nil, ""
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
		pickedPath = cands[0].path
	}

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
		msgs := readCursorStoreDBMessagesAfter(pickedPath, baseline)
		if hasUsableCursorTurn(msgs) || time.Now().After(deadline) {
			return msgs, pickedPath
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func latestCursorAssistantText(msgs []llmtypes.MessageContent) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llmtypes.ChatMessageTypeAI {
			continue
		}
		var b strings.Builder
		for _, part := range msgs[i].Parts {
			if txt, ok := part.(llmtypes.TextContent); ok {
				b.WriteString(txt.Text)
			}
		}
		// An assistant turn can be tool-calls only, with no prose; keep walking
		// back rather than returning "" and losing the reply that precedes it.
		if strings.TrimSpace(b.String()) != "" {
			return b.String()
		}
	}
	return ""
}

func cursorNativeSessionIDFromStoreDBPath(storeDBPath string) string {
	if strings.TrimSpace(storeDBPath) == "" {
		return ""
	}
	if filepath.Base(storeDBPath) != "store.db" {
		return ""
	}
	return filepath.Base(filepath.Dir(storeDBPath))
}

func hasUsableCursorTurn(msgs []llmtypes.MessageContent) bool {
	for _, m := range msgs {
		if m.Role == llmtypes.ChatMessageTypeAI || m.Role == llmtypes.ChatMessageTypeTool {
			return true
		}
	}
	return false
}

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
	path = pathidentity.Key(path)
	sum := md5.Sum([]byte(path)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

func readCursorStoreDBMessages(dbPath string, ownerSessionID string) []llmtypes.MessageContent {
	// Display-only cursor for live streaming: remembers what this stream has
	// already shown. It never decides what a turn records (see
	// readCursorStoreDBMessagesAfter).
	var seen map[string]struct{}
	if ownerSessionID != "" {
		cursorReturnedBlobsMu.Lock()
		current := cursorReturnedBlobs[ownerSessionID]
		seen = make(map[string]struct{}, len(current))
		for ref := range current {
			seen[ref] = struct{}{}
		}
		cursorReturnedBlobsMu.Unlock()
	}
	msgs, refs := cursorStoreMessagesExcept(dbPath, seen)
	if ownerSessionID != "" && refs != nil {
		cursorReturnedBlobsMu.Lock()
		bucket := cursorReturnedBlobs[ownerSessionID]
		if bucket == nil {
			bucket = make(map[string]struct{}, len(refs))
			cursorReturnedBlobs[ownerSessionID] = bucket
		}
		for _, r := range refs {
			bucket[r] = struct{}{}
		}
		cursorReturnedBlobsMu.Unlock()
	}
	return msgs
}

func cursorStoreDBForNativeSession(home, workingDir, id string) string {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) {
		return ""
	}
	paths := append([]string{pathidentity.Key(workingDir)}, pathidentity.Candidates(workingDir)...)
	seen := map[string]bool{}
	for _, path := range paths {
		sum := md5.Sum([]byte(filepath.Clean(path))) //nolint:gosec
		hash := hex.EncodeToString(sum[:])
		if seen[hash] {
			continue
		}
		seen[hash] = true
		for _, root := range cursorChatsRoots(home) {
			candidate := filepath.Join(root, hash, id, "store.db")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}
