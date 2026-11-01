package musecli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func skipMuseMSPLive(t *testing.T) {
	t.Helper()
	if !*codingCLIP0Live {
		t.Skip("run through the live coding CLI P0 runner")
	}
	t.Setenv(MuseTransportEnv, "msp")
}

func museMSPText(resp *llmtypes.ContentResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Content
}

// A chat on `muse serve`: two turns in one session, the second remembering the
// first, and the native session id the transfer and resume paths key on.
func TestMuseMSPRealMultiTurn(t *testing.T) {
	skipMuseMSPLive(t)
	owner := "msp-live-" + museCommandID()
	t.Cleanup(func() { CloseMuseMSPSession(owner) })
	adapter := NewMuseCLIAdapter("", "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	options := []llmtypes.CallOption{WithWorkingDir(t.TempDir()), WithInteractiveSessionID(owner)}

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Remember the code word MSP_ZEBRA. Reply with only: noted")}, options...)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(first)
	if !ok || handle.NativeSessionID == "" || handle.Transport != llmtypes.CodingProviderTransportMSP {
		t.Fatalf("first turn handle = %+v, %v; want a native session on msp", handle, ok)
	}
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "What was the code word? Reply with only the word.")}, options...)
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if !strings.Contains(strings.ToUpper(museMSPText(second)), "MSP_ZEBRA") {
		t.Fatalf("second answer = %q, want the code word from the first turn", museMSPText(second))
	}
	if h, _ := llmtypes.ExtractCodingProviderSessionHandleFromResponse(second); h.NativeSessionID != handle.NativeSessionID {
		t.Fatalf("session changed between turns: %s -> %s", handle.NativeSessionID, h.NativeSessionID)
	}
}
