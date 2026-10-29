package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// A Claude Agent SDK chat runs one bridge process per session (PLAT-872). A turn
// owns the process while it runs. When the turn ends at its answer while tasks
// Claude itself started with run_in_background are still running (a dev server,
// a long build), the process is kept ("parked") instead of exiting, so those
// tasks keep running; the next turn of the same chat sends its prompt into the
// same process when its launch is unchanged. A kept process closes when its
// tasks finish, when the person presses Stop, when the chat closes, when the
// next turn needs a different launch, or after the shared idle limit. Server
// restarts end kept processes, as for Codex and Cursor.

// SDKBackgroundTask is one background task Claude started itself that is still running.
type SDKBackgroundTask struct {
	TaskID      string    `json:"task_id"`
	Description string    `json:"description,omitempty"`
	Command     string    `json:"command,omitempty"`
	StartedAt   time.Time `json:"started_at"`
}

// SDKBackgroundUpdate is the full list of a chat's running background tasks.
// Closed names why a kept session ended ("finished", "stopped", "idle",
// "replaced"); the list is then empty. IdleLimit is the idle bound in use.
type SDKBackgroundUpdate struct {
	Owner     string
	Tasks     []SDKBackgroundTask
	Closed    string
	IdleLimit time.Duration
}

var claudeSDKBackgroundListener atomic.Pointer[func(SDKBackgroundUpdate)]

// SetSDKBackgroundListener receives every change of a chat's background task
// list, during turns and between them. The host shows it; nothing is decided there.
func SetSDKBackgroundListener(listener func(SDKBackgroundUpdate)) {
	if listener == nil {
		claudeSDKBackgroundListener.Store(nil)
		return
	}
	claudeSDKBackgroundListener.Store(&listener)
}

func notifyClaudeSDKBackground(update SDKBackgroundUpdate) {
	if update.Owner == "" {
		return
	}
	update.IdleLimit = persistentInteractiveIdleTimeout()
	if listener := claudeSDKBackgroundListener.Load(); listener != nil {
		(*listener)(update)
	}
}

type claudeSDKWireTask struct {
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
	Command     string `json:"command"`
	StartedAt   int64  `json:"started_at"`
}

const claudeBackgroundCommandLimit = 120

var claudeInlineAssignment = regexp.MustCompile(`^(?:export\s+)?[A-Za-z_][A-Za-z0-9_]*=(?:'[^']*'|"[^"]*"|\S*)\s+`)

// claudeBackgroundCommandLabel is the short command shown to the person: the
// first line, without leading NAME=value assignments (they can carry values
// that must never be shown), trimmed to a readable length.
func claudeBackgroundCommandLabel(command string) string {
	command = strings.TrimSpace(strings.SplitN(strings.TrimSpace(command), "\n", 2)[0])
	for {
		trimmed := claudeInlineAssignment.ReplaceAllString(command, "")
		if trimmed == command {
			break
		}
		command = strings.TrimSpace(trimmed)
	}
	if claudeInlineAssignment.MatchString(command + " ") {
		return ""
	}
	if runes := []rune(command); len(runes) > claudeBackgroundCommandLimit {
		command = strings.TrimSpace(string(runes[:claudeBackgroundCommandLimit-1])) + "…"
	}
	return command
}

func claudeSDKTasksFromWire(wire []claudeSDKWireTask) []SDKBackgroundTask {
	tasks := make([]SDKBackgroundTask, 0, len(wire))
	for _, task := range wire {
		if strings.TrimSpace(task.TaskID) == "" {
			continue
		}
		started := time.Now()
		if task.StartedAt > 0 {
			started = time.UnixMilli(task.StartedAt)
		}
		tasks = append(tasks, SDKBackgroundTask{TaskID: task.TaskID, Description: strings.TrimSpace(task.Description), Command: claudeBackgroundCommandLabel(task.Command), StartedAt: started})
	}
	return tasks
}

// claudeSDKLaunch describes what a kept session was started with, one digest
// per named part: model, instructions, permission mode, process environment
// (account and secrets), native questions, confinement, runtime folder and
// each launch option except the resume id. Only digests are kept, never
// another copy of credentials. The conversation is compared on its own.
type claudeSDKLaunch map[string][32]byte

