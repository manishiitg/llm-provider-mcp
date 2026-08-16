package codexcli

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// codexTurnCompletionTracker follows the rollout JSONL for one interactive
// Codex session. The native task_complete event is a stronger completion
// signal than terminal text: status lines such as "Working" remain in tmux
// scrollback, and some Codex releases omit the visible "Worked for ..."
// footer even though the turn has ended.
//
// The rollout is resolved through resolveRollout when the caller supplies one
// (PLAT-108), which binds this tracker to its own session's conversation.
//
// This used to select purely by session_meta.cwd, justified by a comment
// claiming "interactive sessions use a unique working directory, so this
// remains isolated when several Codex agents run concurrently". That assumption
// is false: a workflow's interactive Chat and its scheduled run share one
// directory, so the tracker could latch onto the OTHER conversation's rollout
// and end this turn on its task_complete. The interactive adapter now always
// passes a bound resolver; only the structured transport still passes nil,
// because a one-shot `--json` process has no session identity to bind.
//
// Events older than turnStart are ignored for persistent sessions that contain
// multiple turns.
type codexTurnCompletionTracker struct {
	turnStart          time.Time
	expectedWorkingDir string
	// resolveRollout, when set, returns THIS session's rollout. It is lock-free
	// (see codexRolloutResolverForSession) because the tracker polls while the
	// adapter holds session.mu. nil falls back to the unsafe directory lookup,
	// which is only correct when no other Codex session shares the directory.
	resolveRollout   func(time.Time) string
	rolloutPath      string
	offset           int64
	pendingToolCalls map[string]struct{}
	sawTurnEvent     bool
	sawFinalAnswer   bool
	// completedLatched makes completion sticky. Reading the rollout advances
	// offset past each line before matching it, so task_complete is consumed by
	// the poll that finds it and no later poll can rediscover it. Callers that
	// re-ask after observing completion — the post-completion modal grace period
	// in the interactive adapter — would otherwise see false forever and hang
	// the turn even though it finished.
	completedLatched bool
}

func newCodexTurnCompletionTracker(turnStart time.Time, expectedWorkingDir string, resolveRollout func(time.Time) string) *codexTurnCompletionTracker {
	return &codexTurnCompletionTracker{
		turnStart:          turnStart,
		expectedWorkingDir: strings.TrimSpace(expectedWorkingDir),
		resolveRollout:     resolveRollout,
		pendingToolCalls:   make(map[string]struct{}),
	}
}

func (t *codexTurnCompletionTracker) completed() bool {
	if t == nil || t.turnStart.IsZero() || t.expectedWorkingDir == "" {
		return false
	}
	if t.completedLatched {
		return true
	}
	if t.rolloutPath == "" {
		if t.resolveRollout != nil {
			t.rolloutPath = t.resolveRollout(t.turnStart)
		} else {
			t.rolloutPath = findCodexRolloutByWorkingDirUnsafe(t.turnStart, t.expectedWorkingDir)
		}
		if t.rolloutPath == "" {
			return false
		}
	}

	f, err := os.Open(t.rolloutPath)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return false
	}

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !(err == io.EOF && len(line) > 0) {
			return false
		}
		// Codex appends one complete JSON object per line. Do not consume a
		// partially-written final record; it will be retried on the next poll.
		if err == io.EOF && !strings.HasSuffix(line, "\n") {
			return false
		}
		t.offset += int64(len(line))
		if t.observe(line) {
			t.completedLatched = true
			return true
		}
		if err == io.EOF {
			return false
		}
	}
}

// blocksTerminalFallback reports whether the native rollout proves that Codex
// is still inside the turn. The TUI may show an idle composer while an MCP call
// is pending, so a ready-looking pane is not a completion signal once the
// rollout has shown tool activity or commentary without a final_answer.
func (t *codexTurnCompletionTracker) blocksTerminalFallback() bool {
	if t == nil || !t.sawTurnEvent {
		return false
	}
	return len(t.pendingToolCalls) > 0 || !t.sawFinalAnswer
}

