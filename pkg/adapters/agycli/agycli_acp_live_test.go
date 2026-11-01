package agycli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func agyACPText(resp *llmtypes.ContentResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Content
}

// A chat on `agy acp` with the service's Gemini key: two turns in one
// conversation, then the same conversation resumed by its exact id after the
// process ended.
func TestAgyACPRealMultiTurnAndResume(t *testing.T) {
	if !*codingCLIP0Live {
		t.Skip("run through the live coding CLI P0 runner")
	}
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("needs GEMINI_API_KEY")
	}
	t.Setenv(AgyTransportEnv, "acp")
	owner := "agy-acp-live-" + time.Now().Format("150405.000")
	t.Cleanup(func() { CloseAgyACPSession(owner) })
	adapter := NewAgyCLIAdapter("", "gemini-3.8-flash-low", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	options := []llmtypes.CallOption{WithWorkingDir(t.TempDir()), WithInteractiveSessionID(owner), WithPersistentInteractiveSession(true)}
	human := func(text string) []llmtypes.MessageContent {
		return []llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, text)}
	}
	stream := make(chan llmtypes.StreamChunk, 1024)
	deltas := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range stream {
			if chunk.Type == llmtypes.StreamChunkTypeContent {
				deltas++
			}
		}
	}()
	first, err := adapter.GenerateContent(ctx, human("Remember the code word ACP_OTTER. Reply with only: noted"), append(options, llmtypes.WithStreamingChan(stream))...)
	close(stream)
	<-done
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(first)
	if !ok || handle.NativeSessionID == "" || handle.Transport != llmtypes.CodingProviderTransportACP || deltas == 0 {
		t.Fatalf("first turn: handle %+v ok=%v deltas=%d", handle, ok, deltas)
	}
	gi := first.Choices[0].GenerationInfo
	if gi == nil || gi.TotalTokens == nil || *gi.TotalTokens == 0 {
		t.Fatalf("no token usage: %+v", gi)
	}
	second, err := adapter.GenerateContent(ctx, human("What was the code word? Reply with only the word."), options...)
	if err != nil || !strings.Contains(strings.ToUpper(agyACPText(second)), "ACP_OTTER") {
		t.Fatalf("second turn: %q, %v", agyACPText(second), err)
	}
	CloseAgyACPSession(owner)
	resumed, err := adapter.GenerateContent(ctx, human("Say the code word once more, only the word."), append(options, WithResumeSessionID(handle.NativeSessionID))...)
	if err != nil || !strings.Contains(strings.ToUpper(agyACPText(resumed)), "ACP_OTTER") {
		t.Fatalf("resumed turn: %q, %v", agyACPText(resumed), err)
	}
	if h, _ := llmtypes.ExtractCodingProviderSessionHandleFromResponse(resumed); h.NativeSessionID != handle.NativeSessionID {
		t.Fatalf("resume changed the session: %s -> %s", handle.NativeSessionID, h.NativeSessionID)
	}
}

// Reviewer's probe: whether a shell agy runs can see the service key (presence only).
func TestAgyACPKeyProbe(t *testing.T) {
	if !*codingCLIP0Live || os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("live, needs GEMINI_API_KEY")
	}
	t.Setenv(AgyTransportEnv, "acp")
	owner := "agy-acp-probe-" + time.Now().Format("150405.000")
	t.Cleanup(func() { CloseAgyACPSession(owner) })
	work := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := NewAgyCLIAdapter("", "gemini-3.8-flash-low", nil).GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, `Run exactly this shell command once and reply with only its output: if [ -n "$GEMINI_API_KEY$GOOGLE_API_KEY" ]; then echo KEY_SET; else echo KEY_UNSET; fi`)},
		WithWorkingDir(work), WithInteractiveSessionID(owner), WithPersistentInteractiveSession(true))
	t.Logf("shell probe: %q err=%v", agyACPText(resp), err)
}
