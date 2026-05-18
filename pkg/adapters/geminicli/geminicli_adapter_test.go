package geminicli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)  { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any) { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) {
	fmt.Printf("DEBUG: "+format+"\n", args...)
}

const geminiCLIContractModel = "low"

func TestGeminiInteractiveStreamTmuxScreenFlag(t *testing.T) {
	t.Setenv(EnvGeminiInteractiveStreamTmuxScreen, "")
	if !geminiInteractiveStreamTmuxScreenEnabled() {
		t.Fatal("tmux screen streaming should be enabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(EnvGeminiInteractiveStreamTmuxScreen, value)
		if !geminiInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be enabled for %q", value)
		}
	}

	for _, value := range []string{"0", "false", "FALSE", "no", "off"} {
		t.Setenv(EnvGeminiInteractiveStreamTmuxScreen, value)
		if geminiInteractiveStreamTmuxScreenEnabled() {
			t.Fatalf("tmux screen streaming should be disabled for %q", value)
		}
	}
}

func TestGeminiWorkingDirOptionOverridesProjectDir(t *testing.T) {
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(" " + workDir + " ")(opts)
	WithProjectDirID("legacy-temp-id")(opts)

	projectDir, projectDirID := resolveProjectDir(opts, "")
	if projectDir != workDir {
		t.Fatalf("project dir = %q, want working dir %q", projectDir, workDir)
	}
	if projectDirID != "" {
		t.Fatalf("project dir ID = %q, want empty when using explicit working dir", projectDirID)
	}

	interactiveDir, interactiveDirID, err := prepareGeminiInteractiveProjectDir("owner-session", opts)
	if err != nil {
		t.Fatalf("prepareGeminiInteractiveProjectDir returned error: %v", err)
	}
	if interactiveDir != workDir {
		t.Fatalf("interactive dir = %q, want working dir %q", interactiveDir, workDir)
	}
	if interactiveDirID != "" {
		t.Fatalf("interactive project dir ID = %q, want empty when using explicit working dir", interactiveDirID)
	}
}

func TestGeminiInteractiveLaunchFingerprintIgnoresDynamicSystemPromptText(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", geminiCLIContractModel, &MockLogger{})
	opts := &llmtypes.CallOptions{}
	policyPath := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policyPath, []byte(`[[rule]]
toolName = "*"
decision = "deny"
priority = 999
`), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	WithAdminPolicyPath(policyPath)(opts)
	WithProjectSettings(`{"mcpServers":{"api-bridge":{"command":"bridge-a"}}}`)(opts)

	baseline := adapter.geminiInteractiveLaunchFingerprint(opts, "system prompt A")
	if baseline == "" {
		t.Fatal("fingerprint should not be empty")
	}
	if got := adapter.geminiInteractiveLaunchFingerprint(opts, "system prompt A"); got != baseline {
		t.Fatalf("fingerprint should be stable, got %q want %q", got, baseline)
	}
	if got := adapter.geminiInteractiveLaunchFingerprint(opts, "system prompt B"); got != baseline {
		t.Fatal("fingerprint should not change for dynamic system prompt text")
	}
	if got := adapter.geminiInteractiveLaunchFingerprint(opts, ""); got == baseline {
		t.Fatal("fingerprint should distinguish absent vs present system prompt")
	}

	changedSettings := &llmtypes.CallOptions{}
	WithAdminPolicyPath(policyPath)(changedSettings)
	WithProjectSettings(`{"mcpServers":{"api-bridge":{"command":"bridge-b"}}}`)(changedSettings)
	if got := adapter.geminiInteractiveLaunchFingerprint(changedSettings, "system prompt A"); got == baseline {
		t.Fatal("fingerprint should change when MCP/project settings change")
	}

	if err := os.WriteFile(policyPath, []byte(`[[rule]]
toolName = "*"
decision = "allow"
priority = 999
`), 0o600); err != nil {
		t.Fatalf("rewrite policy: %v", err)
	}
	if got := adapter.geminiInteractiveLaunchFingerprint(opts, "system prompt A"); got == baseline {
		t.Fatal("fingerprint should change when policy file content changes")
	}
}

