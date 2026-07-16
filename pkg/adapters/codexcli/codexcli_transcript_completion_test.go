package codexcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexTurnCompletionTrackerUsesMatchingRolloutTaskComplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "16")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	wantedWorkingDir := filepath.Join(t.TempDir(), "wanted-session")
	otherWorkingDir := filepath.Join(t.TempDir(), "other-session")
	turnStart := time.Now().UTC().Add(-time.Second)
	completionTime := turnStart.Add(500 * time.Millisecond).Format(time.RFC3339Nano)

	writeRollout := func(name, cwd string, complete bool) string {
		t.Helper()
		path := filepath.Join(dayDir, name)
		lines := []string{fmt.Sprintf(`{"type":"session_meta","payload":{"cwd":%q}}`, cwd)}
		if complete {
			lines = append(lines, fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_complete"}}`, completionTime))
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("write rollout: %v", err)
		}
		return path
	}
	wanted := writeRollout("rollout-2026-07-16T12-00-00-11111111-1111-4111-8111-111111111111.jsonl", wantedWorkingDir, true)
	other := writeRollout("rollout-2026-07-16T12-00-01-22222222-2222-4222-8222-222222222222.jsonl", otherWorkingDir, false)
	newer := time.Now().Add(time.Second)
	if err := os.Chtimes(other, newer, newer); err != nil {
		t.Fatalf("set other rollout mtime: %v", err)
	}

	tracker := newCodexTurnCompletionTracker(turnStart, wantedWorkingDir)
	if !tracker.completed() {
		t.Fatal("matching rollout task_complete event was not detected")
	}
	if tracker.rolloutPath != wanted {
		t.Fatalf("rolloutPath = %q, want matching %q", tracker.rolloutPath, wanted)
	}
}

func TestWaitForCodexInteractiveResponseUsesTaskCompleteWhenFooterIsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	workingDir := filepath.Join(t.TempDir(), "mlp-cli-session")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatalf("mkdir working dir: %v", err)
	}
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "16")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	turnStart := time.Now().UTC().Add(-time.Second)
	rollout := filepath.Join(dayDir, "rollout-2026-07-16T12-00-00-33333333-3333-4333-8333-333333333333.jsonl")
	transcript := fmt.Sprintf("%s\n%s\n",
		fmt.Sprintf(`{"type":"session_meta","payload":{"cwd":%q}}`, workingDir),
		fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"Wrote the aggregate.\\n\\nSTATUS: COMPLETED"}}`, time.Now().UTC().Format(time.RFC3339Nano)),
	)
	if err := os.WriteFile(rollout, []byte(transcript), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	fakeBin := t.TempDir()
	tmuxPath := filepath.Join(fakeBin, "tmux")
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' \
    '• Working (48s • esc to interrupt)' \
    '• Called api-bridge.execute_shell_command({"command":"verify"})' \
    '  command completed successfully' \
    '────────────────────────────────────────────────────────────────' \
    '• Wrote the aggregate.' \
    '  STATUS: COMPLETED' \
    '────────────────────────────────────────────────────────────────' \
    '› Find and fix a bug in @filename' \
    '  gpt-5.6-terra medium · /tmp/workspace'
fi
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	pane := `
• Working (48s • esc to interrupt)
• Wrote the aggregate.
  STATUS: COMPLETED
────────────────────────────────────────────────────────────────
› Find and fix a bug in @filename
  gpt-5.6-terra medium · /tmp/workspace
`
	if !hasCodexActivity(pane) {
		t.Fatal("fixture must reproduce stale Working activity without a Worked-for footer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	captured, err := waitForCodexInteractiveResponse(ctx, "missing-footer-session", "Codex ready\n›", nil, turnStart, workingDir)
	if err != nil {
		t.Fatalf("native task_complete event did not release response wait: %v", err)
	}
	if !strings.Contains(captured, "STATUS: COMPLETED") {
		t.Fatalf("final response capture was lost: %q", captured)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("task_complete detection was unexpectedly slow: %v", elapsed)
	}
}

func TestCodexTurnCompletionTrackerIgnoresPriorTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	workingDir := filepath.Join(t.TempDir(), "persistent-session")
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "16")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	turnStart := time.Now().UTC()
	rollout := filepath.Join(dayDir, "rollout-2026-07-16T12-00-00-44444444-4444-4444-8444-444444444444.jsonl")
	transcript := fmt.Sprintf("%s\n%s\n",
		fmt.Sprintf(`{"type":"session_meta","payload":{"cwd":%q}}`, workingDir),
		fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_complete"}}`, turnStart.Add(-time.Second).Format(time.RFC3339Nano)),
	)
	if err := os.WriteFile(rollout, []byte(transcript), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	tracker := newCodexTurnCompletionTracker(turnStart, workingDir)
	if tracker.completed() {
		t.Fatal("task_complete from a prior persistent-session turn was accepted")
	}
}

func TestWaitForCodexInteractiveResponseFiveMinuteFallbackRequiresIdleComposer(t *testing.T) {
	fakeBin := t.TempDir()
	tmuxPath := filepath.Join(fakeBin, "tmux")
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' \
    '• Working (48s • esc to interrupt)' \
    '• Wrote the aggregate.' \
    '  STATUS: COMPLETED' \
    '────────────────────────────────────────────────────────────────' \
    '› Find and fix a bug in @filename' \
    '  gpt-5.6-terra medium · /tmp/workspace'
fi
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvCodexInteractiveStalePaneBackstopSeconds, "1")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	_, err := waitForCodexInteractiveResponse(ctx, "stable-idle-composer", "Codex ready\n›", nil, time.Time{}, "")
	if err != nil {
		t.Fatalf("stable idle-composer fallback did not release response wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed < time.Second || elapsed >= 2500*time.Millisecond {
		t.Fatalf("fallback elapsed = %v, want approximately configured 1s", elapsed)
	}
}

func TestWaitForCodexInteractiveResponseFallbackRejectsIdleComposerWithoutCompletedMarker(t *testing.T) {
	fakeBin := t.TempDir()
	tmuxPath := filepath.Join(fakeBin, "tmux")
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' \
	'• Working (48s • esc to interrupt)' \
	'• The agent produced an intermediate summary but has not declared completion.' \
	'────────────────────────────────────────────────────────────────' \
	'› Find and fix a bug in @filename' \
	'  gpt-5.6-terra medium · /tmp/workspace'
fi
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvCodexInteractiveStalePaneBackstopSeconds, "1")

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := waitForCodexInteractiveResponse(ctx, "stable-composer-no-completed-marker", "Codex ready\n›", nil, time.Time{}, "")
	if err == nil {
		t.Fatal("stable idle composer without STATUS: COMPLETED was incorrectly accepted")
	}
	if elapsed := time.Since(started); elapsed < 1300*time.Millisecond {
		t.Fatalf("stable idle composer without STATUS: COMPLETED returned early after %v: %v", elapsed, err)
	}
}
