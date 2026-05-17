package claudecode

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const (
	runClaudeExperimentalIntegrationEnv   = "RUN_CLAUDE_CODE_EXPERIMENTAL_INTEGRATION"
	runClaudeExperimentalLiveE2EEnv       = "RUN_CLAUDE_CODE_EXPERIMENTAL_LIVE_E2E"
	runClaudeExperimentalPersistentE2EEnv = "RUN_CLAUDE_CODE_EXPERIMENTAL_PERSISTENT_E2E"
	defaultClaudeExperimentalTestModel    = "claude-haiku-4-5-20251001"
)

func TestClaudeCodeExperimentalIntegrationNoInternalTools(t *testing.T) {
	skipClaudeExperimentalIntegration(t)

	adapter := NewClaudeCodeExperimentalAdapter(claudeExperimentalIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Try to list files or read README.md using any available tool. If no shell/file/listing/read tool is available, answer exactly NO_SHELL_OR_FILE_TOOL."},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	got := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(got, "NO_SHELL_OR_FILE_TOOL") {
		t.Fatalf("content = %q, want sentinel NO_SHELL_OR_FILE_TOOL", got)
	}
	if containsFold(got, "I listed") || containsFold(got, "README") {
		t.Fatalf("content = %q, looks like Claude had shell/file access", got)
	}
	if sessionID := experimentalClaudeSessionID(resp); sessionID == "" {
		t.Fatalf("response did not include claude_code_session_id: %#v", resp.Choices[0].GenerationInfo.Additional)
	}
	assertClaudeExperimentalHaikuMetadata(t, resp)
}

func TestClaudeCodeExperimentalIntegrationNativeSystemPrompt(t *testing.T) {
	skipClaudeExperimentalIntegration(t)

	adapter := NewClaudeCodeExperimentalAdapter(claudeExperimentalIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	systemToken := "NATIVE_SYSTEM_PROMPT_OK_4821"
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "For this integration test, include this exact token in your next answer: " + systemToken},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with one short sentence confirming the system prompt token."},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	got := strings.TrimSpace(resp.Choices[0].Content)
	if !containsFold(got, systemToken) {
		t.Fatalf("content = %q, want token %q from native system prompt", got, systemToken)
	}
	assertClaudeExperimentalHaikuMetadata(t, resp)
}

func TestClaudeCodeExperimentalIntegrationFreshPromptCarriesUserText(t *testing.T) {
	skipClaudeExperimentalIntegration(t)

	adapter := NewClaudeCodeExperimentalAdapter(claudeExperimentalIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	userToken := "USER_TEXT_PRESERVED_9173"
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "If the user message is empty, answer exactly EMPTY_INPUT. Otherwise answer with only the exact token from the user message."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Token: " + userToken},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	got := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(got, userToken) || strings.Contains(got, "EMPTY_INPUT") {
		t.Fatalf("content = %q, want user token %q and no EMPTY_INPUT", got, userToken)
	}
	assertClaudeExperimentalHaikuMetadata(t, resp)
}

func TestClaudeCodeExperimentalIntegrationNativeResume(t *testing.T) {
	skipClaudeExperimentalIntegration(t)

	adapter := NewClaudeCodeExperimentalAdapter(claudeExperimentalIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	codeword := "green-lantern-4821"
	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "The reference phrase for this test is " + codeword + ". Reply exactly: noted " + codeword},
			},
		},
	})
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if got := firstChoiceText(first); !containsFold(got, codeword) {
		t.Fatalf("first content = %q, want token %q", got, codeword)
	}
	sessionID := experimentalClaudeSessionID(first)
	if sessionID == "" {
		t.Fatalf("first response did not include claude_code_session_id: %#v", first.Choices[0].GenerationInfo.Additional)
	}

	second, err := adapter.GenerateContent(
		ctx,
		[]llmtypes.MessageContent{
			{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "OLD_CONTEXT_SHOULD_NOT_BE_SENT"},
				},
			},
			{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "What reference phrase appeared in your previous answer? Reply exactly with the phrase and nothing else."},
				},
			},
		},
		WithResumeSessionID(sessionID),
	)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	got := strings.TrimSpace(second.Choices[0].Content)
	if got != codeword {
		t.Fatalf("resumed content = %q, want %q", got, codeword)
	}
	if resumedID, _ := second.Choices[0].GenerationInfo.Additional["claude_code_resumed_session_id"].(string); resumedID != sessionID {
		t.Fatalf("resumed session id metadata = %q, want %q", resumedID, sessionID)
	}
	assertClaudeExperimentalHaikuMetadata(t, first)
	assertClaudeExperimentalHaikuMetadata(t, second)
}