func TestBuildGeminiInteractivePromptCarriesPriorConversation(t *testing.T) {
	token := "GEMINI_CONTEXT_TOKEN"
	prompt := buildGeminiInteractivePrompt([]llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Take note of " + token},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeAI,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "ACK_" + token},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What token did I ask you to remember?"},
			},
		},
	})

	if !strings.Contains(prompt, "Previous conversation context for this same chat") {
		t.Fatalf("prompt missing prior conversation header: %q", prompt)
	}
	if !strings.Contains(prompt, token) {
		t.Fatalf("prompt missing prior token %q: %q", token, prompt)
	}
	if !strings.Contains(prompt, "Current user message:") {
		t.Fatalf("prompt missing current user marker: %q", prompt)
	}
}

func TestGeminiCLIInteractiveIntegrationFlashLite(t *testing.T) {
	if os.Getenv("RUN_GEMINI_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_GEMINI_CLI_INTERACTIVE_E2E=1 to run real Gemini CLI interactive tmux E2E")
	}
	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		t.Fatal("RUN_GEMINI_CLI_INTERACTIVE_E2E=1 requires GEMINI_API_KEY so Gemini CLI does not block on interactive auth")
	}
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ownerSessionID := "gemini-interactive-e2e-" + geminiRandomHex(4)
	policyPath := filepath.Join(t.TempDir(), "gemini-admin-policy.toml")
	if err := os.WriteFile(policyPath, []byte(`[[rule]]
toolName = "*"
decision = "deny"
priority = 999
deny_message = "No tools are needed for this smoke test."
`), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectSettings(`{}`),
		WithAdminPolicyPath(policyPath),
	}
	streamChan := make(chan llmtypes.StreamChunk, 32)
	firstOptions := append([]llmtypes.CallOption{}, options...)
	firstOptions = append(firstOptions, llmtypes.WithStreamingChan(streamChan))

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Keep answers short."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Remember the token GEMINI_TMUX_OK_4821. Reply exactly: saved GEMINI_TMUX_OK_4821"}}},
	}, firstOptions...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if got := first.Choices[0].Content; !strings.Contains(got, "GEMINI_TMUX_OK_4821") {
		t.Fatalf("first content = %q, want token", got)
	}
	assertGeminiInteractiveTerminalOnlyStream(t, streamChan)

	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What token did I ask you to remember? Reply with only the token."}}},
	}, options...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	if got := second.Choices[0].Content; !strings.Contains(got, "GEMINI_TMUX_OK_4821") {
		t.Fatalf("second content = %q, want token from same tmux session", got)
	}
}

func TestAppendGeminiPolicyArgs(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithPolicyPath("/tmp/user-policy.toml")(opts)
	WithAdminPolicyPath("/tmp/admin-policy.toml")(opts)

	args := []string{"--output-format", "stream-json"}
	appendGeminiPolicyArgs(&args, opts)

	got := strings.Join(args, " ")
	for _, want := range []string{
		"--policy /tmp/user-policy.toml",
		"--admin-policy /tmp/admin-policy.toml",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("args = %q, want %q", got, want)
		}
	}
}

func TestGeminiCommandStringRedactsAPIKey(t *testing.T) {
	got := geminiCommandString([]string{"new-session", "-e", "GEMINI_API_KEY=secret-value", "-e", "GOOGLE_API_KEY=google-secret", "gemini"})
	if strings.Contains(got, "secret-value") {
		t.Fatalf("command string leaked API key: %q", got)
	}
	if strings.Contains(got, "google-secret") {
		t.Fatalf("command string leaked Google API key: %q", got)
	}
	if !strings.Contains(got, "GEMINI_API_KEY=<redacted>") {
		t.Fatalf("command string = %q, want redacted API key marker", got)
	}
	if !strings.Contains(got, "GOOGLE_API_KEY=<redacted>") {
		t.Fatalf("command string = %q, want redacted Google API key marker", got)
	}
}

func TestGeminiAPIKeyEnvSetsBothCommonKeyNames(t *testing.T) {
	got := geminiAPIKeyEnv(" test-key ")
	want := []string{"GEMINI_API_KEY=test-key", "GOOGLE_API_KEY=test-key"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("geminiAPIKeyEnv() = %#v, want %#v", got, want)
	}
	if got := geminiAPIKeyEnv(" "); got != nil {
		t.Fatalf("geminiAPIKeyEnv(blank) = %#v, want nil", got)
	}
}

