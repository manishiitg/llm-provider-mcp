package musecli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Dropped-send recovery (PLAT-422 follow-up): a TUI that answers the first
// Enter with "Message not sent -- another run is still starting" and never
// records intake must see the message resubmitted once its run starts —
// exactly once per round, never on a stale notice over landed intake.
//
// A stand-in `tmux` plays the TUI and appends native-log rows to a fixture
// dataHome tree, so no CLI runs.
func writeMuseDroppedSendTree(t *testing.T) (dataHome, sessionLog string) {
	t.Helper()
	dataHome = t.TempDir()
	sessionLog = filepath.Join(dataHome, "muse", "sessions", "2026", "10", "12", "probe-session", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionLog, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	return dataHome, sessionLog
}

func countMuseSubmitCalls(t *testing.T, log string) (typed, enters, clears int) {
	t.Helper()
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(calls), "\n") {
		switch {
		case strings.HasPrefix(line, "send-keys") && strings.Contains(line, " -l "):
			typed++
		case strings.HasPrefix(line, "send-keys") && strings.Contains(line, "Enter"):
			enters++
		case strings.HasPrefix(line, "send-keys") && strings.Contains(line, "C-u"):
			clears++
		}
	}
	return typed, enters, clears
}

func TestMuseConfirmResubmitsAfterRunStartWithoutIntake(t *testing.T) {
	dir := t.TempDir()
	dataHome, sessionLog := writeMuseDroppedSendTree(t)
	state := filepath.Join(dir, "state")
	log := filepath.Join(dir, "calls.log")
	refusedOnce := filepath.Join(dir, "refused")
	script := `#!/bin/sh
echo "$@" >> '` + log + `'
now="$(date +%s)000000"
case "$1" in
  capture-pane)
    case "$(cat '` + state + `' 2>/dev/null)" in
      refused) printf '! Message not sent — another run is still starting\n❯ hello world\n' ;;
      accepted) printf 'thinking...\n❯ \n' ;;
      typed)   printf '❯ hello world\n' ;;
      *)       printf '❯ \n' ;;
    esac ;;
  send-keys)
    case "$*" in
      *" -l "*) echo typed > '` + state + `' ;;
      *Enter*)
        case "$(cat '` + state + `' 2>/dev/null)" in
          typed)
            if [ -f '` + refusedOnce + `' ]; then
              echo accepted > '` + state + `'
              printf '{"sequence":12,"recorded_at":%s,"record_type":"event","payload_type":"runtime.user_intent.accepted","payload":{"intent_id":"run-2","refill_blocks":[{"kind":"text","text":"hello world"}]}}\n' "$now" >> '` + sessionLog + `'
            else
              touch '` + refusedOnce + `'
              echo refused > '` + state + `'
              printf '{"sequence":11,"recorded_at":%s,"record_type":"event","payload_type":"runtime.session","payload":{"kind":"run","run_id":"run-1","event":{"kind":"started"}}}\n' "$now" >> '` + sessionLog + `'
            fi ;;
        esac ;;
      *C-u*) echo cleared > '` + state + `' ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(prevConfirm, prevStart time.Duration, prevRetries int) {
		museResubmitShortConfirm, museResubmitRunStartWait, museResubmitMaxRetries = prevConfirm, prevStart, prevRetries
	}(museResubmitShortConfirm, museResubmitRunStartWait, museResubmitMaxRetries)
	museResubmitShortConfirm, museResubmitRunStartWait, museResubmitMaxRetries = time.Second, 5*time.Second, 2

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The stub stamps rows with shell-second precision; backdate the submit
	// so microsecond comparison cannot reject them.
	turnStart := time.Now().Add(-2 * time.Second)
	if err := museSendPrompt(ctx, "mlp-muse-test", "hello world"); err != nil {
		t.Fatalf("initial submit: %v", err)
	}
	if err := museConfirmSubmitOrResubmit(ctx, dataHome, "mlp-muse-test", "hello world", turnStart, ""); err != nil {
		t.Fatalf("dropped send must be resubmitted after the run starts: %v", err)
	}
	if typed, enters, clears := countMuseSubmitCalls(t, log); typed != 2 || enters != 2 || clears != 1 {
		t.Fatalf("want one resubmit (typed=2 submits=2 clears=1); typed=%d submits=%d clears=%d", typed, enters, clears)
	}
}

func TestMuseConfirmFailsClearWhenRunNeverStarts(t *testing.T) {
	dir := t.TempDir()
	dataHome, _ := writeMuseDroppedSendTree(t)
	state := filepath.Join(dir, "state")
	log := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "$@" >> '` + log + `'
case "$1" in
  capture-pane)
    case "$(cat '` + state + `' 2>/dev/null)" in
      refused) printf '! Message not sent — another run is still starting\n❯ hello world\n' ;;
      typed)   printf '❯ hello world\n' ;;
      *)       printf '❯ \n' ;;
    esac ;;
  send-keys)
    case "$*" in
      *" -l "*) echo typed > '` + state + `' ;;
      *Enter*)  echo refused > '` + state + `' ;;
    esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(prevConfirm, prevStart time.Duration, prevRetries int) {
		museResubmitShortConfirm, museResubmitRunStartWait, museResubmitMaxRetries = prevConfirm, prevStart, prevRetries
	}(museResubmitShortConfirm, museResubmitRunStartWait, museResubmitMaxRetries)
	museResubmitShortConfirm, museResubmitRunStartWait, museResubmitMaxRetries = time.Second, 2*time.Second, 2

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	turnStart := time.Now()
	if err := museSendPrompt(ctx, "mlp-muse-test", "hello world"); err != nil {
		t.Fatalf("initial submit: %v", err)
	}
	err := museConfirmSubmitOrResubmit(ctx, dataHome, "mlp-muse-test", "hello world", turnStart, "")
	if err == nil || !strings.Contains(err.Error(), "never became ready") {
		t.Fatalf("a refusal with no run start must fail clearly, got: %v", err)
	}
	if typed, enters, clears := countMuseSubmitCalls(t, log); typed != 1 || enters != 1 || clears != 0 {
		t.Fatalf("nothing must be retyped without a run start; typed=%d submits=%d clears=%d", typed, enters, clears)
	}
}

func TestMuseRunStartedSince(t *testing.T) {
	dataHome, sessionLog := writeMuseDroppedSendTree(t)
	since := time.Now()
	runRow := func(seq, at int64, kind string) string {
		return fmt.Sprintf(`{"sequence":%d,"recorded_at":%d,"record_type":"event","payload_type":"runtime.session","payload":{"kind":"run","run_id":"r1","event":{"kind":%q}}}`, seq, at, kind)
	}
	if museRunStartedSince(dataHome, since, "") {
		t.Fatal("empty log must not report a run start")
	}
	old := since.Add(-time.Hour).UnixMicro()
	now := time.Now().UnixMicro()
	if err := os.WriteFile(sessionLog, []byte(runRow(1, old, "started")+"\n"+runRow(2, now, "terminal")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if museRunStartedSince(dataHome, since, "") {
		t.Fatal("only an old start and a terminal event must not report a run start")
	}
	content, err := os.ReadFile(sessionLog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionLog, append(content, []byte(runRow(3, now, "started")+"\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	if !museRunStartedSince(dataHome, since, "") {
		t.Fatal("a run started event after the submit must be reported")
	}
}
