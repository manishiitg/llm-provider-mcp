package cursorcli

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// cursorTranscriptStreamPollInterval is how often the tailer checks Cursor's
// store.db for new messages. Cursor commits its sqlite root asynchronously
// (sometimes seconds after the pane settles), so this is best-effort and laggier
// than the JSONL adapters.
const cursorTranscriptStreamPollInterval = 400 * time.Millisecond

// cursorInteractiveStreamTranscriptEnabled reports whether to poll Cursor's
// store.db for structured streaming. Opt-in (default OFF).
func cursorInteractiveStreamTranscriptEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvCursorInteractiveStreamTranscript))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// cursorTranscriptStreamState polls the current turn's Cursor store.db and emits
// assistant-text and tool-call-start StreamChunks as new message blobs are
// committed. It uses a DISTINCT owner key (ownerSessionID+suffix) so its
// incremental blob dedup (cursorReturnedBlobs) is independent of the post-turn
// sidecar read — both see every blob. Cursor closes the StreamChan in the
// adapter, so this runs as a goroutine that is stopped (with a final flush)
// before any close.
type cursorTranscriptStreamState struct {
	workingDir string
	streamKey  string
	// baseline is set when this turn actually starts (after session
	// acquisition). Cursor spawns warmup "OK" readiness turns during acquisition,
	// each with its own store.db; only store.db modified at/after baseline belong
	// to the real turn, so we skip the warmups.
	baseline time.Time
	seenTool map[string]bool
}

func newCursorTranscriptStreamState(turnStart time.Time, workingDir, ownerSessionID string) *cursorTranscriptStreamState {
	_ = turnStart // baseline is "now" (real-turn start), tighter than turnStart which predates warmups
	return &cursorTranscriptStreamState{
		workingDir: workingDir,
		streamKey:  ownerSessionID + "\x00transcript-stream",
		baseline:   time.Now().Add(-1 * time.Second), // small slack for clock/mtime skew
		seenTool:   map[string]bool{},
	}
}

// freshestCursorStoreDBSince returns the newest store.db under this workingDir's
// cursor chats dir whose mtime is at/after `since`. Re-resolved every poll (not
// cached) so the tailer converges onto the REAL turn's store.db as cursor
// creates it — instead of latching onto a warmup "OK" store.db written first.
func freshestCursorStoreDBSince(workingDir string, since time.Time) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	hash := workingDirHashForCursor(workingDir)
	if hash == "" {
		return ""
	}
	chatsDir := filepath.Join(home, ".cursor", "chats", hash)
	var best string
	var bestMod time.Time
	_ = filepath.WalkDir(chatsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) != "store.db" {
			return nil
		}
		// store.db is opened in WAL mode: writes land in store.db-wal (and
		// touch store.db-shm) without ever updating store.db's own mtime
		// until a checkpoint happens. On a reused persistent session the
		// checkpoint from turn 1 can leave store.db's mtime stale for the
		// entire duration of turn 2+, so a store.db-only mtime check wrongly
		// filters out the very file we need — found live: turn 2 produced
		// zero streamed chunks because every store.db candidate read as
		// "too old" while its -wal sibling kept advancing. Use the freshest
		// mtime across the trio to judge staleness; still return the
		// store.db path itself since the read-only connection below sees
		// committed WAL contents transparently.
		modTime := cursorStoreDBEffectiveModTime(p, d)
		if modTime.Before(since) {
			return nil
		}
		if best == "" || modTime.After(bestMod) {
			best, bestMod = p, modTime
		}
		return nil
	})
	return best
}

// cursorStoreDBEffectiveModTime returns the freshest mtime among store.db and
// its -wal/-shm siblings (see freshestCursorStoreDBSince for why).
func cursorStoreDBEffectiveModTime(p string, d fs.DirEntry) time.Time {
	fi, err := d.Info()
	if err != nil {
		return time.Time{}
	}
	latest := fi.ModTime()
	for _, suffix := range []string{"-wal", "-shm"} {
		if sfi, err := os.Stat(p + suffix); err == nil && sfi.ModTime().After(latest) {
			latest = sfi.ModTime()
		}
	}
	return latest
}

// run polls on a ticker until ctx is cancelled, doing one final flush on stop to
// catch blobs cursor committed right at the end of the turn.
func (s *cursorTranscriptStreamState) run(ctx context.Context, streamChan chan<- llmtypes.StreamChunk) {
	if streamChan == nil {
		return
	}
	ticker := time.NewTicker(cursorTranscriptStreamPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.poll(context.Background(), streamChan) // final flush (single-threaded now)
			return
		case <-ticker.C:
			s.poll(ctx, streamChan)
		}
	}
}

func (s *cursorTranscriptStreamState) poll(ctx context.Context, streamChan chan<- llmtypes.StreamChunk) {
	// Re-resolve the freshest post-baseline store.db every poll so we follow the
	// real turn's store.db instead of a warmup one. Dedup by streamKey means a
	// blob is emitted once even if the resolved path changes between polls.
	path := freshestCursorStoreDBSince(s.workingDir, s.baseline)
	if path == "" {
		return
	}
	msgs := readCursorStoreDBMessages(path, s.streamKey)
	for _, chunk := range cursorMessagesToChunks(msgs, s.seenTool) {
		select {
		case streamChan <- chunk:
		case <-ctx.Done():
			return
		}
	}
}

// cursorMessagesToChunks maps new transcript messages to stream chunks:
// assistant text -> Content, tool_use -> ToolCallStart (deduped by call id).
// Blob-level dedup already prevents a message being returned twice, so no
// content dedup is needed here. Pure/testable — no sqlite.
func cursorMessagesToChunks(msgs []llmtypes.MessageContent, seenTool map[string]bool) []llmtypes.StreamChunk {
	var out []llmtypes.StreamChunk
	meta := map[string]interface{}{"cursor_cli_stream_source": "transcript"}
	for _, m := range msgs {
		for _, p := range m.Parts {
			switch v := p.(type) {
			case llmtypes.TextContent:
				if strings.TrimSpace(v.Text) == "" {
					continue
				}
				out = append(out, llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: v.Text, Metadata: meta})
			case llmtypes.ToolCall:
				name := ""
				if v.FunctionCall != nil {
					name = v.FunctionCall.Name
				}
				if v.ID != "" {
					if seenTool[v.ID] {
						continue
					}
					seenTool[v.ID] = true
				}
				out = append(out, llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallStart, ToolName: name, ToolCallID: v.ID, Metadata: meta})
			}
		}
	}
	return out
}
