package claudecode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-746-863 / PLAT-101: an exit-0 is_error result bypassed the quota
// constructor, so the next turn never learned that this account was spent.
// Stub only the external CLI; run the real structured result consumer.
func TestClaudeStructuredFiveHourLimitPreservesQuotaReset(t *testing.T) {
	const notice = "You've hit your 5-hour limit · resets 2:40pm (Asia/Calcutta)"
	dir := t.TempDir()
	// Use a quoted heredoc because the provider's notice contains an apostrophe.
	script := "#!/bin/sh\ncat <<'RESULT'\n{\"type\":\"result\",\"is_error\":true,\"result\":\"" + notice + "\"}\nRESULT\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := NewClaudeCodeInteractiveAdapter("claude-opus-5-5", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	before := time.Now()
	_, err := adapter.generateContentStructured(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "hello"}}}}, &llmtypes.CallOptions{Metadata: &llmtypes.Metadata{Custom: map[string]interface{}{MetadataKeyWorkingDir: dir}}})
	if llmerrors.KindOf(err) != llmerrors.KindQuotaExhausted || llmerrors.IsRetryable(err) {
		t.Fatalf("quota result = %v", err)
	}
	reset := llmerrors.RetryAtOrZero(err)
	if !reset.After(before) || reset.Sub(before) > 24*time.Hour || reset.Hour() != 9 || reset.Minute() != 10 {
		t.Fatalf("lost 2:40pm Asia/Calcutta reset: %v", reset)
	}
	// The first refusal and a later memo rejection both pass through stored
	// string-only completion events before their explanation reaches the chat.
	memo := &llmerrors.Error{Kind: llmerrors.KindQuotaExhausted, Provider: "claude-code", Model: "claude-opus-5-5", RetryAt: reset, Err: errors.New("selected model is quota-exhausted")}
	for _, failure := range []error{err, errors.New(fmt.Sprintf("llm error: selected LLM failed: %v", err)), errors.New(memo.Error())} {
		display := llmerrors.ProviderDisplay(failure)
		if display.Code != "quota_exhausted" || !strings.Contains(display.Message, "9:10am UTC") || display.Retryable {
			t.Fatalf("reset missing from chat display: %+v", display)
		}
	}
}
