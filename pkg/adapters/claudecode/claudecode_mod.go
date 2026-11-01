package claudecode

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
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
// slot account must be able to write there), else a private folder made for
// this launch, so no other local account can read answers or plant events.
func claudeModBaseDir(opts *llmtypes.CallOptions) (claudeModBase, error) {
	if home := llmtypes.SandboxHomeEnvironment(opts)["HOME"]; strings.TrimSpace(home) != "" {
		return claudeModBase{dir: filepath.Join(home, ".agentworks-mod"), shared: true}, nil
	}
	dir, err := os.MkdirTemp("", "agentworks-mod-")
	if err != nil {
		return claudeModBase{}, err
	}
	return claudeModBase{dir: dir, private: true}, nil
}

// claudeModBase is a launch's mod folder. shared: inside a confined CLI's home,
// group-writable for a slot account. private: made for this launch alone.
type claudeModBase struct {
	dir     string
	shared  bool
	private bool
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

func claudeModBaseFor(sessionName string) (claudeModBase, bool) {
	value, ok := claudeModBases.Load(sessionName)
	if !ok {
		return claudeModBase{}, false
	}
	return value.(claudeModBase), true
}

// claudeModSessionDir is where one tmux session's mod writes its events;
// empty for a session this process did not launch with the mod.
func claudeModSessionDir(sessionName string) string {
	base, ok := claudeModBaseFor(sessionName)
	if !ok {
		return ""
	}
	return filepath.Join(base.dir, "session-"+sessionName)
}

// prepareClaudeModSessionDir creates the session's event folder. Group
// writable: a slot account runs the CLI in a folder shared with its group.
func prepareClaudeModSessionDir(sessionName string) (string, error) {
	dir := claudeModSessionDir(sessionName)
	if dir == "" {
		return "", fmt.Errorf("prepare Claude mod folder: %s was not launched with the mod", sessionName)
	}
	mode := os.FileMode(0o700)
	if base, _ := claudeModBaseFor(sessionName); base.shared {
		mode = 0o770
	}
	for _, sub := range []string{"events", "inbox"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), mode); err != nil {
			return "", fmt.Errorf("prepare Claude mod folder: %w", err)
		}
	}
	return dir, nil
}

func removeClaudeModSessionDir(sessionName string) {
	base, ok := claudeModBaseFor(sessionName)
	if !ok {
		return
	}
	if base.private {
		_ = os.RemoveAll(base.dir)
	} else {
		_ = os.RemoveAll(claudeModSessionDir(sessionName))
	}
	claudeModBases.Delete(sessionName)
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
	InputID       string  `json:"input_id,omitempty"`
	Delivery      string  `json:"delivery,omitempty"`
	Error         string  `json:"error,omitempty"`
	Trigger       string  `json:"trigger,omitempty"`
	TokensBefore  int64   `json:"tokens_before,omitempty"`
	TokensAfter   int64   `json:"tokens_after,omitempty"`
}

