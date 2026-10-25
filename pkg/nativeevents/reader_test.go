package nativeevents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are format/identity regressions. Passing replay is deliberately not
// labelled as acceptance of an installed, signed-in coding provider.
func TestNativeRecordConformance(t *testing.T) {
	cases := []struct {
		provider, rows string
		want           []string
	}{
		{"claude-code", `{"type":"user","uuid":"u1","sessionId":"session-test","origin":{"kind":"human"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":[{"type":"tool_use","id":"tool1","name":"Agent"},{"type":"thinking","thinking":"PRIVATE_THOUGHT"}]}}
{"type":"user","uuid":"result1","parentUuid":"a1","toolUseResult":{"agentId":"task1"},"message":{"content":[{"type":"tool_result","tool_use_id":"tool1"}]}}
{"type":"user","uuid":"notice1","parentUuid":"result1","turnOrigin":"task_notification","origin":{"kind":"task-notification"},"message":{"content":"<task-notification><task-id>task1</task-id><tool-use-id>tool1</tool-use-id><status>completed</status></task-notification>"}}
{"type":"assistant","uuid":"a2","parentUuid":"notice1","message":{"content":[{"type":"text","text":"The helper finished."}]}}
`, []string{"turn:started", "tool:started", "helper:started", "helper:finished", "turn:started", "reply:committed"}},
		{"codex-cli", `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"message1","text":"Done"}}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"code":"capacity"}}}
`, []string{"turn:started", "reply:committed", "turn:failed"}},
		{"pi-cli", `{"type":"session","id":"session-test"}
{"type":"message","id":"p1","message":{"role":"user","content":[{"type":"text","text":"Question"}]}}
{"type":"message","id":"p2","message":{"role":"assistant","content":[{"type":"thinking","thinking":"PRIVATE_THOUGHT"},{"type":"text","text":"Answer"}]}}
`, []string{"reply:committed"}},
		{"muse-cli", `{"sequence":1,"transaction_id":"tx1","payload":{"kind":"run","run_id":"r1","event":{"kind":"assistant_message_committed","message_id":"m1","text":"Answer"}}}
`, []string{"reply:committed"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(p, []byte(tc.rows), 0600); err != nil {
				t.Fatal(err)
			}
			b, err := Read(context.Background(), tc.provider, "session-test", p, Cursor{})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range b.Records {
				got = append(got, r.Kind+":"+r.State)
				if strings.Contains(r.Text, "PRIVATE_THOUGHT") {
					t.Fatal("reasoning escaped")
				}
				if r.ID == "" {
					t.Fatal("missing identity")
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v want %v gaps=%d unknown=%v", got, tc.want, b.Gaps, b.Unknown)
			}
			again, err := Read(context.Background(), tc.provider, "session-test", p, b.Next)
			if err != nil {
				t.Fatal(err)
			}
			if len(again.Records) != 0 {
				t.Fatal("replayed committed records")
			}
		})
	}
}

func TestNativeCursorPartialAndRebind(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	first := `{"type":"message","id":"one","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(first+`{"type":"message"`), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := Read(context.Background(), "pi-cli", "selected-session", p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Next.Offset != int64(len(first)) {
		t.Fatal("partial record was consumed")
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", len(first)+100)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), "pi-cli", "selected-session", p, b.Next); err == nil {
		t.Fatal("rewritten source was silently accepted")
	}
}