func claudeSDKLaunchParts(model, systemPrompt, launchExtra, permissionMode string, environment []string, opts *llmtypes.CallOptions, questions bool) claudeSDKLaunch {
	parts := claudeSDKLaunch{}
	add := func(name string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			// Not comparable by content: compare its type only.
			raw = []byte(fmt.Sprintf("%T", value))
		}
		parts[name] = sha256.Sum256(raw)
	}
	add("model", model)
	add("system_prompt", systemPrompt)
	add("skill_instructions", launchExtra)
	add("permission_mode", permissionMode)
	add("environment", environment)
	add("native_questions", questions)
	if opts != nil {
		add("security", opts.CLISecurity)
		add("runtime_dir", opts.CLIRuntimeDir)
		if opts.Metadata != nil {
			for key, value := range opts.Metadata.Custom {
				if key != MetadataKeyResumeSessionID {
					add("option:"+key, value)
				}
			}
		}
	}
	return parts
}

// changed names the parts that differ (names only, never values).
func (kept claudeSDKLaunch) changed(wanted claudeSDKLaunch) []string {
	var names []string
	for name, digest := range wanted {
		if old, ok := kept[name]; !ok || old != digest {
			names = append(names, name)
		}
	}
	for name := range kept {
		if _, ok := wanted[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// claudeSDKReuseDecision says whether a kept session may take a turn, and why not.
func claudeSDKReuseDecision(kept, wanted claudeSDKLaunch, keptSessionID, resumeSessionID string) (bool, string) {
	if changed := kept.changed(wanted); len(changed) > 0 {
		return false, "launch_changed:" + strings.Join(changed, ",")
	}
	switch {
	case strings.TrimSpace(resumeSessionID) == "":
		return false, "new_conversation"
	case strings.TrimSpace(resumeSessionID) != strings.TrimSpace(keptSessionID):
		return false, "other_conversation"
	}
	return true, ""
}

type claudeSDKInput struct {
	mu sync.Mutex
	w  io.WriteCloser
}

func (in *claudeSDKInput) write(value any) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	return json.NewEncoder(in.w).Encode(value)
}

type claudeLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *claudeLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *claudeLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// claudeLineReader turns a process's stdout into lines. The channel closes at
// EOF; err is valid after done closes.
type claudeLineReader struct {
	lines chan string
	done  chan struct{}
	err   error
}

func startClaudeLineReader(stdout io.Reader) *claudeLineReader {
	r := &claudeLineReader{lines: make(chan string, 256), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		defer close(r.lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
		for scanner.Scan() {
			r.lines <- scanner.Text()
		}
		r.err = scanner.Err()
	}()
	return r
}

type claudeSDKSession struct {
	owner   string
	launch  claudeSDKLaunch
	cmd     *exec.Cmd
	input   *claudeSDKInput
	reader  *claudeLineReader
	stderr  *claudeLockedBuffer
	exited  chan struct{}
	waitErr error // valid after exited closes
	kill    context.CancelFunc
	logger  interfaces.Logger

	mu        sync.Mutex
	nativeID  string
	tasks     []SDKBackgroundTask
	parked    bool
	closing   bool
	idle      *time.Timer
	drainStop chan struct{}
	drainDone chan struct{}
}

// startClaudeSDKSession starts the bridge. cleanup runs once the process has exited.
func startClaudeSDKSession(owner string, launch claudeSDKLaunch, cmd *exec.Cmd, stdin io.WriteCloser, stdout io.Reader, stderr *claudeLockedBuffer, kill context.CancelFunc, cleanup func(), logger interfaces.Logger) (*claudeSDKSession, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &claudeSDKSession{owner: owner, launch: launch, cmd: cmd, input: &claudeSDKInput{w: stdin}, reader: startClaudeLineReader(stdout), stderr: stderr, exited: make(chan struct{}), kill: kill, logger: logger}
	go func() {
		<-s.reader.done
		s.waitErr = cmd.Wait()
		kill()
		_ = stdin.Close()
		cleanup()
		// However the process ended (tasks finished, Stop, an interrupted turn,
		// a crash), the chat's list must not keep showing tasks that died with it.
		s.mu.Lock()
		report := !s.closing && (s.parked || len(s.tasks) > 0)
		s.closing = true
		s.tasks = nil
		if s.idle != nil {
			s.idle.Stop()
		}
		s.mu.Unlock()
		s.unregister()
		if report {
			s.logf("[SDK_BACKGROUND] stopped session=%s reason=process_exited", s.owner)
			notifyClaudeSDKBackground(SDKBackgroundUpdate{Owner: s.owner, Closed: "finished"})
		}
		close(s.exited)
	}()
	return s, nil
}

func (s *claudeSDKSession) hasExited() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

func (s *claudeSDKSession) setNativeID(id string) {
	if id = strings.TrimSpace(id); id != "" {
		s.mu.Lock()
		s.nativeID = id
		s.mu.Unlock()
	}
}

func (s *claudeSDKSession) setTasks(tasks []SDKBackgroundTask) {
	s.mu.Lock()
	s.tasks = tasks
	s.mu.Unlock()
	notifyClaudeSDKBackground(SDKBackgroundUpdate{Owner: s.owner, Tasks: tasks})
}

func (s *claudeSDKSession) taskCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}

func (s *claudeSDKSession) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Infof(format, args...)
	}
}

