package cursorcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorFinalAnswerPrefersUnwrappedTranscript is the adapter-level proof for
// CertReplyFormattingFidelity: given the pane's hard-wrapped rendering and
// cursor's own store.db record of the same reply, the turn must return the
// UNWRAPPED text so it still renders as markdown.
//
// It exercises the two real pieces the adapter composes —
// latestCursorAssistantText (pull the last assistant prose out of a store.db
// read) and llmtypes.ReconcileFinalAnswer (decide whether to trust it) — rather
// than restating llmtypes' own unit tests.
func TestCursorFinalAnswerPrefersUnwrappedTranscript(t *testing.T) {
	const authored = "Let's take this one step at a time.\n\n" +
		"First we borrow, because 1/6 is smaller than 5/6, so **5 1/6 becomes 4 7/6**.\n\n" +
		"- Subtract the whole numbers\n- Then the fractions"

	// What an 60-col pane holds: identical words, structure destroyed.
	paneText := "Let's take this one step at a time.\n" +
		"First we borrow, because 1/6 is smaller than 5/6, so **5 1/6\n" +
		"becomes 4 7/6**.\n" +
		"- Subtract the whole numbers\n- Then the fractions"

	msgs := []llmtypes.MessageContent{
		// A tool-only assistant turn sits AFTER the prose, which is the common
		// real shape (narrate, then call a tool). The extractor must walk back
		// past it instead of returning empty.
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{
			llmtypes.TextContent{Text: authored},
		}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{
			llmtypes.ToolCall{ID: "t1"},
		}},
	}

	fromTranscript := latestCursorAssistantText(msgs)
	if fromTranscript != authored {
		t.Fatalf("latestCursorAssistantText did not recover the prose past a tool-only turn\n got:  %q\n want: %q",
			fromTranscript, authored)
	}

	got := llmtypes.ReconcileFinalAnswer(paneText, fromTranscript)
	if got != authored {
		t.Errorf("final answer kept the pane's wrapping\n got:  %q\n want: %q", got, authored)
	}
	// The property that actually matters downstream: markdown structure survives.
	if !strings.Contains(got, "\n\n") {
		t.Errorf("paragraph breaks lost — the reply will render as one block: %q", got)
	}
}

// TestCursorFinalAnswerFallsBackToPane guards the failure direction. cursor
// commits store.db asynchronously (seconds after the pane settles), so the
// transcript can legitimately be empty or still hold the PREVIOUS turn. Neither
// may be allowed to replace this turn's reply — returning another turn's text is
// far worse than returning wrapped text.
func TestCursorFinalAnswerFallsBackToPane(t *testing.T) {
	const paneText = "The total is 3 4/6."

	t.Run("transcript not committed yet", func(t *testing.T) {
		if got := llmtypes.ReconcileFinalAnswer(paneText, latestCursorAssistantText(nil)); got != paneText {
			t.Errorf("got %q, want the pane text %q", got, paneText)
		}
	})

	t.Run("transcript holds a previous turn", func(t *testing.T) {
		stale := []llmtypes.MessageContent{{
			Role:  llmtypes.ChatMessageTypeAI,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Let's start question 2."}},
		}}
		if got := llmtypes.ReconcileFinalAnswer(paneText, latestCursorAssistantText(stale)); got != paneText {
			t.Errorf("a stale transcript replaced this turn's reply: got %q, want %q", got, paneText)
		}
	})

	t.Run("assistant turn with no prose at all", func(t *testing.T) {
		toolOnly := []llmtypes.MessageContent{{
			Role:  llmtypes.ChatMessageTypeAI,
			Parts: []llmtypes.ContentPart{llmtypes.ToolCall{ID: "t1"}},
		}}
		if got := latestCursorAssistantText(toolOnly); got != "" {
			t.Errorf("expected no text from a tool-only transcript, got %q", got)
		}
	})
}