// readClaudeModEvents returns the session's events in the order they happened.
// File names start with the time and the mod's own counter, so name order is
// event order.
//
// Anything in the session can write the folder, the agent's own shell too, so
// events only time turns and name a session that is then checked; links and
// other non-regular files are skipped, and a private folder takes only files
// this process's account wrote.
func readClaudeModEvents(sessionName string) []claudeModEvent {
	sessionDir := claudeModSessionDir(sessionName)
	if sessionDir == "" {
		return nil
	}
	base, _ := claudeModBaseFor(sessionName)
	dir := filepath.Join(sessionDir, "events")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if base.private && !claudeModOwnedByProcess(filepath.Join(dir, entry.Name())) {
			continue
		}
		names = append(names, entry.Name())
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
		// An operator marked the visible terminal complete.
		if tmuxcontrol.ConsumeForceComplete(sessionName) {
			return claudeModEvent{Type: "force_complete"}, false
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
	if current == "" || current == session.nativeSessionID || !isUUIDLike(current) {
		return
	}
	// Only a session of this launch's own project folder: never one an event
	// names from elsewhere.
	if path, _ := resolveClaudeTranscriptPath(current, session.workingDir, false, session.accountHome); path == "" {
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
	_, ok := claudeModBaseFor(tmuxSessionName)
	return ok
}

func claudeModOwnedByProcess(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// claudeModInputTimeout bounds the wait for the mod to take a message. A new
// turn is confirmed as it starts, so a busy Claude can hold it a while.
const claudeModInputTimeout = 2 * time.Minute

var claudeModInputSeq atomic.Int64

// deliverClaudeModInput puts text in the terminal's mod inbox and waits until
// the mod has handed it to Claude: a new turn when Claude was idle, or a row
// the running turn reads at its next step. It returns how it was delivered.
func deliverClaudeModInput(ctx context.Context, sessionName, text string) (string, error) {
	event, err := claudeModRequest(ctx, sessionName, "input", text)
	return event.Delivery, err
}

// claudeModRequest asks the terminal's mod for one thing (input, abort,
// compact) and waits for its answer.
func claudeModRequest(ctx context.Context, sessionName, kind, text string) (claudeModEvent, error) {
	dir := claudeModSessionDir(sessionName)
	if dir == "" {
		return claudeModEvent{}, fmt.Errorf("Claude Code session %s does not run the AgentWorks mod", sessionName)
	}
	id := fmt.Sprintf("%019d-%06d-%s", time.Now().UnixNano(), claudeModInputSeq.Add(1), kind)
	data, _ := json.Marshal(map[string]string{"id": id, "kind": kind, "text": text})
	inbox := filepath.Join(dir, "inbox")
	// Written aside and renamed in, so the mod never reads half a request.
	tmp := filepath.Join(inbox, id+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return claudeModEvent{}, fmt.Errorf("queue %s for Claude Code: %w", kind, err)
	}
	final := filepath.Join(inbox, id+".json")
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return claudeModEvent{}, fmt.Errorf("queue %s for Claude Code: %w", kind, err)
	}
	defer os.Remove(final)
	ctx, cancel := context.WithTimeout(ctx, claudeModInputTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, event := range readClaudeModEvents(sessionName) {
			if event.Type != "input" || event.InputID != id {
				continue
			}
			if event.Error != "" {
				return event, fmt.Errorf("Claude Code did not take the %s: %s", kind, event.Error)
			}
			return event, nil
		}
		select {
		case <-ctx.Done():
			return claudeModEvent{}, fmt.Errorf("Claude Code did not take the %s in %s: %w", kind, claudeModInputTimeout, ctx.Err())
		case <-ticker.C:
		}
	}
}

// claudeModTaken remembers each owner's messages the mod confirmed, for the
// durable receipt the server asks for after a send.
var (
	claudeModTakenMu sync.Mutex
	claudeModTaken   = map[string][]claudeModTakenInput{}
)

type claudeModTakenInput struct {
	message string
	latency time.Duration
	at      time.Time
}

func recordClaudeModInputTaken(ownerSessionID, message string, latency time.Duration) {
	claudeModTakenMu.Lock()
	defer claudeModTakenMu.Unlock()
	taken := append(claudeModTaken[ownerSessionID], claudeModTakenInput{message: message, latency: latency, at: time.Now()})
	if len(taken) > 64 {
		taken = taken[len(taken)-64:]
	}
	claudeModTaken[ownerSessionID] = taken
}

func takeClaudeModInputTaken(ownerSessionID, message string) (claudeModTakenInput, bool) {
	claudeModTakenMu.Lock()
	defer claudeModTakenMu.Unlock()
	for i, input := range claudeModTaken[ownerSessionID] {
		if input.message == message {
			claudeModTaken[ownerSessionID] = append(claudeModTaken[ownerSessionID][:i:i], claudeModTaken[ownerSessionID][i+1:]...)
			return input, true
		}
	}
	return claudeModTakenInput{}, false
}

// waitForClaudeModEvent waits for the first event of a type written at or
// after since.
func waitForClaudeModEvent(ctx context.Context, sessionName, eventType string, since time.Time) (claudeModEvent, error) {
	sinceMS := since.UnixMilli()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, event := range readClaudeModEvents(sessionName) {
			if event.Type == eventType && event.At >= sinceMS {
				return event, nil
			}
		}
		select {
		case <-ctx.Done():
			return claudeModEvent{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// claudeModTurnError turns an API error that ended a turn into the error the
// stack acts on: a usage limit suspends until its reset, low credit stops.
func claudeModTurnError(sessionName, text string) error {
	switch {
	case IsClaudeUsageLimitText(text):
		return NewClaudeUsageLimitErrorForSession("claudecode", "", sessionName, text, time.Now())
	case IsClaudeCreditLowText(text):
		log.Printf("[TURN] stalled cause=credit_low provider=claude-code tmux=%s", sessionName)
		return &ClaudeCreditLowError{}
	default:
		return fmt.Errorf("claude code turn failed: %s", strings.TrimSpace(text))
	}
}
