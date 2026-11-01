package claudecode

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// The AgentWorks mod (a Claude Code plugin with a hooks module) runs inside a
// chat's Claude Code terminal and reports turns, session switches and usage as
// one JSON file per event in the session's mod folder. Claude Code itself says
// when a turn ends and which session is current, so neither has to be read off
// the screen or guessed after /clear, /resume or /branch.
//
//go:embed all:agentworksmod
var claudeModFiles embed.FS

const claudeModDirEnv = "AGENTWORKS_MOD_DIR"

// claudeModBaseDir is where a launch keeps the mod and its events: inside a
// confined CLI's private home (the shared /tmp is outside its sandbox, and a
// slot account must be able to write there), else the process temp folder.
func claudeModBaseDir(opts *llmtypes.CallOptions) string {
	if home := llmtypes.SandboxHomeEnvironment(opts)["HOME"]; strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".agentworks-mod")
	}
	return filepath.Join(os.TempDir(), "agentworks-mod")
}

// claudeModBases remembers each tmux session's base folder, so readers that
// only know the session name find its events.
var claudeModBases sync.Map

var claudeModPluginMu sync.Mutex

// claudeModPluginDir writes the embedded mod under base, in a folder named
// after its content, so a new build never loads an older copy.
func claudeModPluginDir(base string) (string, error) {
	sum := sha256.New()
	_ = fs.WalkDir(claudeModFiles, "agentworksmod", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := claudeModFiles.ReadFile(path)
			sum.Write([]byte(path))
			sum.Write(data)
		}
		return nil
	})
	dir := filepath.Join(base, "plugin-"+hex.EncodeToString(sum.Sum(nil))[:12])
	claudeModPluginMu.Lock()
	defer claudeModPluginMu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "hooks", "hooks.json")); err == nil {
		return dir, nil
	}
	err := fs.WalkDir(claudeModFiles, "agentworksmod", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, strings.TrimPrefix(path, "agentworksmod"))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := claudeModFiles.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	return dir, err
}

// claudeModSessionDir is where one tmux session's mod writes its events.
func claudeModSessionDir(sessionName string) string {
	base := filepath.Join(os.TempDir(), "agentworks-mod")
	if value, ok := claudeModBases.Load(sessionName); ok {
		base = value.(string)
	}
	return filepath.Join(base, "session-"+sessionName)
}

// prepareClaudeModSessionDir creates the session's event folder. Group
// writable: a slot account runs the CLI in a folder shared with its group.
func prepareClaudeModSessionDir(sessionName string) (string, error) {
	dir := claudeModSessionDir(sessionName)
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o770); err != nil {
		return "", fmt.Errorf("prepare Claude mod folder: %w", err)
	}
	return dir, nil
}

func removeClaudeModSessionDir(sessionName string) {
	if strings.TrimSpace(sessionName) != "" {
		_ = os.RemoveAll(claudeModSessionDir(sessionName))
		claudeModBases.Delete(sessionName)
	}
}

// claudeModEvent is one event the mod wrote. Every event names the session
// that was current when it happened.
type claudeModEvent struct {
	Type          string  `json:"type"`
	At            int64   `json:"at"`
	Seq           int64   `json:"seq"`
	SessionID     string  `json:"session_id"`
	Reason        string  `json:"reason,omitempty"`
	Command       string  `json:"command,omitempty"`
	TurnID        string  `json:"turn_id,omitempty"`
	Aborted       bool    `json:"aborted,omitempty"`
	Answer        string  `json:"answer,omitempty"`
	DurationMS    int64   `json:"duration_ms,omitempty"`
	ContextTokens int64   `json:"context_tokens,omitempty"`
	ContextWindow int64   `json:"context_window,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`
}

// readClaudeModEvents returns the session's events in the order they happened.
// File names start with the time and the mod's own counter, so name order is
// event order.
func readClaudeModEvents(sessionName string) []claudeModEvent {
	dir := filepath.Join(claudeModSessionDir(sessionName), "events")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	events := make([]claudeModEvent, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var event claudeModEvent
		if json.Unmarshal(data, &event) == nil && event.Type != "" {
			events = append(events, event)
		}
	}
	return events
}

