package agycli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxexec"
)

func TestAgyInteractiveStreamTmuxScreenFlag(t *testing.T) {
	t.Setenv(EnvAgyInteractiveStreamTmuxScreen, "")
	if !agyInteractiveStreamTmuxScreenEnabled() {
		t.Fatal("tmux screen streaming should be enabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(EnvAgyInteractiveStreamTmuxScreen, value)
		if !agyInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be enabled for %q", value)
		}
	}

	for _, value := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Setenv(EnvAgyInteractiveStreamTmuxScreen, value)
		if agyInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be disabled for %q", value)
		}
	}
}

func TestAgyTerminalStreamCapturesRawScreenRows(t *testing.T) {
	fakeBin := t.TempDir()
	argsPath := fakeBin + "/capture-args.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' "$*" > "$TMUX_TEST_CAPTURE_ARGS"
  printf 'screen row one\nscreen row two\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_CAPTURE_ARGS", argsPath)

	stream := make(chan llmtypes.StreamChunk, 1)
	var last string
	if !streamAgyTerminalSnapshot(context.Background(), "raw-display-session", stream, &last) {
		t.Fatal("streamAgyTerminalSnapshot returned false")
	}
	chunk := <-stream
	if chunk.Type != llmtypes.StreamChunkTypeTerminal {
		t.Fatalf("chunk type = %q, want terminal", chunk.Type)
	}
	if !strings.Contains(chunk.Content, "screen row one\nscreen row two") {
		t.Fatalf("chunk content = %q, want raw screen rows", chunk.Content)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read capture args: %v", err)
	}
	if !strings.Contains(string(args), " -J") {
		t.Fatalf("terminal display capture did not use joined rows (-J): %q", string(args))
	}
	if want := fmt.Sprintf(" -S -%d", tmuxexec.DefaultScrollbackLines); !strings.Contains(string(args), want) {
		t.Fatalf("terminal display capture did not request %s: %q", want, string(args))
	}
}

func TestAgyStartSessionSetsHistoryLimit(t *testing.T) {
	fakeBin := t.TempDir()
	argsPath := fakeBin + "/tmux-args.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_ARGS"
exit 0
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_ARGS", argsPath)

	if err := startAgyTmuxSession(context.Background(), "history-session", []string{"agy"}, nil, t.TempDir()); err != nil {
		t.Fatalf("startAgyTmuxSession returned error: %v", err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read tmux args: %v", err)
	}
	want := "set-option -t history-session history-limit " + tmuxexec.DefaultHistoryLimit
	if !strings.Contains(string(args), want) {
		t.Fatalf("tmux args missing history limit %q:\n%s", want, string(args))
	}
}

func TestAgyResetPaneForTurnPreservesScrollback(t *testing.T) {
	fakeBin := t.TempDir()
	argsPath := fakeBin + "/tmux-args.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_ARGS"
exit 0
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_ARGS", argsPath)

	resetAgyPaneForTurn(context.Background(), "history-session")
	args, err := os.ReadFile(argsPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read tmux args: %v", err)
	}
	if strings.Contains(string(args), "clear-history") {
		t.Fatalf("resetAgyPaneForTurn should preserve tmux history, got args:\n%s", string(args))
	}
}
