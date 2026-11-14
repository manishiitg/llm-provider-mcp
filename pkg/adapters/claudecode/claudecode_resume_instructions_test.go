package claudecode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-856-442: a failed changed-instruction resume must resend on retry, and
// an agent-controlled marker link cannot redirect an application-side write.
func TestClaudeResumeInstructionsRetryAndMarkerLinks(t *testing.T) {
	root := t.TempDir()
	opts := &llmtypes.CallOptions{CLIRuntimeDir: root}
	_, remember := claudeCurrentResumeInstructions(opts, root, "native-id", "source rules", "hello", false)
	remember()
	prompt, remember := claudeCurrentResumeInstructions(opts, root, "native-id", "destination rules", "next", true)
	if !strings.Contains(prompt, "destination rules") {
		t.Fatal("new instructions missing")
	}
	// Failed turn: do not call remember.
	retry, done := claudeCurrentResumeInstructions(opts, root, "native-id", "destination rules", "next", true)
	if !strings.Contains(retry, "destination rules") {
		t.Fatal("failed turn consumed refresh")
	}
	done()
	stable, _ := claudeCurrentResumeInstructions(opts, root, "native-id", "destination rules", "next", true)
	if stable != "next" {
		t.Fatal("unchanged instructions repeated")
	}
	outside := filepath.Join(t.TempDir(), "keep.txt")
	if e := os.WriteFile(outside, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	markers, _ := filepath.Glob(filepath.Join(root, ".claude", "*.sha256"))
	if len(markers) != 1 {
		t.Fatal("missing marker")
	}
	if e := os.Remove(markers[0]); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, markers[0]); e != nil {
		t.Fatal(e)
	}
	_, remember = claudeCurrentResumeInstructions(opts, root, "native-id", "new rules", "next", true)
	remember()
	forgetClaudeResumeInstructions(opts, root, "native-id")
	raw, e := os.ReadFile(outside)
	if e != nil || string(raw) != "keep" {
		t.Fatal("instruction marker followed a link")
	}
}

// PLAT-746: a native compact could remove the one-time refreshed rules while
// their digest still suppressed redelivery. Stub only the CLI; exercise the
// actual stream consumer, successful-turn defer, and next-turn prompt builder.
func TestClaudeCompactionRestatesCurrentInstructionsNextTurn(t *testing.T) {
	for _, scenario := range []string{"auto", "idle", "idle_warm", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			opts := &llmtypes.CallOptions{CLIRuntimeDir: root, Metadata: &llmtypes.Metadata{Custom: map[string]interface{}{MetadataKeyWorkingDir: root, MetadataKeyResumeSessionID: "native-id"}}}
			_, remember := claudeCurrentResumeInstructions(opts, root, "native-id", "current destination rules", "hello", true)
			remember()
			output := `{"type":"system","subtype":"compact_boundary","session_id":"native-id","compact_metadata":{"trigger":"auto","pre_tokens":50000,"post_tokens":10000}}`
			if scenario == "failed" {
				output = `{"type":"system","subtype":"status","status":"compacting","session_id":"native-id"}` + "\n" + `{"type":"system","subtype":"status","status":null,"compact_result":"failed","session_id":"native-id"}`
			}
			if scenario == "idle" || scenario == "idle_warm" {
				llmtypes.WithCodingProviderIdleCompact()(opts)
			}
			if scenario == "idle_warm" {
				owner := "compacting-warm-chat"
				WithInteractiveSessionID(owner)(opts)
				life, kill := context.WithCancel(context.Background())
				cmd := exec.CommandContext(life, "sh", "-c", `while read line; do case "$line" in *stop_background*) exit 0;; esac; done`)
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				s, err := startClaudeSDKSession(owner, nil, cmd, stdin, stdout, &claudeLockedBuffer{}, kill, func() {}, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { kill(); <-s.exited })
				s.warm = true
				s.park()
			}
			output += "\n" + `{"type":"result","is_error":false,"session_id":"native-id","result":"done"}` + "\n"
			script := "#!/bin/sh\ncat <<'RESULT'\n" + output + "RESULT\n"
			if err := os.WriteFile(filepath.Join(root, "claude"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			adapter := NewClaudeCodeInteractiveAdapter("example-model", &MockLogger{})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := adapter.generateContentStructured(ctx, []llmtypes.MessageContent{
				llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "current destination rules"),
				llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "continue"),
			}, opts)
			if err != nil {
				t.Fatal(err)
			}
			next, _ := claudeCurrentResumeInstructions(opts, root, "native-id", "current destination rules", "next task", true)
			if got, want := strings.Contains(next, "current destination rules"), scenario != "failed"; got != want {
				t.Fatalf("current instructions delivered after %s=%t, want %t", scenario, got, want)
			}
		})
	}
}