// claudeModCurrentSessionID is the session the terminal is on now: the one
// its latest event names. Empty when the mod has reported nothing.
func claudeModCurrentSessionID(sessionName string) string {
	events := readClaudeModEvents(sessionName)
	for i := len(events) - 1; i >= 0; i-- {
		if id := strings.TrimSpace(events[i].SessionID); id != "" {
			return id
		}
	}
	return ""
}

// ClaudeModCurrentSessionID reports the native session a Claude terminal is
// on now, for a caller that follows a terminal between its own turns.
func ClaudeModCurrentSessionID(tmuxSessionName string) string {
	return claudeModCurrentSessionID(tmuxSessionName)
}

// waitForClaudeModTurnComplete waits until the turns started at or after since
// have ended, and returns the last one's end. A message queued while Claude
// answered starts another turn right after the first ends, so an end counts
// only once it is still the latest event one poll later.
func waitForClaudeModTurnComplete(ctx context.Context, sessionName string, since time.Time) (claudeModEvent, bool) {
	sinceMS := since.UnixMilli()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var pending int64 = -1
	for {
		started := false
		var end claudeModEvent
		ended := false
		for _, event := range readClaudeModEvents(sessionName) {
			if event.At < sinceMS {
				continue
			}
			switch event.Type {
			case "turn_start":
				started, ended = true, false
			case "turn_complete":
				if started {
					end, ended = event, true
				}
			}
		}
		if ended {
			if pending == end.Seq {
				return end, true
			}
			pending = end.Seq
		} else {
			pending = -1
		}
		select {
		case <-ctx.Done():
			return claudeModEvent{}, false
		case <-ticker.C:
		}
	}
}

func claudeArgsHavePluginDir(args []string) bool {
	for _, arg := range args {
		if arg == "--plugin-dir" {
			return true
		}
	}
	return false
}

// claudeModSandboxPaths adds the mod to a confined launch: its code is read,
// its session folder written.
func claudeModSandboxPaths(args []string, sessionName string, read, write []string) ([]string, []string) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--plugin-dir" {
			read = append(read, args[i+1])
			write = append(write, claudeModSessionDir(sessionName))
		}
	}
	return read, write
}

// adoptClaudeModSession moves a retained terminal to the session its mod says
// is current: /clear, /resume or /branch typed in the terminal changed it, and
// the turn, its transcript and the next resume belong to that session now.
func adoptClaudeModSession(session *claudeInteractivePersistentSession, logger interfaces.Logger) {
	if session == nil {
		return
	}
	current := claudeModCurrentSessionID(session.tmuxSessionName)
	if current == "" || current == session.nativeSessionID {
		return
	}
	if logger != nil {
		logger.Infof("[CLAUDE_MOD] session switch tmux=%s from=%s to=%s", session.tmuxSessionName, session.nativeSessionID, current)
	}
	session.nativeSessionID = current
}

// claudeModReadyTimeout bounds the wait for the mod's first event after the
// terminal starts; Claude Code loads plugins before its first prompt.
const claudeModReadyTimeout = 60 * time.Second

// waitForClaudeModReady waits for the mod's first event. A terminal without it
// cannot be driven: Claude Code older than mods, or a policy that turns mods off.
func waitForClaudeModReady(ctx context.Context, sessionName string) error {
	ctx, cancel := context.WithTimeout(ctx, claudeModReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for claudeModCurrentSessionID(sessionName) == "" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("Claude Code did not load the AgentWorks mod in %s: update Claude Code to 2.1.290 or later, and check that no managed setting turns mods off", sessionName)
		case <-ticker.C:
		}
	}
	return nil
}

// ClaudeModRunsIn reports whether a Claude terminal this process launched runs
// the AgentWorks mod. Its session switches are followed, so every slash
// command may be typed into it.
func ClaudeModRunsIn(tmuxSessionName string) bool {
	_, ok := claudeModBases.Load(tmuxSessionName)
	return ok
}