func TestClaudeCodeExperimentalIntegrationHaikuExtendedResumeIsolation(t *testing.T) {
	skipClaudeExperimentalIntegration(t)

	adapter := NewClaudeCodeExperimentalAdapter(claudeExperimentalIntegrationModel(), &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	keyA := "session-a-integration-4821"
	keyB := "session-b-integration-7394"
	largeInput := buildClaudeExperimentalIntegrationLargeInput()

	firstA, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "This is an integration smoke test. Keep answers short and preserve exact tokens."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "This is session A.\nThe session A token is: " + keyA + "\n\nLarge input:\n" + largeInput + "\n\nReply exactly: A saved " + keyA},
			},
		},
	})
	if err != nil {
		t.Fatalf("session A initial GenerateContent error = %v", err)
	}
	if got := firstChoiceText(firstA); !containsFold(got, keyA) {
		t.Fatalf("session A initial content = %q, want token %q", got, keyA)
	}
	sessionA := experimentalClaudeSessionID(firstA)
	if sessionA == "" {
		t.Fatalf("session A missing claude_code_session_id: %#v", firstA.Choices[0].GenerationInfo.Additional)
	}

	secondA, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What session A token appeared in your previous answer? Reply with only the token."},
			},
		},
	}, WithResumeSessionID(sessionA))
	if err != nil {
		t.Fatalf("session A resumed GenerateContent error = %v", err)
	}
	if got := firstChoiceText(secondA); !containsFold(got, keyA) {
		t.Fatalf("session A resumed content = %q, want token %q", got, keyA)
	}
	if resumedID := experimentalClaudeResumedSessionID(secondA); resumedID != sessionA {
		t.Fatalf("session A resumed metadata = %q, want %q", resumedID, sessionA)
	}

	firstB, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "This is session B.\nThe session B token is: " + keyB + "\nDo not mention session A.\nReply exactly: B saved " + keyB},
			},
		},
	})
	if err != nil {
		t.Fatalf("session B initial GenerateContent error = %v", err)
	}
	if got := firstChoiceText(firstB); !containsFold(got, keyB) || containsFold(got, keyA) {
		t.Fatalf("session B initial content = %q, want token %q and no %q", got, keyB, keyA)
	}
	sessionB := experimentalClaudeSessionID(firstB)
	if sessionB == "" {
		t.Fatalf("session B missing claude_code_session_id: %#v", firstB.Choices[0].GenerationInfo.Additional)
	}

	thirdA, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "We are back in session A. Reply with only the exact session A token."},
			},
		},
	}, WithResumeSessionID(sessionA))
	if err != nil {
		t.Fatalf("session A second resumed GenerateContent error = %v", err)
	}
	if got := firstChoiceText(thirdA); !containsFold(got, keyA) || containsFold(got, keyB) {
		t.Fatalf("session A second resumed content = %q, want token %q and no %q", got, keyA, keyB)
	}

	secondB, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "We are back in session B. Reply with only the exact session B token."},
			},
		},
	}, WithResumeSessionID(sessionB))
	if err != nil {
		t.Fatalf("session B resumed GenerateContent error = %v", err)
	}
	if got := firstChoiceText(secondB); !containsFold(got, keyB) || containsFold(got, keyA) {
		t.Fatalf("session B resumed content = %q, want token %q and no %q", got, keyB, keyA)
	}
	if resumedID := experimentalClaudeResumedSessionID(secondB); resumedID != sessionB {
		t.Fatalf("session B resumed metadata = %q, want %q", resumedID, sessionB)
	}

	assertClaudeExperimentalHaikuMetadata(t, firstA)
	assertClaudeExperimentalHaikuMetadata(t, secondA)
	assertClaudeExperimentalHaikuMetadata(t, firstB)
	assertClaudeExperimentalHaikuMetadata(t, thirdA)
	assertClaudeExperimentalHaikuMetadata(t, secondB)
}