func TestExtractGeminiVisibleAssistantTextFiltersTUIProgress(t *testing.T) {
	input := `
Gemini CLI v0.42.0
Generating... esc to cancel
Thinking with high effort
✦ Useful assistant text
Press Ctrl+O to expand pasted text
>   Type your message or @path/to/file
`
	got := extractGeminiVisibleAssistantText(input)
	if got != "Useful assistant text" {
		t.Fatalf("visible assistant text = %q, want useful assistant text only", got)
	}
}

func TestParseGeminiInteractiveResponsePrefersLatestMarkedAssistantBlock(t *testing.T) {
	baseline := "Gemini ready\n>"
	captured := baseline + `
│ ✓ execute_shell_command (api-bridge MCP Server) {"command":"ls"}
│ {"stdout":"tool output"}
✦ Earlier assistant text near tool output.
────────────────────────────────────────────────────────────────────────────────
✦ Final answer:
  - one
  - two
> Type your message
`

	got := parseGeminiInteractiveResponse(captured, baseline, "", nil)
	want := "Final answer:\n- one\n- two"
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
}

func TestParseGeminiInteractiveResponseAcceptsArrowAssistantMarker(t *testing.T) {
	baseline := "Gemini ready\n>"
	captured := baseline + `
-> Final from arrow marker
> Type your message
`

	got := parseGeminiInteractiveResponse(captured, baseline, "", nil)
	want := "Final from arrow marker"
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
}

func TestParseGeminiInteractiveResponseRejectsQueuedValidationEcho(t *testing.T) {
	baseline := "Gemini ready\n>"
	captured := baseline + `
> ## Pre-validation failed (retry attempt 3)

❌ PRE-VALIDATION FAILED

Checks: 0 passed, 1 failed

Fix the specific issues above and re-produce the required outputs.

> Type your message
`

	got := parseGeminiInteractiveResponse(captured, baseline, "## Pre-validation failed (retry attempt 3)", nil)
	if got != "" {
		t.Fatalf("parsed queued validation echo = %q, want empty", got)
	}
}

func TestExtractGeminiVisibleAssistantTextFiltersMCPToolPanels(t *testing.T) {
	input := `
ℹ Waiting for MCP servers to initialize... Slash commands are still available
and prompts will be queued.
│ ✓  execute_shell_command (api-bridge MCP Server) {"command":"jq '[.steps[]]'"}
│ {
│   "stdout": "[\n  {\n    \"id\": \"prepare-test-fixtures\",\n    \"title\": \"Prepare Regression Fixtures\"\n  }\n]\n",
│   "stderr": "",
│   "exit_code": 0,
│   "execution_time_ms": 44
│ }
Hello! I'm your Workflow Builder Agent for the testing workflow.
I see 11 planned regression steps.
`
	got := extractGeminiVisibleAssistantText(input)
	want := "Hello! I'm your Workflow Builder Agent for the testing workflow.\nI see 11 planned regression steps."
	if got != want {
		t.Fatalf("visible assistant text = %q, want %q", got, want)
	}
	for _, forbidden := range []string{
		"Waiting for MCP",
		"prompts will be queued",
		"execute_shell_command",
		"api-bridge",
		"stdout",
		"exit_code",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("visible assistant text leaked %q in %q", forbidden, got)
		}
	}
}

func TestGeminiInteractiveAPIErrorIgnoresEchoedPromptText(t *testing.T) {
	input := `
ℹ Initializing... Prompts will be queued.
Question ID: q-20260517T164842-0gkjlo
Raw operator question: Re-run latency RCA and collect API error context from logs.
Execution checklist:
- Query APIs and output files.
✦ I am checking the logs now.
`
	if got := geminiInteractiveAPIError(input); got != "" {
		t.Fatalf("geminiInteractiveAPIError() = %q, want no error for echoed prompt text", got)
	}
}

func TestGeminiInteractiveAPIErrorDetectsActualErrorLine(t *testing.T) {
	input := `
ℹ Initializing... Prompts will be queued.
API Error: API_KEY_INVALID: API key not valid. Please pass a valid API key.
`
	got := geminiInteractiveAPIError(input)
	if !strings.Contains(got, "API Error") || !strings.Contains(got, "API_KEY_INVALID") {
		t.Fatalf("geminiInteractiveAPIError() = %q, want actual API error line", got)
	}
}

