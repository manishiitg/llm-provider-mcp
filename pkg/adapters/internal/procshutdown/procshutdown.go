// Package procshutdown implements the structured-CLI process shutdown
// contract documented in docs/coding_sdk_structured_contract.md (section
// "Process Shutdown Contract", certification matrix row #22).
//
// The contract is: once an adapter has observed the CLI's terminal event
// (result / task_complete / done), tear down the subprocess with
//
//	SIGTERM  →  10s  →  SIGTERM  →  10s  →  SIGTERM  →  5s  →  SIGKILL
//
// Three SIGTERMs give a CLI multiple opportunities to notice the signal —
// useful for Node.js-based CLIs whose event loop may not service signals
// promptly while blocked in a syscall (e.g. a synchronous HTTP call to
// their MCP bridge). After 25s the kernel-unblockable SIGKILL fires so an
// uncooperative CLI cannot leak as an orphan.
//
// The graces (10/10/5s) exist so the CLI can flush state the *next* call
// needs (session files for --resume, transcript JSONL, etc.). Token usage
// and pricing data must already be captured from the terminal event itself,
// so escalating to SIGKILL after 25s does not lose billing data — it only
// forfeits --resume for that turn.
package procshutdown

import (
	"os/exec"
	"syscall"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
)

// Package-level vars rather than const so tests can shrink them. Production
// callers should never override these — the contract is uniform across all
// structured CLI adapters.
var (
	FirstGrace  = 10 * time.Second
	SecondGrace = 10 * time.Second
	ThirdGrace  = 5 * time.Second
)

// Graceful tears down a CLI subprocess according to the structured shutdown
// contract. It is intended to be launched as a goroutine from the adapter's
// stdout-decode loop the moment a terminal event is observed:
//
//	go procshutdown.Graceful(cmd, decodeDone, g.logger)
//
// `terminated` MUST be a channel the caller closes when the subprocess's
// stdout reaches EOF (i.e. the process has exited and the decode goroutine
// has finished draining its output). In practice this is the same
// `decodeDone` channel the adapter's main goroutine selects on.
//
// Graceful returns once the shutdown sequence is complete (process exited
// at some stage of the sequence, or SIGKILL has been sent). It does NOT
// call cmd.Wait — the adapter's existing main goroutine is responsible for
// reaping.
//
// The subprocess must have been started with syscall.SysProcAttr{Setpgid: true}
// so the kill targets the whole process group (-pid), reaching any child
// workers the CLI spawned. All structured-CLI adapters in this repo do this.
//
// Edge cases:
//   - cmd or cmd.Process nil: returns immediately, no-op.
//   - syscall.Kill returning ESRCH (process already gone): logged at info, returns.
//   - terminated already closed when called: SIGTERM still sent (idempotent on a
//     dead process group); function returns on the immediate <-terminated read.
func Graceful(cmd *exec.Cmd, terminated <-chan struct{}, logger interfaces.Logger) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if logger == nil {
		logger = noopLogger{}
	}
	pid := cmd.Process.Pid

	// Attempt #1 — first SIGTERM with the longest grace, since this is when
	// the CLI is most likely to do useful state-flush work.
	if !sigtermAndWait(pid, terminated, FirstGrace, 1, logger) {
		return
	}

	// Attempt #2 — second SIGTERM. If the first signal was lost in the noise
	// (very rare on Unix but possible on Node.js if the event loop was busy),
	// the second one lands on a now-warmer event loop. If the CLI just
	// ignores SIGTERM (e.g. trap '' TERM), this won't help — but it also
	// won't hurt, and gives operators a second log marker.
	if !sigtermAndWait(pid, terminated, SecondGrace, 2, logger) {
		return
	}

	// Attempt #3 — last polite nudge before we escalate to SIGKILL.
	if !sigtermAndWait(pid, terminated, ThirdGrace, 3, logger) {
		return
	}

	// SIGKILL — unblockable by the kernel. After this, the process is dead
	// within microseconds and stdout will close (terminated will fire).
	logger.Infof("[SHUTDOWN] all 3 SIGTERMs exhausted for pid %d — sending SIGKILL", pid)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		logger.Errorf("[SHUTDOWN] SIGKILL to pgrp %d failed: %v", pid, err)
	}
}

// sigtermAndWait sends a SIGTERM to the process group and waits up to
// `grace` for the terminated channel to close. Returns true if the caller
// should proceed to the next escalation step, false if the process has
// already exited (or the kill failed with ESRCH) and we can return early.
func sigtermAndWait(pid int, terminated <-chan struct{}, grace time.Duration, attempt int, logger interfaces.Logger) (proceed bool) {
	logger.Infof("[SHUTDOWN] SIGTERM attempt %d/3 to pgrp %d (grace=%s)", attempt, pid, grace)
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			logger.Infof("[SHUTDOWN] SIGTERM #%d: pgrp %d already gone", attempt, pid)
			return false
		}
		logger.Errorf("[SHUTDOWN] SIGTERM #%d to pgrp %d failed: %v", attempt, pid, err)
		// Try to keep going anyway — the next escalation might land.
	}

	select {
	case <-terminated:
		logger.Infof("[SHUTDOWN] process %d exited after SIGTERM #%d (within %s)", pid, attempt, grace)
		return false
	case <-time.After(grace):
		logger.Infof("[SHUTDOWN] SIGTERM #%d grace (%s) expired for pid %d", attempt, grace, pid)
		return true
	}
}

// noopLogger satisfies interfaces.Logger for callers that pass nil. Adapters
// in this repo guard their own logger calls with nil checks; mirroring that
// here means Graceful is safe to use from any code path without a separate
// fallback at every call site.
type noopLogger struct{}

func (noopLogger) Infof(string, ...any)  {}
func (noopLogger) Errorf(string, ...any) {}
func (noopLogger) Debugf(string, ...any) {}
