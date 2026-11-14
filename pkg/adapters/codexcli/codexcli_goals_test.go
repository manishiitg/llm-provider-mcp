package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type goalRPCWriter struct {
	rpc      *codexRPC
	requests []codexRPCMessage
	reply    func(codexRPCMessage) codexRPCMessage
}

func (w *goalRPCWriter) Write(raw []byte) (int, error) {
	var request codexRPCMessage
	if err := json.Unmarshal(raw, &request); err != nil {
		return 0, err
	}
	w.requests = append(w.requests, request)
	response := w.reply(request)
	w.rpc.mu.Lock()
	ch := w.rpc.pending[string(request.ID)]
	w.rpc.mu.Unlock()
	if ch != nil {
		ch <- response
	}
	return len(raw), nil
}
func (*goalRPCWriter) Close() error { return nil }

var _ io.WriteCloser = (*goalRPCWriter)(nil)

func goalFixtureRPC(reply func(codexRPCMessage) codexRPCMessage) (*codexRPC, *goalRPCWriter) {
	exited := make(chan struct{})
	close(exited)
	rpc := &codexRPC{events: make(chan codexRPCMessage, 32), done: make(chan struct{}), exited: exited, stop: func() {}, pending: map[string]chan codexRPCMessage{}}
	writer := &goalRPCWriter{rpc: rpc, reply: reply}
	rpc.stdin = writer
	return rpc, writer
}
func goalWire(status string) json.RawMessage {
	return json.RawMessage(`{"goal":{"objective":"Finish the task","status":"` + status + `","tokensUsed":42,"tokenBudget":1000,"timeUsedSeconds":12}}`)
}

