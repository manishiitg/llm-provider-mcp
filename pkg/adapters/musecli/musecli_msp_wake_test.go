package musecli

import (
	"encoding/json"
	"testing"
)

// A Monitor that fires between the chat's turns makes Muse run a turn of its
// own; its reply must reach the chat with the Monitor that woke it, not only
// the log.
func TestMuseMSPWakeTurnReplyReachesTheHost(t *testing.T) {
	var got []MuseMSPReply
	SetMuseMSPReplyHandler(func(reply MuseMSPReply) { got = append(got, reply) })
	t.Cleanup(func() { SetMuseMSPReplyHandler(nil) })
	s := &museMSPSession{owner: "chat-1", sessionID: "native-1"}
	event := func(method string, params any) museRPCMessage {
		raw, _ := json.Marshal(params)
		return museRPCMessage{Method: method, Params: raw}
	}
	a := &MuseCLIAdapter{}
	a.handleIdleMuseMSP(s, event("turn/started", map[string]any{"turnId": "wake-1"}))
	a.handleIdleMuseMSP(s, event("item/completed", map[string]any{"item": map[string]any{"itemId": "i1", "kind": "agentMessage", "turnId": "wake-1", "text": "Build finished: 3 tests failed.", "wakeSourceDescriptions": []string{"CI build"}}}))
	a.handleIdleMuseMSP(s, event("turn/completed", map[string]any{"turnId": "wake-1", "terminal": "completed"}))
	if len(got) != 1 || got[0].Owner != "chat-1" || got[0].Text != "Build finished: 3 tests failed." || len(got[0].Sources) != 1 || got[0].Sources[0] != "CI build" {
		t.Fatalf("wake reply = %+v", got)
	}
}
