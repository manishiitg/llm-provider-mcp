package musecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// fakeMuseHost answers like `muse serve` over in-memory pipes. Its first turn
// ends right after the steer arrives; with rejectSteer the steer's rejection
// row reaches the journal only after turn/completed, as seen live.
type fakeMuseHost struct {
	t           *testing.T
	journal     string
	rejectSteer bool
	in          *bufio.Scanner
	out         io.Writer
	mu          sync.Mutex
	starts      []string
}

func (h *fakeMuseHost) send(v any) {
	raw, _ := json.Marshal(v)
	if _, err := h.out.Write(append(raw, '\n')); err != nil {
		h.t.Errorf("host write: %v", err)
	}
}

func (h *fakeMuseHost) endTurn(turnID, reply string) {
	h.send(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"itemId": "item-" + turnID, "kind": "agentMessage", "turnId": turnID, "text": reply}}})
	h.send(map[string]any{"method": "turn/completed", "params": map[string]any{"turnId": turnID, "terminal": "completed"}})
}

func (h *fakeMuseHost) serve() {
	for h.in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				CommandID string `json:"commandId"`
				Input     []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if json.Unmarshal(h.in.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "turn/start":
			h.mu.Lock()
			h.starts = append(h.starts, req.Params.Input[0].Text)
			n := len(h.starts)
			h.mu.Unlock()
			turnID := fmt.Sprintf("turn-%d", n)
			h.send(map[string]any{"method": "turn/started", "params": map[string]any{"turnId": turnID}})
			h.send(map[string]any{"id": req.ID, "result": map[string]any{"turnId": turnID}})
			if n > 1 {
				h.endTurn(turnID, "answer to the steer")
			}
		case "turn/steer":
			h.send(map[string]any{"id": req.ID, "result": map[string]any{}})
			h.endTurn("turn-1", "first answer")
			if h.rejectSteer {
				time.Sleep(100 * time.Millisecond)
				row := fmt.Sprintf(`{"sequence":2,"payload_type":"turn_submit.rejected","payload":{"kind":"turn_submit_rejected","record":{"command_id":%q,"reason":"already_terminal"}}}`+"\n", req.Params.CommandID)
				f, err := os.OpenFile(h.journal, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					h.t.Errorf("journal: %v", err)
					continue
				}
				_, _ = f.WriteString(row)
				_ = f.Close()
			}
		}
	}
}

// Seen live, 6 Nov: Muse answered turn/steer, ended the turn without reading the
// message and journalled turn_submit.rejected already_terminal, so the person's
// message showed as delivered and no turn ran it. A rejected steer must run as
// exactly one next turn; a steer the turn read must not run again.
func TestMuseMSPSteerRejectedAtTurnEndRunsOnceAsNextTurn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reject     bool
		wantStarts []string
		wantAnswer string
	}{
		{"rejected after the turn ended", true, []string{"first prompt", "late message"}, "first answer\n\nanswer to the steer"},
		{"read by the turn", false, []string{"first prompt"}, "first answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wait := museMSPSteerVerdictWait
			museMSPSteerVerdictWait = 500 * time.Millisecond
			t.Cleanup(func() { museMSPSteerVerdictWait = wait })

			dataHome := t.TempDir()
			dir := filepath.Join(dataHome, "muse", "sessions", "2026", "11", "06", "native-1")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(dir, "session.jsonl")
			if err := os.WriteFile(journal, []byte(`{"sequence":1,"payload_type":"runtime.session","payload":{}}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			toHost, hostIn := io.Pipe()
			fromHost, hostOut := io.Pipe()
			host := &fakeMuseHost{t: t, journal: journal, rejectSteer: tc.reject, in: bufio.NewScanner(toHost), out: hostOut}
			go host.serve()

			rpc := &museRPC{stdin: hostIn, pending: map[string]chan museRPCMessage{}, events: make(chan museRPCMessage, 64), done: make(chan struct{}), exited: make(chan struct{}), stop: func() {}}
			go rpc.read(fromHost)
			t.Cleanup(func() { rpc.finish(fmt.Errorf("test over")); _ = hostOut.Close() })

			owner := "chat-" + strings.ReplaceAll(tc.name, " ", "-")
			s := &museMSPSession{owner: owner, sessionID: "native-1", dataHome: dataHome, rpc: rpc}
			museMSPSessions.Lock()
			museMSPSessions.m[owner] = s
			museMSPSessions.Unlock()
			t.Cleanup(func() {
				museMSPSessions.Lock()
				delete(museMSPSessions.m, owner)
				museMSPSessions.Unlock()
			})
			a := &MuseCLIAdapter{}
			go a.pumpMuseMSP(s)

			steerErr := make(chan error, 1)
			go func() {
				for {
					s.mu.Lock()
					running := s.turnID != ""
					s.mu.Unlock()
					if running {
						steerErr <- SendMuseMSPInput(context.Background(), owner, "late message")
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			params := map[string]any{"commandId": museCommandID(), "sessionId": "native-1", "input": []map[string]any{{"type": "text", "text": "first prompt"}}}
			resp, err := a.runMuseMSPTurns(ctx, s, owner, params, false, &llmtypes.CallOptions{}, func(llmtypes.StreamChunk) {})
			if err != nil {
				t.Fatalf("turns: %v", err)
			}
			if err := <-steerErr; err != nil {
				t.Fatalf("steer: %v", err)
			}
			host.mu.Lock()
			starts := append([]string(nil), host.starts...)
			host.mu.Unlock()
			if strings.Join(starts, "|") != strings.Join(tc.wantStarts, "|") {
				t.Fatalf("turns started = %q, want %q", starts, tc.wantStarts)
			}
			s.mu.Lock()
			pendingWakes := len(s.wake)
			s.mu.Unlock()
			if pendingWakes != 0 {
				t.Fatalf("foreground start became wake: %d", pendingWakes)
			}
			if got := resp.Choices[0].Content; got != tc.wantAnswer {
				t.Fatalf("answer = %q, want %q", got, tc.wantAnswer)
			}
		})
	}
}
