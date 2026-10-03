package cursorcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCursorStructuredLaunchAppliesEffortAfterModelOverride(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$CURSOR_AGENT_TEST_ARGS"
printf '%s\n' '{"type":"result","result":"OK","session_id":"s1"}'
`
	if err := os.WriteFile(filepath.Join(dir, "cursor-agent"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CURSOR_AGENT_TEST_ARGS", argsPath)
	adapter := NewCursorCLIAdapter("", "auto", &MockLogger{})
	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{{
		Role:  llmtypes.ChatMessageTypeHuman,
		Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply OK"}},
	}}, WithCursorStructuredTransport(true), WithWorkingDir(dir),
		WithCursorModel("grok-4.6[effort=high,fast=false]"), llmtypes.WithReasoningEffort("low"))
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--model\ngrok-4.6[fast=false,effort=low]\n") {
		t.Fatalf("launch args = %s", args)
	}
}