var claudeSDKKept = struct {
	sync.Mutex
	sessions map[string]*claudeSDKSession
}{sessions: map[string]*claudeSDKSession{}}

// park keeps the session after its turn: it starts the idle bound and a reader
// that handles output while no turn owns the session.
func (s *claudeSDKSession) park() {
	idle := persistentInteractiveIdleTimeout()
	s.mu.Lock()
	s.parked = true
	s.drainStop, s.drainDone = make(chan struct{}), make(chan struct{})
	s.idle = time.AfterFunc(idle, func() { s.closeIfParked("idle", "expired", fmt.Sprintf("idle %s", idle)) })
	tasks := len(s.tasks)
	s.mu.Unlock()
	claudeSDKKept.Lock()
	previous := claudeSDKKept.sessions[s.owner]
	claudeSDKKept.sessions[s.owner] = s
	claudeSDKKept.Unlock()
	if previous != nil && previous != s {
		previous.closeIfParked("replaced", "stopped", "another session kept for this chat")
	}
	s.logf("[SDK_BACKGROUND] kept session=%s tasks=%d reason=own_background_tasks_running idle_limit=%s", s.owner, tasks, idle)
	go s.drain()
}

// drain reads the kept session's output while no turn owns it. A model turn the
// SDK runs on its own (for example after a task notification) is logged and
// dropped from the chat; a permission request is refused because nobody is
// there to answer it; an empty task list closes the session.
func (s *claudeSDKSession) drain() {
	s.mu.Lock()
	stop, done := s.drainStop, s.drainDone
	s.mu.Unlock()
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case line, ok := <-s.reader.lines:
			if !ok {
				return // the process exited; its waiter reports it
			}
			s.handleKeptLine(line)
		}
	}
}

func (s *claudeSDKSession) handleKeptLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return
	}
	var ev claudeStreamEvent
	if json.Unmarshal([]byte(line), &ev) != nil {
		return
	}
	switch ev.Type {
	case "sdk_background":
		tasks := claudeSDKTasksFromWire(ev.Background)
		s.setTasks(tasks)
		if len(tasks) == 0 {
			go s.closeIfParked("finished", "stopped", "tasks_finished")
		}
	case "sdk_permission":
		s.logf("[SDK_BACKGROUND] refused tool request between turns session=%s tool=%s", s.owner, ev.ToolName)
		_ = s.input.write(map[string]any{"type": "permission", "id": ev.RequestID, "response": map[string]any{"behavior": "deny", "message": "Nobody is in this chat turn to answer. Ask again in chat."}})
	case "result":
		output := 0
		if ev.Usage != nil {
			output = ev.Usage.OutputTokens
		}
		s.logf("[SDK_BACKGROUND] dropped a model turn between chat turns session=%s output_tokens=%d is_error=%t", s.owner, output, ev.IsError)
	}
}

func (s *claudeSDKSession) unregister() {
	claudeSDKKept.Lock()
	if claudeSDKKept.sessions[s.owner] == s {
		delete(claudeSDKKept.sessions, s.owner)
	}
	claudeSDKKept.Unlock()
}

// closeIfParked closes a kept session: Claude's own background tasks are
// stopped, input ends and the process exits (killed if it does not within a
// short grace). A session a turn has taken is left to that turn.
func (s *claudeSDKSession) closeIfParked(closed, verb, reason string) bool {
	s.mu.Lock()
	if !s.parked || s.closing {
		s.mu.Unlock()
		return false
	}
	s.closing = true
	if s.idle != nil {
		s.idle.Stop()
	}
	tasks := len(s.tasks)
	s.tasks = nil
	s.mu.Unlock()
	s.unregister()
	s.logf("[SDK_BACKGROUND] %s session=%s tasks=%d reason=%s", verb, s.owner, tasks, reason)
	notifyClaudeSDKBackground(SDKBackgroundUpdate{Owner: s.owner, Closed: closed})
	s.shutdown()
	return true
}