// PLAT-746: ordinary turn completion must not end a native goal, and the
// host must never manufacture the next turn. Only the owned thread updates it.
func TestCodexGoalRunsNativeContinuationUntilTerminalState(t *testing.T) {
	for _, status := range []string{"complete", "blocked", "budgetLimited", "usageLimited", "cleared"} {
		t.Run(status, func(t *testing.T) {
			rpc, wire := goalFixtureRPC(func(codexRPCMessage) codexRPCMessage { return codexRPCMessage{} })
			session := &codexAppSession{owner: "chat", threadID: "thread", turnID: "first", goal: &llmtypes.NativeGoal{Objective: "Finish the task", Status: "active"}, goalActivationPending: true}
			send := func(method, body string) {
				rpc.events <- codexRPCMessage{Method: method, Params: json.RawMessage(body)}
			}
			// Ignore the transient old clear/paused acknowledgements during activation.
			send("thread/goal/cleared", `{"threadId":"thread"}`)
			send("thread/goal/updated", `{"threadId":"thread","goal":{"objective":"Finish the task","status":"paused"}}`)
			send("thread/goal/updated", `{"threadId":"another-thread","goal":{"objective":"private other task","status":"complete"}}`)
			send("item/completed", `{"threadId":"thread","turnId":"first","item":{"id":"a","type":"agentMessage","text":"first step"}}`)
			send("turn/completed", `{"threadId":"thread","turn":{"id":"first","status":"completed"}}`)
			send("thread/goal/updated", `{"threadId":"thread","goal":{"objective":"Finish the task","status":"active"}}`)
			if status == "complete" {
				send("turn/started", `{"threadId":"thread","turn":{"id":"second","status":"inProgress"}}`)
				send("item/completed", `{"threadId":"thread","turnId":"second","item":{"id":"b","type":"agentMessage","text":"task finished"}}`)
			}
			if status == "cleared" {
				send("thread/goal/cleared", `{"threadId":"thread"}`)
			} else {
				send("thread/goal/updated", `{"threadId":"thread","goal":{"objective":"Finish the task","status":"`+status+`","tokensUsed":42,"tokenBudget":1000,"timeUsedSeconds":12}}`)
			}
			if status == "complete" {
				send("turn/completed", `{"threadId":"thread","turn":{"id":"second","status":"completed"}}`)
			}
			updates := []llmtypes.NativeGoalUpdate{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			response, err := (&CodexCLIAdapter{}).consumeAppTurn(ctx, session, rpc, "thread", "first", func(chunk llmtypes.StreamChunk) {
				if chunk.NativeGoal != nil {
					updates = append(updates, *chunk.NativeGoal)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(wire.requests) != 0 {
				t.Fatal("host started a replacement goal turn")
			}
			if status == "complete" && !strings.Contains(response.Choices[0].Content, "task finished") {
				t.Fatal("native continuation was lost at first completion")
			}
			if len(updates) == 0 {
				t.Fatal("goal updates did not reach the host")
			}
			for _, update := range updates {
				if update.NativeSessionID != "thread" || (update.Goal != nil && update.Goal.Objective != "Finish the task") {
					t.Fatal("another thread's goal leaked")
				}
			}
			last := updates[len(updates)-1]
			if status == "cleared" {
				if last.Goal != nil {
					t.Fatal("clear did not remove goal")
				}
			} else {
				if last.Goal == nil || last.Goal.TokensUsed == nil || *last.Goal.TokensUsed != 42 || last.Goal.TokenBudget == nil || *last.Goal.TokenBudget != 1000 {
					t.Fatal("native goal counters lost")
				}
			}
		})
	}
}

// Invalid objectives leave the native goal untouched. Unsupported native
// methods are different from a transient failure; neither starts model work.
func TestCodexGoalControlDoesNotFallbackToAnOrdinaryPrompt(t *testing.T) {
	for _, scenario := range []string{"too_long", "show", "unsupported", "read_failed"} {
		t.Run(scenario, func(t *testing.T) {
			rpc, wire := goalFixtureRPC(func(request codexRPCMessage) codexRPCMessage {
				switch scenario {
				case "unsupported":
					return codexRPCMessage{Error: &codexRPCError{Code: -32601, Message: "Method not found"}}
				case "read_failed":
					return codexRPCMessage{Error: &codexRPCError{Code: -32000, Message: "temporarily unavailable"}}
				default:
					return codexRPCMessage{Result: goalWire("paused")}
				}
			})
			session := &codexAppSession{owner: "chat", threadID: "thread"}
			command := llmtypes.NativeGoalCommand{Action: "show"}
			if scenario == "too_long" {
				command = llmtypes.NativeGoalCommand{Action: "set", Objective: strings.Repeat("x", 4001)}
			}
			_, activate, _, err := (&CodexCLIAdapter{}).prepareCodexGoalCommand(context.Background(), session, rpc, "thread", command, func(llmtypes.StreamChunk) {})
			if activate {
				t.Fatal("control started model work")
			}
			var unavailable *llmtypes.NativeGoalUnavailableError
			if scenario == "unsupported" && !errors.As(err, &unavailable) {
				t.Fatal("unsupported native goal was not reported plainly")
			}
			if scenario == "read_failed" && (err == nil || errors.As(err, &unavailable)) {
				t.Fatal("transient read was mislabeled unsupported")
			}
			if scenario == "too_long" && (err == nil || len(wire.requests) != 0) {
				t.Fatal("invalid objective touched native goal")
			}
			if scenario == "show" && err != nil {
				t.Fatal(err)
			}
			for _, request := range wire.requests {
				if request.Method == "turn/start" || request.Method == "thread/goal/clear" {
					t.Fatal("read/invalid goal control changed work")
				}
			}
		})
	}
}

// Stop and missing native continuation pause the native goal rather than
// creating an AgentWorks continuation loop or abandoning an active goal.
func TestCodexGoalStopAndNativeContinuationGapPauseTheOwnedThread(t *testing.T) {
	previous := codexGoalFollowupWait
	codexGoalFollowupWait = 20 * time.Millisecond
	defer func() { codexGoalFollowupWait = previous }()
	for _, scenario := range []string{"native_gap", "stop"} {
		t.Run(scenario, func(t *testing.T) {
			rpc, wire := goalFixtureRPC(func(request codexRPCMessage) codexRPCMessage {
				if request.Method == "thread/goal/set" {
					return codexRPCMessage{Result: goalWire("paused")}
				}
				return codexRPCMessage{Result: json.RawMessage(`{}`)}
			})
			session := &codexAppSession{owner: "chat", threadID: "thread", goal: &llmtypes.NativeGoal{Objective: "Finish the task", Status: "active"}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if scenario == "stop" {
				cancel()
			} else {
				rpc.events <- codexRPCMessage{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)}
			}
			_, err := (&CodexCLIAdapter{}).consumeAppTurn(ctx, session, rpc, "thread", "turn", func(llmtypes.StreamChunk) {})
			if scenario == "native_gap" && err != nil {
				t.Fatal(err)
			}
			if scenario == "stop" && !errors.Is(err, context.Canceled) {
				t.Fatal("Stop did not cancel the owned turn")
			}
			if session.goal == nil || session.goal.Status != "paused" {
				t.Fatal("native goal still active")
			}
			paused := false
			for _, request := range wire.requests {
				var params struct {
					ThreadID string `json:"threadId"`
					Status   string `json:"status"`
				}
				if json.Unmarshal(request.Params, &params) != nil {
					t.Fatal("invalid native params")
				}
				if params.ThreadID != "thread" {
					t.Fatal("control reached another thread")
				}
				if request.Method == "thread/goal/set" && params.Status == "paused" {
					paused = true
				}
				if request.Method == "turn/start" {
					t.Fatal("host invented a goal continuation")
				}
			}
			if !paused {
				t.Fatal("native pause command missing")
			}
		})
	}
}