func TestExtractGeminiVisibleAssistantTextFiltersAdminPolicyWarnings(t *testing.T) {
	input := `
⚠  [ADMIN] Policy file warning in restrict-tools.toml:
Unrecognized tool name
Rule #2: The "__" syntax for MCP tools is strictly deprecated. Please use the
'mcpName = "..."' property or the 'mcp_server_tool' format instead.
Hello from Gemini.
`
	got := extractGeminiVisibleAssistantText(input)
	if got != "Hello from Gemini." {
		t.Fatalf("visible assistant text = %q, want warning removed", got)
	}
}

func TestSanitizeGeminiStreamJSONContentFiltersWarningsWithoutManglingNormalText(t *testing.T) {
	input := "Before\n⚠  [ADMIN] Policy file warning in restrict-tools.toml:\nUnrecognized tool name\nRule #2: The \"__\" syntax for MCP tools is strictly deprecated. Please use the\n'mcpName = \"...\"' property or the 'mcp_server_tool' format instead.\nAfter\n"
	got := sanitizeGeminiStreamJSONContent(input)
	want := "Before\nAfter\n"
	if got != want {
		t.Fatalf("sanitized stream-json content = %q, want %q", got, want)
	}

	normal := "I am thinking through the plan and generating a concise answer.\n"
	if got := sanitizeGeminiStreamJSONContent(normal); got != normal {
		t.Fatalf("normal stream-json content changed: got %q, want %q", got, normal)
	}
}