const claudeSDKCloseGrace = 15 * time.Second

func (s *claudeSDKSession) shutdown() {
	_ = s.input.write(map[string]any{"type": "stop_background", "id": randomHex(12)})
	go func() {
		select {
		case <-s.exited:
		case <-time.After(claudeSDKCloseGrace):
			s.logf("[SDK_BACKGROUND] session=%s did not exit after stop; ending the process", s.owner)
			s.kill()
		}
	}()
}

// endReleased ends a session whose turn was released at its answer: input
// ends, remaining output is discarded, and the process is ended if it does not
// exit within a short grace.
func (s *claudeSDKSession) endReleased() {
	go func() {
		for range s.reader.lines {
		}
	}()
	go func() {
		select {
		case <-s.exited:
		case <-time.After(claudeSDKCloseGrace):
			s.logf("[SDK_BACKGROUND] session=%s did not exit after its turn was released; ending the process", s.owner)
			s.kill()
		}
	}()
}

// takeKeptClaudeSDKSession hands a chat's kept session to its next turn when
// the launch is unchanged and the turn continues the same conversation.
// Otherwise the kept session is closed and the caller starts a new one.
func takeKeptClaudeSDKSession(owner string, launch claudeSDKLaunch, resumeSessionID string) *claudeSDKSession {
	claudeSDKKept.Lock()
	s := claudeSDKKept.sessions[owner]
	delete(claudeSDKKept.sessions, owner)
	claudeSDKKept.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	ok, reason := claudeSDKReuseDecision(s.launch, launch, s.nativeID, resumeSessionID)
	if s.hasExited() {
		ok, reason = false, "process_exited"
	}
	if !ok {
		s.mu.Unlock()
		// Still parked: its reader keeps consuming output until the process exits.
		s.closeIfParked("replaced", "stopped", reason)
		return nil
	}
	s.parked = false
	if s.idle != nil {
		s.idle.Stop()
	}
	stop, done := s.drainStop, s.drainDone
	tasks := len(s.tasks)
	s.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	s.logf("[SDK_BACKGROUND] reused session=%s tasks=%d", owner, tasks)
	return s
}

// closeKeptClaudeSDKSession closes a chat's kept session, stopping its tasks.
func closeKeptClaudeSDKSession(owner, reason string) bool {
	claudeSDKKept.Lock()
	s := claudeSDKKept.sessions[strings.TrimSpace(owner)]
	claudeSDKKept.Unlock()
	if s == nil {
		return false
	}
	return s.closeIfParked("stopped", "stopped", reason)
}

func claudeSDKSessionForOwner(owner string) *claudeSDKSession {
	owner = strings.TrimSpace(owner)
	claudeSDKKept.Lock()
	kept := claudeSDKKept.sessions[owner]
	claudeSDKKept.Unlock()
	if kept != nil {
		return kept
	}
	if process, ok := claudeSDKProcesses.Get(owner); ok {
		return process.session
	}
	return nil
}

// ClaudeSDKBackgroundTasks lists the background tasks Claude started itself in
// this chat that are still running, during a turn or between turns.
func ClaudeSDKBackgroundTasks(owner string) []SDKBackgroundTask {
	s := claudeSDKSessionForOwner(owner)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil
	}
	return append([]SDKBackgroundTask(nil), s.tasks...)
}

// StopClaudeSDKBackgroundTask stops one background task Claude started itself
// in this chat. When it was the last one of a kept session, the empty list that
// follows closes the session. False when the chat has no such task.
func StopClaudeSDKBackgroundTask(owner, taskID string) bool {
	s := claudeSDKSessionForOwner(owner)
	if s == nil {
		return false
	}
	found := false
	for _, task := range ClaudeSDKBackgroundTasks(owner) {
		found = found || task.TaskID == taskID
	}
	if !found {
		return false
	}
	s.logf("[SDK_BACKGROUND] stop requested session=%s task=%s", s.owner, taskID)
	return s.input.write(map[string]any{"type": "stop_task", "task_id": taskID}) == nil
}
