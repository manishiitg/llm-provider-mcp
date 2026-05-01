package kimi

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestKimiCLILiveSmoke(t *testing.T) {
	if os.Getenv("KIMI_CLI_LIVE_TEST") != "1" {
		t.Skip("set KIMI_CLI_LIVE_TEST=1 to run the live Kimi CLI smoke test")
	}
	if _, err := exec.LookPath("kimi"); err != nil {
		t.Skip("kimi CLI is not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Setenv("KIMI_CODE_CLI_AGENT_FILE", "")
	t.Setenv("KIMI_CODE_CLI_TOOL_MODE", "none")
	t.Setenv("KIMI_CODE_CLI_MAX_STEPS_PER_TURN", "1")

	adapter := NewKimiCLIAdapter(ModelKimiCode, testLogger{})
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say hello in one short sentence."}},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent returned error: %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Content) == "" {
		t.Fatalf("GenerateContent returned empty response: %#v", resp)
	}
}
