package claudecode

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Idle compaction (PLAT-872). Claude Code compacts a live session that sat
// idle near the end of its prompt-cache lifetime, so the next message does not
// pay a full cache write of a very long history. An SDK chat starts a new
// Claude process per turn with --resume, so that native feature never fires
// there. Instead, a turn that resumes a long conversation after a long break
// first runs Claude Code's own /compact in the same run, then the person's
// message. The tmux path keeps the interactive CLI alive between turns, so
// Claude Code does it there itself.
//
// The thresholds live here and nowhere else.
const (
	// EnvClaudeIdleCompactMinTokens is Claude Code's own setting for the
	// smallest context that is compacted after a break; honoured when set.
	EnvClaudeIdleCompactMinTokens     = "CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS"
	claudeIdleCompactDefaultMinTokens = 200_000
	// claudeIdleCompactAfter: the previous turn ended this long ago, so the
	// one-hour prompt cache has most likely expired.
	claudeIdleCompactAfter = 55 * time.Minute
	// Only the transcript's tail is read: the last usage and timestamps are there.
	claudeIdleCompactTailBytes = 8 << 20
)

func claudeIdleCompactMinTokens() int {
	if raw := strings.TrimSpace(os.Getenv(EnvClaudeIdleCompactMinTokens)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return claudeIdleCompactDefaultMinTokens
}

// claudeIdleState is what the native transcript says about a conversation:
// its context size at the last model call, when it was last active, and
// whether its last event was a compaction.
type claudeIdleState struct {
	ContextTokens int
	LastActivity  time.Time
	JustCompacted bool
}

// claudeIdleCompactDecision decides whether a turn compacts first. state is
// nil when the conversation's transcript could not be read.
func claudeIdleCompactDecision(resumeSessionID string, sessionRunning bool, state *claudeIdleState, minTokens int, now time.Time) (bool, string) {
	switch {
	case strings.TrimSpace(resumeSessionID) == "":
		return false, "new_conversation"
	case sessionRunning:
		return false, "session_running"
	case state == nil:
		return false, "no_transcript"
	case state.JustCompacted:
		return false, "just_compacted"
	case state.ContextTokens < minTokens:
		return false, "below_threshold"
	case state.LastActivity.IsZero() || now.Sub(state.LastActivity) < claudeIdleCompactAfter:
		return false, "recently_active"
	}
	return true, "idle_cache_expired"
}

// readClaudeIdleState reads the tail of the conversation's native transcript.
func readClaudeIdleState(sessionID, workingDir string, accountHome ...string) *claudeIdleState {
	if !isClaudeTranscriptSessionID(sessionID) {
		return nil
	}
	f, _, err := openClaudeTranscript(sessionID, workingDir, accountHome...)
	if err != nil || f == nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	var src io.Reader = f
	partial := false
	if info.Size() > claudeIdleCompactTailBytes {
		if _, err := f.Seek(info.Size()-claudeIdleCompactTailBytes, io.SeekStart); err != nil {
			return nil
		}
		partial = true
	}
	state := parseClaudeIdleState(src, partial)
	if state != nil && state.LastActivity.IsZero() {
		state.LastActivity = info.ModTime()
	}
	return state
}

func parseClaudeIdleState(src io.Reader, skipFirstLine bool) *claudeIdleState {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	state := &claudeIdleState{}
	known := false
	for scanner.Scan() {
		if skipFirstLine {
			skipFirstLine = false // starts mid-row
			continue
		}
		var row struct {
			Type        string `json:"type"`
			Subtype     string `json:"subtype"`
			Timestamp   string `json:"timestamp"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				Usage *claudeStreamUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.IsSidechain {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil && ts.After(state.LastActivity) {
			state.LastActivity = ts
		}
		switch {
		case row.Type == "system" && row.Subtype == "compact_boundary":
			state.JustCompacted, known = true, true
		case row.Type == "assistant" && row.Message.Usage != nil:
			u := row.Message.Usage
			if total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens; total > 0 {
				state.ContextTokens, state.JustCompacted, known = total, false, true
			}
		}
	}
	if !known {
		return nil
	}
	return state
}

// decideClaudeIdleCompact reads the conversation's state, decides and logs.
func (c *ClaudeCodeInteractiveAdapter) decideClaudeIdleCompact(owner, resumeSessionID, workingDir string, opts *llmtypes.CallOptions, sessionRunning bool) bool {
	var state *claudeIdleState
	if strings.TrimSpace(resumeSessionID) != "" && !sessionRunning {
		state = readClaudeIdleState(resumeSessionID, workingDir, llmtypes.CLIHomeEnvironment(opts)["HOME"])
	}
	now := time.Now()
	compact, reason := claudeIdleCompactDecision(resumeSessionID, sessionRunning, state, claudeIdleCompactMinTokens(), now)
	if c.logger != nil {
		tokens, idleMin := 0, 0
		if state != nil {
			tokens = state.ContextTokens
			if !state.LastActivity.IsZero() {
				idleMin = int(now.Sub(state.LastActivity).Minutes())
			}
		}
		action := "skip"
		if compact {
			action = "compact"
		}
		c.logger.Infof("[IDLE_COMPACT] session=%s native=%s tokens=%d idle_min=%d action=%s reason=%s", owner, resumeSessionID, tokens, idleMin, action, reason)
	}
	return compact
}
