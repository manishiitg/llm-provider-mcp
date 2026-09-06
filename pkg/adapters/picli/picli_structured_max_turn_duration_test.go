package picli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// The regression this exists for, measured live 2026-08-19: a form-26as pi
// process sat with a completely idle Node event loop for over an hour, its
// last tool call having finished in 28ms with nothing after. Nothing bounded
// it -- TOOL_EXECUTION_TIMEOUT only wraps individual tool calls, and this
// deployment did not even have it set. This is pi's own documented failure
// mode (github.com/earendil-works/pi#8004: two real sessions frozen 5.5 and
// 8.7 hours by exactly this shape), not something the CLI can be trusted to
// recover from on its own.
//
// The script backgrounds its own hang (`sleep 999999 &`) and records BOTH the
// script's own pid ($$) and the backgrounded sleep's pid ($!) before
// `wait`-ing on it -- a real one-liner that would forward its own PID via an
// exec-tail-call optimization (had the script simply ended in `sleep
// 999999`) would have made this indistinguishable from the earlier, weaker
// version of this test, which checked only $$ and passed even while a real
// grandchild leaked: it happened to observe the shell interpreter's own pid
// dying without ever checking whether anything IT spawned also died.
// Confirmed live while writing this: three separate runs each left a real,
// reparented (ppid=1) `sleep 999999` process behind, invisible to a
// $$-only check.
func TestPiStructuredEnforcesMaxTurnDurationOnAWedgedProcess(t *testing.T) {
	t.Setenv("PI_STRUCTURED_MAX_TURN_DURATION", "700ms")
	// This test deliberately installs a fake pi on PATH. A developer machine
	// may opt into AgentWorks' managed CLI shims globally; keep that integration
	// setting from replacing the fixture executable.
	t.Setenv("AGENTWORKS_MANAGED_CLI_BIN", "")

	pidFile := filepath.Join(t.TempDir(), "pi.pid")
	childPidFile := filepath.Join(t.TempDir(), "pi.child.pid")
	writeFakePiScript(t, "echo $$ > '"+pidFile+"'\nsleep 999999 &\necho $! > '"+childPidFile+"'\nwait\n")

	adapter := &PiCLIAdapter{}
	// Deliberately generous and NOT what should stop this call -- the whole
	// point is proving the internal ceiling fires on its own, not that the
	// caller happened to time out too.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	_, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "hi"}},
		}},
		WithPiStructuredTransport(true),
		WithWorkingDir(t.TempDir()),
	)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a process that never produced a valid response")
	}
	// Generous upper bound: the ceiling itself is 700ms, this allows for
	// process-teardown overhead without allowing the old unbounded behaviour
	// (30s+, the caller's own timeout) to pass as a false negative.
	if elapsed > 5*time.Second {
		t.Fatalf("GenerateContent took %s; piMaxTurnDuration did not bound a wedged process (would have run until the caller's own 30s timeout, or forever with a longer one)", elapsed)
	}
	if elapsed < 600*time.Millisecond {
		t.Fatalf("GenerateContent took only %s, under the 700ms ceiling -- suspicious; check this did not resolve for an unrelated reason", elapsed)
	}

	assertPidIsDead(t, pidFile, "the pi script's own process")
	assertPidIsDead(t, childPidFile, "its backgrounded grandchild")
}

func assertPidIsDead(t *testing.T, pidFile, label string) {
	t.Helper()
	pidBytes, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("%s never recorded its pid: %v", label, readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if convErr != nil {
		t.Fatalf("%s: bad pid file contents %q: %v", label, pidBytes, convErr)
	}
	// FindProcess always succeeds on Unix; Signal(0) is the actual liveness
	// check, and must fail once piMaxTurnDuration's kill has taken effect.
	proc, _ := os.FindProcess(pid)
	if sigErr := proc.Signal(syscall.Signal(0)); sigErr == nil {
		_ = proc.Kill() // do not leak past a failing test either
		t.Fatalf("%s (pid %d) is still alive after the turn ceiling fired -- reported as recovered but actually leaked", label, pid)
	}
}