func (t *codexTurnCompletionTracker) observe(line string) bool {
	type rolloutEvent struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type   string `json:"type"`
			Phase  string `json:"phase"`
			CallID string `json:"call_id"`
		} `json:"payload"`
	}
	var event rolloutEvent
	if json.Unmarshal([]byte(line), &event) != nil {
		return false
	}
	timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil || timestamp.Before(t.turnStart) {
		return false
	}
	t.sawTurnEvent = true

	if event.Type == "event_msg" && event.Payload.Type == "task_complete" {
		return true
	}
	if event.Type != "response_item" {
		return false
	}
	switch event.Payload.Type {
	case "function_call", "custom_tool_call", "tool_search_call":
		if event.Payload.CallID != "" {
			t.pendingToolCalls[event.Payload.CallID] = struct{}{}
		}
	case "function_call_output", "custom_tool_call_output", "tool_search_output":
		delete(t.pendingToolCalls, event.Payload.CallID)
	case "message":
		if event.Payload.Phase == "final_answer" || event.Payload.Phase == "final" {
			t.sawFinalAnswer = true
		}
	}
	return false
}

// findCodexRolloutByWorkingDirUnsafe resolves a rollout using ONLY working
// directory + newest modification time.
//
// It is named "Unsafe" deliberately: working directory is not a conversation
// identity, so this can return another session's transcript whenever two Codex
// sessions share a directory — which a workflow's Chat and Schedule always do
// (PLAT-106). Every remaining caller is a known gap tracked by PLAT-108. New
// code must use resolveCodexRolloutPath, which binds to the session's thread.
func findCodexRolloutByWorkingDirUnsafe(turnStart time.Time, expectedWorkingDir string) string {
	return findCodexRolloutByWorkingDirExcluding(turnStart, expectedWorkingDir, nil)
}

// findCodexRolloutByWorkingDirExcluding resolves a rollout by working directory
// and recency, skipping any path already claimed by another live session.
//
// Working directory alone is NOT an identity: a Chat and a Schedule for the same
// workflow run in the same directory, so "newest rollout in this cwd" can return
// the other conversation's transcript (PLAT-106). The exclusion set is the
// disambiguator until the session learns its own thread ID, after which
// findCodexRolloutForThread is exact and this path is not used.
func findCodexRolloutByWorkingDirExcluding(turnStart time.Time, expectedWorkingDir string, excluded map[string]bool) string {
	root := codexSessionsRoot()
	if root == "" {
		return ""
	}
	cutoff := turnStart.Add(-30 * time.Second)
	type candidate struct {
		path string
		mod  time.Time
	}
	var candidates []candidate
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if excluded[path] {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr == nil && !info.ModTime().Before(cutoff) {
			candidates = append(candidates, candidate{path: path, mod: info.ModTime()})
		}
		return nil
	})
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].mod.After(candidates[j].mod) })
	for _, candidate := range candidates {
		if sameCodexWorkingDir(readCodexRolloutWorkingDir(candidate.path), expectedWorkingDir) {
			return candidate.path
		}
	}
	return ""
}

// findCodexRolloutForThread resolves the rollout for an EXACT Codex thread ID.
// Codex names each rollout `rollout-<timestamp>-<thread-id>.jsonl` and repeats
// the same value in `session_meta.payload.id`, so the filename is a cheap
// pre-filter and the payload is the authority.
func findCodexRolloutForThread(threadID string) string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ""
	}
	root := codexSessionsRoot()
	if root == "" {
		return ""
	}
	var match string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" || match != "" {
			return nil
		}
		if !strings.Contains(filepath.Base(path), threadID) {
			return nil
		}
		if readCodexRolloutThreadID(path) == threadID {
			match = path
		}
		return nil
	})
	return match
}

// readCodexRolloutThreadID returns `session_meta.payload.id` — Codex's thread
// identity for the conversation recorded in this rollout.
func readCodexRolloutThreadID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	type sessionMeta struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	reader := bufio.NewReader(f)
	for i := 0; i < 8; i++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event sessionMeta
			if json.Unmarshal(line, &event) == nil && event.Type == "session_meta" {
				return strings.TrimSpace(event.Payload.ID)
			}
		}
		if readErr != nil {
			break
		}
	}
	return ""
}

func codexSessionsRoot() string {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func readCodexRolloutWorkingDir(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	type sessionMeta struct {
		Type    string `json:"type"`
		Payload struct {
			CWD string `json:"cwd"`
		} `json:"payload"`
	}
	reader := bufio.NewReader(f)
	for i := 0; i < 8; i++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event sessionMeta
			if json.Unmarshal(line, &event) == nil && event.Type == "session_meta" {
				return strings.TrimSpace(event.Payload.CWD)
			}
		}
		if readErr != nil {
			break
		}
	}
	return ""
}