func TestStripGeminiHistoricalAssistantTextRemovesPaneReplay(t *testing.T) {
	previous := `Hello! I'm your Workflow Builder agent. I'm currently in the testing
workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?`

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "full previous response before new answer",
			text: previous + "\nYes, I do! A message sequence is ordered.",
			want: "Yes, I do! A message sequence is ordered.",
		},
		{
			name: "suffix of previous response before new answer",
			text: `workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?
Yes, I do! A message sequence is ordered.`,
			want: "Yes, I do! A message sequence is ordered.",
		},
		{
			name: "only historical suffix",
			text: `workspace, where we have a regression test workflow designed to verify the
system's guardrails.
Would you like me to run it?`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripGeminiHistoricalAssistantText(tt.text, []string{previous})
			if got != tt.want {
				t.Fatalf("stripped = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripGeminiEchoedUserPromptKeepsAssistantAnswer(t *testing.T) {
	token := "REAL_GEMINI_TMUX_abc123"
	prompt := fmt.Sprintf(`This is a real Gemini CLI tmux contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Reply exactly:
saved %s`, token, token)
	visible := fmt.Sprintf(`Preserve input safely:
blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते
Reply exactly:
saved %s
YOLO Ctrl+Y                                                              |⌐■_■|
saved %s`, token, token, token)

	filtered := extractGeminiVisibleAssistantText(visible)
	got := stripGeminiEchoedUserPrompt(filtered, prompt)
	want := "saved " + token
	if got != want {
		t.Fatalf("stripped prompt = %q, want %q", got, want)
	}
}

func TestStripGeminiPaneReplayKeepsSecondTurnAnswer(t *testing.T) {
	token := "REAL_GEMINI_TMUX_abc123"
	firstPrompt := fmt.Sprintf(`This is a real Gemini CLI tmux contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Reply exactly:
saved %s`, token, token)
	firstAnswer := "saved " + token
	secondPrompt := "Reply exactly: SECOND_" + token + ". Do not mention the previous token."
	visible := fmt.Sprintf(`Preserve input safely:
blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते
Reply exactly:
saved %s
saved %s
token.
SECOND_%s`, token, token, token, token)

	got := stripGeminiEchoedUserPrompt(visible, firstPrompt)
	got = stripGeminiHistoricalAssistantText(got, []string{firstAnswer})
	got = stripGeminiLeadingPromptFragments(got, secondPrompt)
	want := "SECOND_" + token
	if got != want {
		t.Fatalf("stripped replay = %q, want %q", got, want)
	}
}

func TestGeminiIdleDetectionIgnoresAssistantProseAboutRunning(t *testing.T) {
	pane := `
✦ The prepare-test-fixtures step is now running in the background.
  I will wait for the automatic notification before proceeding.

                                                                ? for shortcuts
────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits                                                |⌐■_■|
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                            sandbox               /model
 /tmp/project                                      no sandbox   Auto (Gemini 3)
`
	if !hasGeminiReadyPrompt(pane) {
		t.Fatalf("ready prompt not detected")
	}
	if hasGeminiActivity(pane) {
		t.Fatalf("assistant prose containing running should not count as active TUI state")
	}
	if isGeminiTUILine("The prepare-test-fixtures step is now running in the background.") {
		t.Fatalf("assistant prose containing running should not be filtered as TUI chrome")
	}
}

func TestGeminiReadyPromptAcceptsV042StarPrompt(t *testing.T) {
	pane := `
▝▜▄     Gemini CLI v0.42.0
  ▗▟▀    Authenticated with gemini-api-key /auth

                                                                ? for shortcuts
────────────────────────────────────────────────────────────────────────────────
 YOLO Ctrl+Y                                                              |⌐■_■|
────────────────────────────────────────────────────────────────────────────────
 *   Type your message or @path/to/file
────────────────────────────────────────────────────────────────────────────────
 workspace (/directory)                      sandbox                     /model
 /private/var/folders/project                no sandbox   gemini-3.1-flash-lite
`
	if !hasGeminiReadyPrompt(pane) {
		t.Fatalf("v0.42 star prompt was not detected as ready")
	}
	if hasGeminiActivity(pane) {
		t.Fatalf("v0.42 star prompt should not count as active generation")
	}
}

func TestGeminiActiveStatusAbovePromptKeepsSessionActive(t *testing.T) {
	var filler strings.Builder
	for i := 0; i < 48; i++ {
		fmt.Fprintf(&filler, "tool output line %02d\n", i)
	}
	pane := `
✦ Running shell command via api-bridge

esc to interrupt
` + filler.String() + `
                                                                ? for shortcuts
────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits                                                |⌐■_■|
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                            sandbox               /model
 /tmp/project                                      no sandbox   Auto (Gemini 3)
`
	if !hasGeminiReadyPrompt(pane) {
		t.Fatalf("ready prompt not detected")
	}
	if !hasGeminiActivity(pane) {
		t.Fatalf("active status above long output should keep Gemini session active")
	}
}

func TestGeminiDetectsUnsubmittedV042Draft(t *testing.T) {
	pane := `
────────────────────────────────────────────────────────────────────────────────
 * Reply exactly: SECOND_REAL_GEMINI_TMUX_26e56245. Do not mention the
   previous token.

────────────────────────────────────────────────────────────────────────────────
 workspace (/directory)                      sandbox                     /model
 /private/var/folders/project                no sandbox   gemini-3.1-flash-lite
`
	if !hasGeminiUnsubmittedDraft(pane) {
		t.Fatalf("v0.42 draft prompt was not detected")
	}
	largePastePane := `
▝▜▄ Gemini CLI v0.42.0
────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits                                                |⌐■_■|
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > [Pasted Text: 61 lines]
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                      sandbox                     /model
 /tmp/project                                no sandbox          Auto (Gemini 3)
`
	if !hasGeminiUnsubmittedDraft(largePastePane) {
		t.Fatalf("large pasted draft prompt was not detected")
	}
	if hasGeminiReadyPrompt(largePastePane) {
		t.Fatalf("large pasted draft prompt should not be treated as ready")
	}
	readyPane := `
────────────────────────────────────────────────────────────────────────────────
 *   Type your message or @path/to/file
────────────────────────────────────────────────────────────────────────────────
`
	if hasGeminiUnsubmittedDraft(readyPane) {
		t.Fatalf("empty v0.42 ready prompt should not be treated as a draft")
	}
}

func TestGeminiCompletedMarkdownBulletsAreNotDraft(t *testing.T) {
	pane := `
✦ Investigation complete.

  Key Evidence
   1. RTS-FRONTEND-5
       * Impact: Extreme UI thread blocking during bootstrap.
       * Details: CSS bundle blocking for 5174 ms.

  STATUS: COMPLETED

────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits                                                |⌐■_■|
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                      sandbox                     /model
 /tmp/project                                no sandbox          Auto (Gemini 3)
`
	if hasGeminiUnsubmittedDraft(pane) {
		t.Fatalf("completed assistant markdown bullets should not be treated as an unsubmitted draft")
	}
}

func TestGeminiSubmittedPromptInScrollbackIsNotDraftWhenReady(t *testing.T) {
	pane := `
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > ## Orchestrator Instructions
   Do the task and create the output file.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀

✦ Done.

  STATUS: COMPLETED

────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits                                                |⌐■_■|
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                      sandbox                     /model
 /tmp/project                                no sandbox          Auto (Gemini 3)
`
	if hasGeminiUnsubmittedDraft(pane) {
		t.Fatalf("submitted prompt retained in scrollback should not be treated as current draft")
	}
}

func TestGeminiLiveInputQueueRoundTrip(t *testing.T) {
	session := &geminiInteractiveSession{}
	if err := enqueueGeminiLiveInput(session, "follow-up one"); err != nil {
		t.Fatalf("enqueue first live input: %v", err)
	}
	if err := enqueueGeminiLiveInput(session, "follow-up two\nwith newline"); err != nil {
		t.Fatalf("enqueue second live input: %v", err)
	}

	got, ok := popGeminiLiveInput(session)
	if !ok || got != "follow-up one" {
		t.Fatalf("first pop = %q, %v; want first queued message", got, ok)
	}
	got, ok = popGeminiLiveInput(session)
	if !ok || got != "follow-up two\nwith newline" {
		t.Fatalf("second pop = %q, %v; want second queued message", got, ok)
	}
	if got, ok = popGeminiLiveInput(session); ok || got != "" {
		t.Fatalf("third pop = %q, %v; want empty queue", got, ok)
	}
}

func TestExtractTextFromMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    llmtypes.MessageContent
		expected string
	}{
		{
			name: "Single text part",
			input: llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "Hello"},
				},
			},
			expected: "Hello",
		},
		{
			name: "Multiple text parts",
			input: llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "Hello"},
					llmtypes.TextContent{Text: "World"},
				},
			},
			expected: "Hello\nWorld",
		},
		{
			name: "Empty message",
			input: llmtypes.MessageContent{
				Role:  llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTextFromMessage(tt.input)
			if result != tt.expected {
				t.Errorf("extractTextFromMessage() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGeminiCLIRejectsImageContent(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-3.1-flash-lite", &MockLogger{})

	_, err := adapter.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Describe this image."},
				llmtypes.ImageContent{SourceType: "base64", MediaType: "image/png", Data: "iVBORw0KGgo="},
			},
		},
	})
	if err == nil {
		t.Fatal("GenerateContent() error = nil, want unsupported image content error")
	}
	if !strings.Contains(err.Error(), "does not support llmtypes.ImageContent") {
		t.Fatalf("GenerateContent() error = %v, want image content unsupported error", err)
	}
}