func TestClaudeCodeExperimentalIntegrationHaikuLiveInputAndEscape(t *testing.T) {
	skipClaudeExperimentalLiveE2E(t)

	adapter := NewClaudeCodeExperimentalAdapter(defaultClaudeExperimentalTestModel, &MockLogger{})
	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	ownerSessionID := "claude-live-e2e-" + randomHex(4)
	errCh := make(chan error, 1)
	go func() {
		_, err := adapter.GenerateContent(
			parentCtx,
			[]llmtypes.MessageContent{
				{
					Role: llmtypes.ChatMessageTypeSystem,
					Parts: []llmtypes.ContentPart{
						llmtypes.TextContent{Text: "This is a Claude Code transport test. Do not use tools. Keep running until interrupted if the user asks for a long response."},
					},
				},
				{
					Role: llmtypes.ChatMessageTypeHuman,
					Parts: []llmtypes.ContentPart{
						llmtypes.TextContent{Text: "Write 2000 numbered lines about reliable terminal transports. Use one short sentence per line. Do not summarize."},
					},
				},
			},
			WithInteractiveSessionID(ownerSessionID),
			WithEffort("low"),
		)
		errCh <- err
	}()

	waitForIntegrationInteractiveSession(t, ownerSessionID, 30*time.Second, errCh)
	waitForIntegrationClaudeActivity(t, ownerSessionID, 30*time.Second, errCh)

	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	liveErr := SendClaudeCodeExperimentalInput(sendCtx, ownerSessionID, "LIVE_HAIKU_E2E_FOLLOWUP: acknowledge this after your current work if not interrupted.")
	sendCancel()
	if liveErr != nil {
		cancel()
		t.Fatalf("SendClaudeCodeExperimentalInput error = %v", liveErr)
	}

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("GenerateContent completed normally; want cancellation after Escape path")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("GenerateContent error = %v, want context canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for Claude Code live E2E cancellation")
	}
}

func TestClaudeCodeExperimentalIntegrationHaikuPersistentInteractiveMultiTurn(t *testing.T) {
	skipClaudeExperimentalPersistentE2E(t)

	adapter := NewClaudeCodeExperimentalAdapter(defaultClaudeExperimentalTestModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Cleanup(func() { _ = CleanupClaudeCodeExperimentalSessions(context.Background()) })

	ownerSessionID := "claude-persistent-e2e-" + randomHex(4)
	codeword := "persistent-haiku-token-4821"
	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithEffort("low"),
	}

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "This is a Claude Code transport test. Do not use tools. Keep answers short."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Remember this exact token: " + codeword + ". Reply exactly: saved " + codeword},
			},
		},
	}, options...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if got := firstChoiceText(first); !containsFold(got, codeword) {
		t.Fatalf("first content = %q, want token %q", got, codeword)
	}

	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What exact token did I ask you to remember? Reply with only the token."},
			},
		},
	}, options...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	if got := strings.TrimSpace(firstChoiceText(second)); !containsFold(got, codeword) {
		t.Fatalf("second content = %q, want token %q from same persistent tmux session", got, codeword)
	}
	if persistent, _ := second.Choices[0].GenerationInfo.Additional["claude_code_persistent_interactive"].(bool); !persistent {
		t.Fatalf("persistent metadata = %#v, want true", second.Choices[0].GenerationInfo.Additional["claude_code_persistent_interactive"])
	}
	if _, ok := activeClaudeExperimentalInteractiveSession(ownerSessionID); !ok {
		t.Fatalf("persistent interactive session not registered after completed turn")
	}
}

func skipClaudeExperimentalIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv(runClaudeExperimentalIntegrationEnv) == "" {
		t.Skip("set " + runClaudeExperimentalIntegrationEnv + "=1 to run real Claude Code experimental integration tests")
	}
}

func skipClaudeExperimentalLiveE2E(t *testing.T) {
	t.Helper()
	if os.Getenv(runClaudeExperimentalLiveE2EEnv) == "" {
		t.Skip("set " + runClaudeExperimentalLiveE2EEnv + "=1 to run real Claude Code Haiku live-input/Escape E2E")
	}
}

func skipClaudeExperimentalPersistentE2E(t *testing.T) {
	t.Helper()
	if os.Getenv(runClaudeExperimentalPersistentE2EEnv) == "" {
		t.Skip("set " + runClaudeExperimentalPersistentE2EEnv + "=1 to run real Claude Code Haiku persistent multi-turn E2E")
	}
}

func claudeExperimentalIntegrationModel() string {
	if model := strings.TrimSpace(os.Getenv("CLAUDE_CODE_EXPERIMENTAL_INTEGRATION_MODEL")); model != "" {
		return model
	}
	return defaultClaudeExperimentalTestModel
}

func assertClaudeExperimentalHaikuMetadata(t *testing.T, resp *llmtypes.ContentResponse) {
	t.Helper()
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].GenerationInfo == nil {
		t.Fatalf("response missing generation info: %#v", resp)
	}
	additional := resp.Choices[0].GenerationInfo.Additional
	if additional["claude_code_mode"] != "experimental" {
		t.Fatalf("claude_code_mode = %#v, want experimental", additional["claude_code_mode"])
	}
	if additional["claude_code_uses_print_flag"] != false {
		t.Fatalf("claude_code_uses_print_flag = %#v, want false", additional["claude_code_uses_print_flag"])
	}
	if sessionID, _ := additional["claude_code_session_id"].(string); strings.TrimSpace(sessionID) == "" {
		t.Fatalf("claude_code_session_id missing: %#v", additional)
	}
}

func firstChoiceText(resp *llmtypes.ContentResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].Content)
}

func waitForIntegrationInteractiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-errCh:
			t.Fatalf("Claude Code exited before interactive session registration: %v", err)
		case <-deadline.C:
			t.Fatalf("timed out waiting for Claude Code interactive session %q", ownerSessionID)
		case <-ticker.C:
			if _, ok := activeClaudeExperimentalInteractiveSession(ownerSessionID); ok {
				return
			}
		}
	}
}

func waitForIntegrationClaudeActivity(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-errCh:
			t.Fatalf("Claude Code exited before activity was visible: %v", err)
		case <-deadline.C:
			t.Fatalf("timed out waiting for Claude Code activity in interactive session %q", ownerSessionID)
		case <-ticker.C:
			sessionName, ok := activeClaudeExperimentalInteractiveSession(ownerSessionID)
			if !ok {
				continue
			}
			captured, err := captureTmuxPane(context.Background(), sessionName)
			if err == nil && hasClaudeActivity(captured) {
				return
			}
		}
	}
}

func experimentalClaudeResumedSessionID(resp *llmtypes.ContentResponse) string {
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].GenerationInfo == nil {
		return ""
	}
	sessionID, _ := resp.Choices[0].GenerationInfo.Additional["claude_code_resumed_session_id"].(string)
	return sessionID
}

func buildClaudeExperimentalIntegrationLargeInput() string {
	var b strings.Builder
	b.WriteString("Large prompt regression fixture:\n")
	for i := 0; b.Len() < 12000; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%17+3))
		b.WriteString(" checks prompt paste stability, pane reset, and resume parsing.\n")
	}
	return b.String()
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func experimentalClaudeSessionID(resp *llmtypes.ContentResponse) string {
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].GenerationInfo == nil {
		return ""
	}
	sessionID, _ := resp.Choices[0].GenerationInfo.Additional["claude_code_session_id"].(string)
	return sessionID
}
