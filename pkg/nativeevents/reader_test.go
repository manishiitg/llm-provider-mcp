package nativeevents

import (
	"context"
	"encoding/json"
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
		// PLAT-746-764: an async dispatch acknowledgement and turn completion
		// must not settle or discard a still-unanswered question.
		{"codex-cli", `{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-question"}}
{"type":"response_item","payload":{"type":"function_call","name":"request_user_input_async","call_id":"call-question","arguments":"{\"questions\":[{\"title\":\"Which folder?\",\"options\":[\"Existing\",\"New\"]}]}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"call-question","output":"{\"accepted\":true}"}}
{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-question"}}
`, []string{"turn:started", "question:requested", "turn:completed"}},
		{"claude-code", `{"type":"user","uuid":"u1","sessionId":"session-test","origin":{"kind":"human"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":[{"type":"tool_use","id":"tool1","name":"Agent"},{"type":"thinking","thinking":"PRIVATE_THOUGHT"}]}}
{"type":"user","uuid":"result1","parentUuid":"a1","toolUseResult":{"agentId":"task1","isAsync":true},"message":{"content":[{"type":"tool_result","tool_use_id":"tool1"}]}}
{"type":"user","uuid":"notice1","parentUuid":"result1","turnOrigin":"task_notification","origin":{"kind":"task-notification"},"message":{"content":"<task-notification><task-id>task1</task-id><tool-use-id>tool1</tool-use-id><status>completed</status></task-notification>"}}
{"type":"assistant","uuid":"a2","parentUuid":"notice1","message":{"id":"msg-helper","content":[{"type":"thinking","thinking":"PRIVATE_THOUGHT"},{"type":"text","text":"The helper finished."}]}}
`, []string{"turn:started", "tool:started", "helper:started", "helper:finished", "turn:started", "reply:committed"}},
		{"claude-code", `{"type":"user","uuid":"u1","origin":{"kind":"human"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":[{"type":"tool_use","id":"tool1","name":"Agent"}]}}
{"type":"user","uuid":"result1","parentUuid":"a1","toolUseResult":{"agentId":"task1","isAsync":false},"message":{"content":[{"type":"tool_result","tool_use_id":"tool1"}]}}
`, []string{"turn:started", "tool:started", "tool:finished"}},
		{"claude-code", `{"type":"user","uuid":"u1","origin":{"kind":"human"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"content":[{"type":"tool_use","id":"bash1","name":"Bash","input":{"run_in_background":false}},{"type":"tool_use","id":"todo1","name":"TaskUpdate"}]}}
{"type":"user","uuid":"result1","parentUuid":"a1","toolUseResult":{"backgroundTaskId":"command1","timedOutAfterMs":1000},"message":{"content":[{"type":"tool_result","tool_use_id":"bash1"}]}}
{"type":"user","uuid":"todo-result","parentUuid":"result1","toolUseResult":{"taskId":"todo1"},"message":{"content":[{"type":"tool_result","tool_use_id":"todo1"}]}}
{"type":"user","uuid":"notice","parentUuid":"todo-result","queueTranscriptOnly":true,"origin":{"kind":"task-notification"},"message":{"content":"<task-notification><task-id>command1</task-id><status>stopped</status></task-notification>"}}
`, []string{"turn:started", "tool:started", "tool:started", "background_command:started", "tool:finished", "background_command:stopped"}},
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
		{"muse-cli", `{"sequence":2,"payload":{"kind":"task","run_id":"r1","event":{"kind":"status","task_id":"task1","details":{"phase":"opening_stream","facets":[{"kind":"external_attempt","attempt":1}]}}}}
{"sequence":3,"payload":{"kind":"task","run_id":"r1","event":{"kind":"status","task_id":"task1","details":{"phase":"opening_stream","facets":[{"kind":"external_attempt","attempt":2}]}}}}
{"sequence":4,"payload":{"kind":"task","run_id":"r1","event":{"kind":"status","task_id":"task1","details":{"phase":"stream_succeeded","facets":[{"kind":"external_attempt","attempt":2}]}}}}
{"sequence":5,"payload":{"kind":"run","run_id":"r1","event":{"kind":"terminal","terminal":"completed","turn_duration_ms":500}}}
`, []string{"status:opening_stream", "retry:retrying", "status:stream_succeeded", "turn:completed"}},
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
				// PLAT-746: the synthetic helper follow-up's committed reply is
				// also streamed as a native block. Preserve that exact identity.
				if r.Provider == "claude-code" && r.Origin == "helper_finished" && r.Kind == "reply" && r.AssistantBlockID != "claude:msg-helper:1" {
					t.Fatalf("lost native helper reply block identity: %+v", r)
				}
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

// PLAT-746-899: a Codex command that finished after its turn ended was closed
// from the rollout with an empty card, because the decoder dropped its output.
func TestCodexCommandCompletionCarriesItsOutput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	rows := `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-1","exit_code":0,"aggregated_output":"VERIFY: PASSED\n"}}}
`
	if err := os.WriteFile(p, []byte(rows), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := Read(context.Background(), "codex-cli", "session-test", p, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Records) != 1 || b.Records[0].Name != "Command" || b.Records[0].Output != "VERIFY: PASSED\n" {
		t.Fatalf("records=%+v", b.Records)
	}
	if data, _ := json.Marshal(b.Records[0]); strings.Contains(string(data), "VERIFY") {
		t.Fatalf("command output must not be serialized: %s", data)
	}
}

// PLAT-746-941: a task id alone is not an intentional or timed-out background shell.
func TestClaudeBackgroundShellClassification(t *testing.T) {
	for _, tc := range []struct{ name, input, result, mode string }{
		{"ordinary", `{"command":"gh pr list","run_in_background":false}`, `{"backgroundTaskId":"bg"}`, ""},
		{"explicit", `{"command":"TOKEN=private deploy.sh verify\nignored","run_in_background":true}`, `{"backgroundTaskId":"bg"}`, "explicit"},
		{"automatic", `{"command":"deploy.sh verify"}`, `{"backgroundTaskId":"bg","timedOutAfterMs":1000}`, "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "session.jsonl")
			first := `{"type":"user","uuid":"u","origin":{"kind":"human"}}` + "\n" + `{"type":"assistant","uuid":"a","parentUuid":"u","message":{"content":[{"type":"tool_use","id":"bash","name":"Bash","input":` + tc.input + `}]}}` + "\n"
			if err := os.WriteFile(p, []byte(first), 0600); err != nil {
				t.Fatal(err)
			}
			initial, err := Read(context.Background(), "claude-code", "session-test", p, Cursor{})
			if err != nil {
				t.Fatal(err)
			}
			// Real observers persist their cursor between the tool start and result.
			raw, err := json.Marshal(initial.Next)
			if err != nil {
				t.Fatal(err)
			}
			var cursor Cursor
			if err := json.Unmarshal(raw, &cursor); err != nil {
				t.Fatal(err)
			}
			rest := `{"type":"user","uuid":"r","parentUuid":"a","toolUseResult":` + tc.result + `,"message":{"content":[{"type":"tool_result","tool_use_id":"bash"}]}}` + "\n" + `{"type":"user","uuid":"n","parentUuid":"r","origin":{"kind":"task-notification"},"message":{"content":"<task-notification><task-id>bg</task-id><tool-use-id>bash</tool-use-id><status>completed</status></task-notification>"}}` + "\n"
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString(rest)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			batch, err := Read(context.Background(), "claude-code", "session-test", p, cursor)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, r := range batch.Records {
				if r.Kind != "background_command" {
					continue
				}
				count++
				if r.BackgroundMode != tc.mode || r.CommandLabel != "deploy.sh verify" {
					t.Fatalf("incorrect shell label/classification: %+v", r)
				}
			}
			want := 2
			if tc.mode == "" {
				want = 0
			}
			if count != want {
				t.Fatalf("background rows=%d want %d", count, want)
			}
		})
	}
}