func TestMapResultToContentResponse(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	raw := map[string]interface{}{
		"type":       "result",
		"session_id": "test-session-123",
		"result":     "Hello world",
		"stats": map[string]interface{}{
			"input_tokens":  float64(100),
			"output_tokens": float64(50),
			"total_tokens":  float64(150),
		},
	}

	resp := adapter.mapResultToContentResponse(raw, "test-session-123", "", "Hello world", "")

	// Verify Content
	if len(resp.Choices) != 1 {
		t.Fatalf("Expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Content != "Hello world" {
		t.Errorf("Expected content 'Hello world', got '%s'", resp.Choices[0].Content)
	}

	// Verify Usage
	if resp.Usage.InputTokens != 100 {
		t.Errorf("Expected 100 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("Expected 50 output tokens, got %d", resp.Usage.OutputTokens)
	}
	if resp.Usage.TotalTokens != 150 {
		t.Errorf("Expected 150 total tokens, got %d", resp.Usage.TotalTokens)
	}

	// Verify GenerationInfo / Additional
	genInfo := resp.Choices[0].GenerationInfo
	if genInfo == nil {
		t.Fatal("GenerationInfo is nil")
	}
	if sid, ok := genInfo.Additional["gemini_session_id"].(string); !ok || sid != "test-session-123" {
		t.Errorf("Expected session ID 'test-session-123', got %v", genInfo.Additional["gemini_session_id"])
	}
}

func TestMapResultToContentResponse_UsageField(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	// Test with "usage" field instead of "stats"
	raw := map[string]interface{}{
		"type":   "result",
		"result": "Test response",
		"usage": map[string]interface{}{
			"input_tokens":  float64(200),
			"output_tokens": float64(100),
		},
	}

	resp := adapter.mapResultToContentResponse(raw, "session-456", "", "Test response", "")

	if resp.Usage.InputTokens != 200 {
		t.Errorf("Expected 200 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 100 {
		t.Errorf("Expected 100 output tokens, got %d", resp.Usage.OutputTokens)
	}
	// total_tokens should be computed as input + output when not provided
	if resp.Usage.TotalTokens != 300 {
		t.Errorf("Expected 300 total tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestMapResultToContentResponse_EmptyResult(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	raw := map[string]interface{}{
		"type":     "result",
		"response": "Fallback response text",
	}

	resp := adapter.mapResultToContentResponse(raw, "", "", "Fallback response text", "")

	if resp.Choices[0].Content != "Fallback response text" {
		t.Errorf("Expected fallback to 'response' field, got '%s'", resp.Choices[0].Content)
	}
}

func TestGetModelMetadata(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-flash", &MockLogger{})

	meta, err := adapter.GetModelMetadata("gemini-2.5-flash")
	if err != nil {
		t.Fatalf("GetModelMetadata() error = %v", err)
	}

	if meta.Provider != "gemini-cli" {
		t.Errorf("Expected provider 'gemini-cli', got '%s'", meta.Provider)
	}
	if meta.InputCostPer1MTokens != 0.30 {
		t.Errorf("Expected input cost 0.30, got %f", meta.InputCostPer1MTokens)
	}
	if meta.OutputCostPer1MTokens != 2.50 {
		t.Errorf("Expected output cost 2.50, got %f", meta.OutputCostPer1MTokens)
	}
}

func TestGetModelID(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-2.5-pro", &MockLogger{})
	if adapter.GetModelID() != "gemini-2.5-pro" {
		t.Errorf("Expected model ID 'gemini-2.5-pro', got '%s'", adapter.GetModelID())
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestGeminiCLISearchWebSmoke(t *testing.T) {
	if os.Getenv("RUN_GEMINI_CLI_REAL_E2E") == "" && os.Getenv("RUN_GEMINI_CLI_SEARCH_WEB_E2E") == "" {
		t.Skip("set RUN_GEMINI_CLI_SEARCH_WEB_E2E=1 to run real Gemini CLI web search test")
	}
	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, quietGeminiStreamLogger{})
	streamChan := make(chan llmtypes.StreamChunk, 128)
	captureDone := collectGeminiStream(streamChan)
	result, err := adapter.SearchWeb(ctx,
		"What is the capital of France? Use Google web search and reply with the city and country only.",
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("SearchWeb() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "paris") {
		t.Fatalf("expected result to mention Paris, got %q", result)
	}
	if capture := <-captureDone; capture.toolStarts == 0 {
		t.Fatalf("expected SearchWeb to emit a native Google web-search tool call, streamed content=%q", capture.content)
	}
}

type geminiDrainedStream struct {
	content        string
	terminalCount  int
	terminalSample string
}

func drainGeminiStream(streamChan <-chan llmtypes.StreamChunk) geminiDrainedStream {
	var parts []string
	var drained geminiDrainedStream
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				drained.content = strings.TrimSpace(strings.Join(parts, ""))
				return drained
			}
			switch chunk.Type {
			case llmtypes.StreamChunkTypeContent:
				parts = append(parts, chunk.Content)
			case llmtypes.StreamChunkTypeTerminal:
				drained.terminalCount++
				if drained.terminalSample == "" {
					drained.terminalSample = chunk.Content
				}
			}
		default:
			drained.content = strings.TrimSpace(strings.Join(parts, ""))
			return drained
		}
	}
}

func assertGeminiInteractiveTerminalOnlyStream(t *testing.T, streamChan <-chan llmtypes.StreamChunk) {
	t.Helper()
	drained := drainGeminiStream(streamChan)
	if drained.content != "" {
		t.Fatalf("interactive stream emitted assistant-content chunk %q; want terminal snapshots only", drained.content)
	}
	if drained.terminalCount == 0 {
		t.Fatalf("interactive stream emitted no terminal snapshots")
	}
}

func assertGeminiStreamQuality(t *testing.T, streamed, want string) {
	t.Helper()
	if !strings.Contains(streamed, want) {
		t.Fatalf("streamed content = %q, want assistant response containing %q", streamed, want)
	}
	for _, noisy := range []string{
		"Generating",
		"esc to cancel",
		"Type your message",
		"Gemini CLI",
		"Ctrl+O",
		"pasted text",
		"Waiting for MCP",
		"prompts will be queued",
		"api-bridge",
		"execute_shell_command",
		"exit_code",
	} {
		if strings.Contains(streamed, noisy) {
			t.Fatalf("streamed content = %q, should not contain TUI noise %q", streamed, noisy)
		}
	}
}
