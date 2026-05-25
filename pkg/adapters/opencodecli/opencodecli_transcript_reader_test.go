package opencodecli

import (
	"encoding/json"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// realExportFixture is a real `opencode export` output captured from a
// 3-turn multi-turn run against the free tier (deepseek-v4-flash-free)
// — trimmed to the fields the reader actually consumes. Keeping a
// real-binary fixture (rather than a hand-written one) means parser
// regressions vs. opencode's schema land here loudly instead of in
// production.
const realExportFixture = `{
  "info": {
    "id": "ses_test123",
    "model": {"id": "deepseek-v4-flash-free", "providerID": "opencode"},
    "tokens": {
      "input": 8468,
      "output": 16,
      "reasoning": 65,
      "cache": {"read": 16128, "write": 0}
    },
    "cost": 0
  },
  "messages": [
    {
      "info": {"id": "msg_a", "role": "user"},
      "parts": [{"type": "text", "text": "Remember this token: WIDGET_A47. Reply with ONLY the word ACK."}]
    },
    {
      "info": {"id": "msg_b", "role": "assistant"},
      "parts": [
        {"type": "reasoning"},
        {"type": "text", "text": "ACK"},
        {"type": "step-finish"}
      ]
    },
    {
      "info": {"id": "msg_c", "role": "user"},
      "parts": [{"type": "text", "text": "Which two tokens?"}]
    },
    {
      "info": {"id": "msg_d", "role": "assistant"},
      "parts": [
        {"type": "tool", "tool": "read", "callID": "call_1", "input": {"path": "/tmp/x"}, "state": {"status": "completed", "output": "file contents"}},
        {"type": "text", "text": "WIDGET_A47, WIDGET_B23"},
        {"type": "step-finish"}
      ]
    }
  ]
}`

func TestOpencodeExportParsesRealBinaryShape(t *testing.T) {
	var exp opencodeExport
	if err := json.Unmarshal([]byte(realExportFixture), &exp); err != nil {
		t.Fatalf("decode realExportFixture: %v", err)
	}
	if got, want := exp.Info.Tokens.Input, 8468; got != want {
		t.Errorf("Info.Tokens.Input = %d, want %d", got, want)
	}
	if got, want := exp.Info.Tokens.Cache.Read, 16128; got != want {
		t.Errorf("Info.Tokens.Cache.Read = %d, want %d", got, want)
	}
	if got := exp.Info.Model.ID + "/" + exp.Info.Model.ProviderID; got != "deepseek-v4-flash-free/opencode" {
		t.Errorf("model id round-trip = %q; want 'deepseek-v4-flash-free/opencode'", got)
	}
	if len(exp.Messages) != 4 {
		t.Fatalf("Messages len = %d, want 4", len(exp.Messages))
	}
}

func TestLastTurnMessagesReturnsOnlyMostRecentPair(t *testing.T) {
	var exp opencodeExport
	if err := json.Unmarshal([]byte(realExportFixture), &exp); err != nil {
		t.Fatalf("decode realExportFixture: %v", err)
	}
	got := exp.lastTurnMessages()
	if len(got) != 2 {
		t.Fatalf("lastTurnMessages() returned %d messages, want 2 (user + assistant for the current turn)", len(got))
	}
	if got[0].Role != llmtypes.ChatMessageTypeHuman {
		t.Errorf("first message role = %q, want %q", got[0].Role, llmtypes.ChatMessageTypeHuman)
	}
	if got[1].Role != llmtypes.ChatMessageTypeAI {
		t.Errorf("second message role = %q, want %q", got[1].Role, llmtypes.ChatMessageTypeAI)
	}
	// First message is the latest user prompt.
	userText := messageFirstText(got[0])
	if userText != "Which two tokens?" {
		t.Errorf("user message = %q, want 'Which two tokens?'", userText)
	}
}

func TestOpencodeExportMessageMapsToolCallsBothSides(t *testing.T) {
	// The assistant message in the fixture contains a tool call with
	// a populated state.output. The reader must surface BOTH a
	// ToolCall and a matching ToolCallResponse so the host's
	// conversation log captures both halves of the exchange (matches
	// the splice shape cursor's transcript reader produces).
	var exp opencodeExport
	if err := json.Unmarshal([]byte(realExportFixture), &exp); err != nil {
		t.Fatalf("decode realExportFixture: %v", err)
	}
	assistant := opencodeExportMessageToContent(exp.Messages[3])
	var sawCall, sawResp bool
	for _, p := range assistant.Parts {
		switch v := p.(type) {
		case llmtypes.ToolCall:
			sawCall = true
			if v.FunctionCall == nil || v.FunctionCall.Name != "read" {
				t.Errorf("ToolCall name = %v, want 'read'", v.FunctionCall)
			}
		case llmtypes.ToolCallResponse:
			sawResp = true
			if v.Content != "file contents" {
				t.Errorf("ToolCallResponse.Content = %q, want 'file contents'", v.Content)
			}
		}
	}
	if !sawCall {
		t.Error("assistant parts missing ToolCall — reader must surface the tool invocation")
	}
	if !sawResp {
		t.Error("assistant parts missing ToolCallResponse — reader must surface the tool output when state.output is non-empty")
	}
}

func TestOpencodeExportMessageDropsReasoningAndStepMarkers(t *testing.T) {
	// reasoning + step-start + step-finish are internal opencode
	// machinery — surfacing them would pollute the host's
	// conversation log with content that isn't actually part of the
	// model's response.
	var exp opencodeExport
	if err := json.Unmarshal([]byte(realExportFixture), &exp); err != nil {
		t.Fatalf("decode realExportFixture: %v", err)
	}
	assistant := opencodeExportMessageToContent(exp.Messages[1])
	if len(assistant.Parts) != 1 {
		t.Fatalf("assistant parts len = %d, want 1 (only the text part, no reasoning/step-finish)", len(assistant.Parts))
	}
	if tc, ok := assistant.Parts[0].(llmtypes.TextContent); !ok || tc.Text != "ACK" {
		t.Errorf("assistant part[0] = %+v, want TextContent{Text:'ACK'}", assistant.Parts[0])
	}
}

func messageFirstText(m llmtypes.MessageContent) string {
	for _, p := range m.Parts {
		if t, ok := p.(llmtypes.TextContent); ok {
			return t.Text
		}
	}
	return ""
}
