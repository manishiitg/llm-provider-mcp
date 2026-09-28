package agycli

import (
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestAgyLaunchOnlyDoesNotRequireHumanPrompt(t *testing.T) {
	adapter := NewAgyCLIAdapter("", "gemini-3.8-flash-high", nil)
	_, err := adapter.GenerateContent(t.Context(), nil,
		WithInteractiveSessionID("restored-owner"),
		WithPersistentInteractiveSession(true),
		WithNativeToolsMode("invalid-test-mode"),
		llmtypes.WithReasoningEffort("high"),
		llmtypes.WithCodingProviderLaunchOnly(),
	)
	// The invalid tool mode stops before any CLI process is started. Reaching it
	// proves launch-only skipped the normal human-prompt requirement and accepted
	// a redundant effort matching the selected model's baked-in level.
	if err == nil || !strings.Contains(err.Error(), "native tools mode") {
		t.Fatalf("launch-only error = %v, want invalid tool mode after prompt and effort checks", err)
	}
}

func TestAgyInteractiveEffortMustMatchModel(t *testing.T) {
	adapter := NewAgyCLIAdapter("", "gemini-3.8-flash-high", nil)
	_, err := adapter.GenerateContent(t.Context(), []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "hi"),
	},
		WithInteractiveSessionID("owner"),
		WithPersistentInteractiveSession(true),
		llmtypes.WithReasoningEffort("low"),
	)
	if err == nil || !strings.Contains(err.Error(), "does not match the booted model") {
		t.Fatalf("interactive effort mismatch error = %v", err)
	}
}