// TestCursorSidecarFoundWhenOnlyWALIsFresh pins the trap that silently defeated
// the reply-formatting fix in the live app.
//
// Cursor opens store.db in WAL mode, so a turn's writes land in store.db-wal
// while store.db's own mtime stays frozen until a checkpoint. Observed live at a
// 24-minute gap (store.db 19:30, -wal 19:54). The sidecar reader filtered
// candidates on store.db's own mtime, so it rejected the file holding the current
// turn, reported "no transcript", and every caller fell back to the hard-wrapped
// tmux pane — the fix looked implemented and did nothing.
//
// Reverting to `d.Info().ModTime()` in readCursorTranscriptMessagesAndStoreDB
// makes this fail.
func TestCursorSidecarFoundWhenOnlyWALIsFresh(t *testing.T) {
	const authored = "Scale means:\n\n- **1 cm = 50 km**\n- multiply, never add"

	workingDir := newCursorHistoryFixture(t, authored)

	// Age store.db far past the turn window, exactly as an un-checkpointed db
	// looks, while leaving a fresh -wal beside it.
	hash := workingDirHashForCursor(workingDir)
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".cursor", "chats", hash, "agent-restart-test", "store.db")
	stale := time.Now().Add(-24 * time.Minute)
	if err := os.Chtimes(dbPath, stale, stale); err != nil {
		t.Fatalf("age store.db: %v", err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatalf("write -wal: %v", err)
	}

	msgs, path := readCursorTranscriptMessagesAndStoreDB(time.Now(), workingDir, "", "")
	if path == "" || len(msgs) == 0 {
		t.Fatalf("sidecar not found while only the -wal was fresh — callers would fall back to the wrapped pane")
	}
	if got := latestCursorAssistantText(msgs); got != authored {
		t.Errorf("recovered text mismatch\n got:  %q\n want: %q", got, authored)
	}
}

// TestReadCursorTranscriptPrefersKnownSessionOverNewerDecoy pins the bug found
// live: a real parent conversation's turn also made several read_image calls,
// each a bounded, one-shot cursor-agent invocation sharing the SAME workingDir
// (image_tool.go's runReadImage always uses workspaceRoot()) and so landing
// under the SAME chatsDir. Without a known session id to go on, the old code
// picked whichever store.db anywhere in chatsDir had the freshest mtime —
// and a read_image sub-call's async commit landing even a moment later than
// the real conversation's own was enough to silently pick ITS unrelated
// content instead. That both destroyed that turn's markdown (the wrapped pane
// got used) and, worse, persisted the decoy's native_session_id for every
// future turn to resume, permanently gluing the conversation onto the wrong
// session.
//
// Reverting the knownNativeSessionID fast path in
// readCursorTranscriptMessagesAndStoreDB makes this fail (it picks the decoy).
func TestReadCursorTranscriptPrefersKnownSessionOverNewerDecoy(t *testing.T) {
	const realReply = "**Short answer: yes.**\n\n- point one\n- point two"
	const decoyReply = "a photo transcription, nothing to do with the real conversation"

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	workingDir := filepath.Join(tmpHome, "ws", "family")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll workingDir: %v", err)
	}

	// The real conversation's own session, written FIRST (older mtime).
	writeCursorStoreDBAt(t, workingDir, "real-parent-session", realReply)
	realDBPath := filepath.Join(tmpHome, ".cursor", "chats", workingDirHashForCursor(workingDir), "real-parent-session", "store.db")
	older := time.Now().Add(-10 * time.Second)
	if err := os.Chtimes(realDBPath, older, older); err != nil {
		t.Fatalf("age real session store.db: %v", err)
	}

	// A read_image-style bounded decoy session sharing the same workingDir,
	// written SECOND so its commit is strictly newer — exactly the race that
	// broke this live.
	writeCursorStoreDBAt(t, workingDir, "cursor-bounded-decoy1234", decoyReply)

	msgs, path := readCursorTranscriptMessagesAndStoreDB(time.Now(), workingDir, "", "real-parent-session")
	if path == "" {
		t.Fatal("expected to find the known session's store.db")
	}
	if !strings.Contains(path, "real-parent-session") {
		t.Fatalf("picked the wrong session's store.db: %s", path)
	}
	if got := latestCursorAssistantText(msgs); got != realReply {
		t.Errorf("got the decoy's content instead of the known session's own\n got:  %q\n want: %q", got, realReply)
	}
}
