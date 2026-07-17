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
// The rollout is selected by its session_meta.cwd. Interactive sessions use a
// unique working directory, so this remains isolated when several Codex agents
// run concurrently. Events older than turnStart are ignored for persistent
// sessions that contain multiple turns.
type codexTurnCompletionTracker struct {
	turnStart          time.Time
	expectedWorkingDir string
	rolloutPath        string
	offset             int64
	pendingToolCalls   map[string]struct{}
	sawTurnEvent       bool
	sawFinalAnswer     bool
}

func newCodexTurnCompletionTracker(turnStart time.Time, expectedWorkingDir string) *codexTurnCompletionTracker {
	return &codexTurnCompletionTracker{
		turnStart:          turnStart,
		expectedWorkingDir: strings.TrimSpace(expectedWorkingDir),
		pendingToolCalls:   make(map[string]struct{}),
	}
}

func (t *codexTurnCompletionTracker) completed() bool {
	if t == nil || t.turnStart.IsZero() || t.expectedWorkingDir == "" {
		return false
	}
	if t.rolloutPath == "" {
		t.rolloutPath = findCodexRolloutForTurn(t.turnStart, t.expectedWorkingDir)
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

func findCodexRolloutForTurn(turnStart time.Time, expectedWorkingDir string) string {
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
