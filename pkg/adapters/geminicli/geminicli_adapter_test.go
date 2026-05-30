package geminicli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/tmuxlaunch"
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

func TestGeminiTerminalStreamCapturesRawScreenRows(t *testing.T) {
	fakeBin := t.TempDir()
	argsPath := fakeBin + "/capture-args.log"
	tmuxPath := fakeBin + "/tmux"
	script := `#!/bin/sh
if [ "$1" = "capture-pane" ]; then
  printf '%s\n' "$*" > "$TMUX_TEST_CAPTURE_ARGS"
  for arg in "$@"; do
    if [ "$arg" = "-J" ]; then
      echo "terminal display capture must not use -J" >&2
      exit 9
    fi
  done
  printf 'screen row one\nscreen row two\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_TEST_CAPTURE_ARGS", argsPath)

	stream := make(chan llmtypes.StreamChunk, 1)
	var last string
	if !streamGeminiTerminalSnapshot(context.Background(), "raw-display-session", stream, &last) {
		t.Fatal("streamGeminiTerminalSnapshot returned false")
	}
	chunk := <-stream
	if chunk.Type != llmtypes.StreamChunkTypeTerminal {
		t.Fatalf("chunk type = %q, want terminal", chunk.Type)
	}
	if !strings.Contains(chunk.Content, "screen row one\nscreen row two") {
		t.Fatalf("chunk content = %q, want raw screen rows", chunk.Content)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read capture args: %v", err)
	}
	if strings.Contains(string(args), " -J") {
		t.Fatalf("terminal display capture used joined rows: %q", string(args))
	}
}

func TestGeminiInteractiveTimeoutDefaultsToNoDeadline(t *testing.T) {
	t.Setenv(EnvGeminiInteractiveTimeoutSeconds, "")
	if got := geminiInteractiveTimeout(); got != 0 {
		t.Fatalf("geminiInteractiveTimeout default = %v, want 0", got)
	}

	t.Setenv(EnvGeminiInteractiveTimeoutSeconds, "0")
	if got := geminiInteractiveTimeout(); got != 0 {
		t.Fatalf("geminiInteractiveTimeout zero env = %v, want 0", got)
	}

	t.Setenv(EnvGeminiInteractiveTimeoutSeconds, "2")
	if got := geminiInteractiveTimeout(); got != 2*time.Second {
		t.Fatalf("geminiInteractiveTimeout env = %v, want 2s", got)
	}
}

func TestGeminiInteractivePromptWaitDefaultsToStartupBudget(t *testing.T) {
	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "")
	t.Setenv(EnvGeminiInteractivePromptWaitSeconds, "")
	if got := geminiInteractivePromptWait(); got != 120*time.Second {
		t.Fatalf("geminiInteractivePromptWait default = %v, want 120s", got)
	}

	t.Setenv(tmuxlaunch.EnvPromptWaitSeconds, "3")
	t.Setenv(EnvGeminiInteractivePromptWaitSeconds, "")
	if got := geminiInteractivePromptWait(); got != 3*time.Second {
		t.Fatalf("geminiInteractivePromptWait global env = %v, want 3s", got)
	}

	t.Setenv(EnvGeminiInteractivePromptWaitSeconds, "2")
	if got := geminiInteractivePromptWait(); got != 2*time.Second {
		t.Fatalf("geminiInteractivePromptWait provider env = %v, want 2s", got)
	}
}

func TestGeminiProjectSettingsWithSafePaste(t *testing.T) {
	got, err := geminiProjectSettingsWithSafePaste(`{"mcpServers":{"api-bridge":{"command":"bridge"}},"ui":{"hideTips":true,"escapePastedAtSymbols":false}}`)
	if err != nil {
		t.Fatalf("geminiProjectSettingsWithSafePaste returned error: %v", err)
	}
	if !strings.Contains(got, `"escapePastedAtSymbols":true`) {
		t.Fatalf("settings did not force safe pasted @ handling: %s", got)
	}
	if !strings.Contains(got, `"hideTips":true`) {
		t.Fatalf("settings did not preserve existing ui keys: %s", got)
	}
	if !strings.Contains(got, `"mcpServers"`) || !strings.Contains(got, `"api-bridge"`) {
		t.Fatalf("settings did not preserve existing keys: %s", got)
	}
}

func TestGeminiWorkingDirKeepsProjectSettingsIsolated(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	workDir := filepath.Join(tmpDir, "shared-work")
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(" " + workDir + " ")(opts)
	WithProjectDirID("session-a")(opts)
	WithProjectSettings(`{"mcpServers":{"api-bridge":{"env":{"MCP_API_URL":"http://example.test/s/session-a"}}}}`)(opts)

	projectDir, projectDirID := resolveProjectDir(opts, "")
	if projectDir == workDir {
		t.Fatalf("project dir should be isolated from working dir, both were %q", workDir)
	}
	if projectDirID != "session-a" {
		t.Fatalf("project dir ID = %q, want session-a", projectDirID)
	}

	interactiveDir, interactiveDirID, err := prepareGeminiInteractiveProjectDir("owner-session", opts)
	if err != nil {
		t.Fatalf("prepareGeminiInteractiveProjectDir returned error: %v", err)
	}
	if interactiveDir != projectDir {
		t.Fatalf("interactive dir = %q, want isolated project dir %q", interactiveDir, projectDir)
	}
	if interactiveDirID != "session-a" {
		t.Fatalf("interactive project dir ID = %q, want session-a", interactiveDirID)
	}
	settingsPath := filepath.Join(interactiveDir, ".gemini", "settings.json")
	settingsBytes, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read isolated settings: %v", err)
	}
	if !strings.Contains(string(settingsBytes), "/s/session-a") {
		t.Fatalf("isolated settings did not contain session-a MCP URL: %s", settingsBytes)
	}
	if !strings.Contains(string(settingsBytes), `"escapePastedAtSymbols":true`) {
		t.Fatalf("isolated settings did not enable safe pasted @ handling: %s", settingsBytes)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".gemini", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("working dir settings should not be written; stat err=%v", err)
	}

	adapter := NewGeminiCLIAdapter("", geminiCLIContractModel, &MockLogger{})
	args, env, launchProjectDir, launchProjectDirID, commandDir, _, err := adapter.buildGeminiInteractiveLaunch("owner-session", opts, "")
	if err != nil {
		t.Fatalf("buildGeminiInteractiveLaunch returned error: %v", err)
	}
	if launchProjectDir != projectDir || launchProjectDirID != "session-a" {
		t.Fatalf("launch project dir/id = %q/%q, want %q/session-a", launchProjectDir, launchProjectDirID, projectDir)
	}
	if commandDir != projectDir {
		t.Fatalf("command dir = %q, want isolated project dir %q", commandDir, projectDir)
	}
	if !argPairContains(args, "--include-directories", workDir) {
		t.Fatalf("args did not include working dir %q via --include-directories: %#v", workDir, args)
	}
	if !envContains(env, "GEMINI_PROJECT_DIR="+projectDir) {
		t.Fatalf("env did not include isolated GEMINI_PROJECT_DIR=%q: %#v", projectDir, env)
	}

	other := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(other)
	WithProjectDirID("session-b")(other)
	WithProjectSettings(`{"mcpServers":{"api-bridge":{"env":{"MCP_API_URL":"http://example.test/s/session-b"}}}}`)(other)
	otherDir, _, err := prepareGeminiInteractiveProjectDir("other-owner", other)
	if err != nil {
		t.Fatalf("prepare other project dir: %v", err)
	}
	if otherDir == interactiveDir {
		t.Fatalf("parallel sessions with same working dir must use distinct project settings dirs")
	}
	otherSettings, err := os.ReadFile(filepath.Join(otherDir, ".gemini", "settings.json"))
	if err != nil {
		t.Fatalf("read other settings: %v", err)
	}
	if !strings.Contains(string(otherSettings), "/s/session-b") || strings.Contains(string(otherSettings), "/s/session-a") {
		t.Fatalf("other isolated settings contain wrong session URL: %s", otherSettings)
	}
}

func TestGeminiProjectDirAbsoluteOverridesTmpDefault(t *testing.T) {
	// When MetadataKeyProjectDirAbsolute is set, the adapter must use that
	// path directly instead of joining under os.TempDir(). This is the
	// workflow main_agent case where the project dir lives inside the
	// workflow folder (e.g. <workflow>/.gemini-main).
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	workDir := filepath.Join(tmpDir, "workflow-confida-login")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}
	absProjectDir := filepath.Join(workDir, ".gemini-main")

	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)
	WithProjectDirID("session-main")(opts)
	WithProjectDirAbsolute(absProjectDir)(opts)
	WithProjectSettings(`{"mcpServers":{}}`)(opts)

	interactiveDir, _, err := prepareGeminiInteractiveProjectDir("owner-session", opts)
	if err != nil {
		t.Fatalf("prepareGeminiInteractiveProjectDir: %v", err)
	}
	if interactiveDir != absProjectDir {
		t.Fatalf("project dir = %q, want absolute override %q", interactiveDir, absProjectDir)
	}
	// Ensure it actually lives inside the workflow dir, not /tmp/gemini-cli-project-*.
	if !strings.HasPrefix(interactiveDir, workDir) {
		t.Fatalf("project dir %q not rooted in workflow dir %q", interactiveDir, workDir)
	}
	if strings.Contains(interactiveDir, "gemini-cli-project-") {
		t.Fatalf("project dir %q still falling back to /tmp/gemini-cli-project- pattern", interactiveDir)
	}
	if _, err := os.Stat(filepath.Join(interactiveDir, ".gemini", "settings.json")); err != nil {
		t.Fatalf("expected .gemini/settings.json under override path: %v", err)
	}
}

func TestAppendGeminiIncludeWorkingDirSkipsWhenProjectIsDescendant(t *testing.T) {
	// Workflow main_agent: projectDir lives inside workingDir
	// (<workflow>/.gemini-main). Gemini's natural cwd → parent walk
	// already discovers GEMINI.md in <workflow>, so adding
	// --include-directories <workflow> would cause the SAME file to be
	// loaded twice (reported as "2 GEMINI.md files" in the context
	// summary). The arg must be skipped here.
	tmpDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(tmpDir)(opts)

	var args []string
	descendantProjectDir := filepath.Join(tmpDir, ".gemini-main")
	appendGeminiIncludeWorkingDirArg(&args, opts, descendantProjectDir)
	if argPairContains(args, "--include-directories", tmpDir) {
		t.Fatalf("expected --include-directories to be SKIPPED when projectDir is a descendant of workingDir; got args=%#v", args)
	}

	// Sanity check: when projectDir is OUTSIDE workingDir (the step
	// /tmp isolation case), --include-directories MUST still be added
	// so workspace files are reachable.
	args = nil
	outsideProjectDir := filepath.Join(t.TempDir(), "gemini-cli-project-step-1")
	appendGeminiIncludeWorkingDirArg(&args, opts, outsideProjectDir)
	if !argPairContains(args, "--include-directories", tmpDir) {
		t.Fatalf("expected --include-directories %q when projectDir %q is outside workingDir; got args=%#v", tmpDir, outsideProjectDir, args)
	}
}

func TestGeminiProjectDirAbsoluteIgnoredWhenRelative(t *testing.T) {
	// Defensive: a non-absolute path in MetadataKeyProjectDirAbsolute must
	// be ignored so callers can't accidentally write the project dir into
	// CWD or some unexpected location.
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	opts := &llmtypes.CallOptions{}
	WithProjectDirID("session-rel")(opts)
	WithProjectDirAbsolute("relative/path/.gemini-main")(opts)
	WithProjectSettings(`{}`)(opts)

	interactiveDir, _, err := prepareGeminiInteractiveProjectDir("owner-session", opts)
	if err != nil {
		t.Fatalf("prepareGeminiInteractiveProjectDir: %v", err)
	}
	if !strings.Contains(interactiveDir, "gemini-cli-project-session-rel") {
		t.Fatalf("relative override should have been ignored; got %q, expected default /tmp path", interactiveDir)
	}
}

func envContains(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func argPairContains(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
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
	}, true)

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

func TestBuildGeminiInteractivePromptUsesLatestOnlyWithNativeContext(t *testing.T) {
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
				llmtypes.TextContent{Text: "Run job search"},
			},
		},
	}, false)

	if prompt != "Run job search" {
		t.Fatalf("prompt = %q, want latest message only", prompt)
	}
	if strings.Contains(prompt, "Previous conversation context") || strings.Contains(prompt, token) {
		t.Fatalf("prompt leaked prior context despite native session context: %q", prompt)
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

func TestGeminiTerminalTailTextFallbackUsesLatestCleanLines(t *testing.T) {
	input := `▝▜▄ Gemini CLI v0.42.0
╭────────────────────╮
│ ✓ execute_shell_command │
╰────────────────────╯
older answer line
new answer line 1
new answer line 2
> Type your message or @path/to/file`

	got := geminiTerminalTailTextFallback(input, 2)
	if got != "new answer line 1\nnew answer line 2" {
		t.Fatalf("tail fallback = %q, want latest clean lines", got)
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

func TestParseGeminiInteractiveResponseKeepsMarkedAssistantTable(t *testing.T) {
	baseline := "Gemini ready\n>"
	captured := baseline + `
╭────────────────────────────────────────────────────────────────────────────────╮
│ ✓  execute_shell_command (api-bridge MCP Server) {"command":"find db"}        │
╰────────────────────────────────────────────────────────────────────────────────╯
✦ I've analyzed the workspace. Here is the current snapshot:

  ---

  1. Recent Discoveries
  From the last job search run, 5 jobs were saved:

  ┌───┬────────────────────────────┬───────────┬──────────┐
  │ # │ Job Title                  │ Fit Score │ Connects │
  ├───┼────────────────────────────┼───────────┼──────────┤
  │ 1 │ AI Implementation Engineer │ 13/16     │ 21       │
  └───┴────────────────────────────┴───────────┴──────────┘

  2. Active Browser State
  Upwork and Gmail tabs are open.

                                                                                  ? for shortcuts
────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits
────────────────────────────────────────────────────────────────────────────────
 >   Type your message or @path/to/file
 workspace (/directory)                                       sandbox /model
`

	got := parseGeminiInteractiveResponse(captured, baseline, "", nil)
	for _, want := range []string{
		"I've analyzed the workspace",
		"┌───┬────────────────────────────┬───────────┬──────────┐",
		"│ 1 │ AI Implementation Engineer │ 13/16     │ 21       │",
		"2. Active Browser State",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("parsed response missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Type your message", "workspace (/directory)", "Shift+Tab"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("parsed response included Gemini footer %q:\n%s", unwanted, got)
		}
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
	testcontracts.AssertCleanFinalExtraction(t, "gemini-cli", got,
		[]string{"Hello! I'm your Workflow Builder Agent", "I see 11 planned regression steps."},
		[]string{
			"Waiting for MCP",
			"prompts will be queued",
			"execute_shell_command",
			"api-bridge",
			"stdout",
			"exit_code",
		},
	)
}

func TestParseGeminiInteractiveResponseFiltersShellStartupWhenBaselineDiverges(t *testing.T) {
	baseline := `/Users/mipl/.zshrc:source:4: no such file or directory: /Users/mipl/powerlevel10k/powerlevel10k.zsh-theme
Agent pid 12345
Identity added: /Users/mipl/.ssh/id_ed25519 (user@example.com)
/Users/mipl/.zshrc:source:77: no such file or directory: /Users/mipl/esp/esp-idf/export.sh
> Type your message
`
	prompt := "Return a final answer containing these three plain lines and no setup commentary:\nGemini final LIVE_TOKEN\nfirst LIVE_TOKEN\nsecond LIVE_TOKEN"
	captured := `/Users/mipl/.zshrc:source:4: no such file or directory: /Users/mipl/powerlevel10k/powerlevel10k.zsh-theme
Agent pid 12345
Identity added: /Users/mipl/.ssh/id_ed25519 (user@example.com)
/Users/mipl/.zshrc:source:77: no such file or directory: /Users/mipl/esp/esp-idf/export.sh
> Return a final answer containing these three plain lines and no setup commentary:
Gemini final LIVE_TOKEN
first LIVE_TOKEN
second LIVE_TOKEN
> Type your message
`

	got := parseGeminiInteractiveResponse(captured, baseline, prompt, nil)
	want := "Gemini final LIVE_TOKEN\nfirst LIVE_TOKEN\nsecond LIVE_TOKEN"
	if got != want {
		t.Fatalf("parsed response = %q, want %q", got, want)
	}
	testcontracts.AssertCleanFinalExtraction(t, "gemini-cli", got,
		[]string{"Gemini final LIVE_TOKEN", "first LIVE_TOKEN", "second LIVE_TOKEN"},
		[]string{
			".zshrc:source",
			"Agent pid",
			"Identity added",
			"Return a final answer",
			"Type your message",
		},
	)
}

func TestGeminiFinalExtractionVertexJudgeE2E(t *testing.T) {
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
	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "gemini-cli",
		TmuxScreen: input,
		Extracted:  got,
		UserGoal:   "Introduce the workflow builder state after initialization.",
		MustContain: []string{
			"Hello! I'm your Workflow Builder Agent",
			"I see 11 planned regression steps.",
		},
		Forbidden: []string{
			"Waiting for MCP",
			"prompts will be queued",
			"execute_shell_command",
			"api-bridge",
			"stdout",
			"exit_code",
		},
		ExpectedNote: "The final answer should preserve the two clean assistant lines.",
	})
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

func TestGeminiInteractiveFatalErrorDetectsDebugConsoleCrash(t *testing.T) {
	input := `
╭────────────────────────────────────────────────────────╮
│ Debug Console (F12 to close)                           │
│ ✖ =========================================             │
│   This is an unexpected error. Please file a bug report │
│   CRITICAL: Unhandled Promise Rejection!                │
│   Reason: Error: ENAMETOOLONG: name too long, lstat     │
╰────────────────────────────────────────────────────────╯
`
	got := geminiInteractiveFatalError(input)
	if !strings.Contains(got, "Debug Console") || !strings.Contains(got, "ENAMETOOLONG") {
		t.Fatalf("geminiInteractiveFatalError() = %q, want debug console ENAMETOOLONG", got)
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

func TestHasGeminiActivityRecognizesToolCallInProgress(t *testing.T) {
	// Real failing pane from server_debug.log (2026-05-30):
	// gemini already accepted the Phase 2 prompt and started a tool call,
	// but the previous heuristic only looked for text-status markers
	// ("esc to cancel", "generating", etc.) which gemini does NOT render
	// while a tool is mid-execution. The adapter then retried submit 5x
	// and the whole turn failed even though gemini was working correctly.
	// The fix: recognize the '⊶' in-progress tool marker as activity.
	pane := `
is now fully verified.

  Phase 1 Result:
   - File: login_status.json (Copied to orchestrator folder)
   - Status: Verified

  I am proceeding to the next requested phase. Please provide the instruction for Phase 2.
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Phase 2: Discovery. Use sub-agents to gather data.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
╭────────────────────────────────────────────────────────────────────────────────╮
│ ⊶  execute_shell_command (api-bridge MCP Server) {"command":"curl -sS ..."     │
│                                                                                │
╰────────────────────────────────────────────────────────────────────────────────╯
`
	if !hasGeminiActivity(pane) {
		t.Fatalf("hasGeminiActivity must return true when a tool-call panel with ⊶ in-progress marker is present (otherwise the submit-detection heuristic falsely declares the prompt unsubmitted and the workflow aborts mid-turn)")
	}
}

func TestHasNewGeminiAssistantMarkerDetectsFastResponse(t *testing.T) {
	// Real failing pane from server_debug.log (2026-05-30):
	// the agent pasted an "Ack briefly" prompt; gemini answered with a
	// quick "✦ Acknowledged. Monitoring..." line and returned to the
	// ready prompt before the 100ms poll caught the in-flight state.
	// Without the new-assistant-marker check, waitForGeminiInputDraft
	// timed out even though the input had been received AND fully
	// processed.
	baseline := `▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)
`
	captured := `[workflow=Workflow/check-form-26as-xspaces] started. Ack briefly. Do NOT call tools.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 ✦ Acknowledged. Monitoring the Review Workflow Timing and Review Workflow Costs background agents.
─────────────────────────────────────────────────────────────
 Shift+Tab to accept edits
 2 GEMINI.md files · 1 MCP server
`
	if !hasNewGeminiAssistantMarker(captured, baseline) {
		t.Fatalf("captured pane contains a new ✦ marker; baseline has none — fast-response detector must return true")
	}
	if hasNewGeminiAssistantMarker(baseline, captured) {
		t.Fatalf("the reverse direction (going from response → ready) must NOT count as a new marker")
	}
	if hasNewGeminiAssistantMarker(captured, captured) {
		t.Fatalf("comparing a pane to itself must not flag new markers")
	}
	// Also verify '->' and '→' markers count.
	withArrow := "  -> Some response from gemini\n"
	withUnicode := "  → Another response\n"
	if !hasNewGeminiAssistantMarker(withArrow, "") {
		t.Fatalf("'->' assistant marker must be detected")
	}
	if !hasNewGeminiAssistantMarker(withUnicode, "") {
		t.Fatalf("'→' assistant marker must be detected")
	}
}

func TestHasGeminiActivityRejectsCompletedToolCall(t *testing.T) {
	// Sanity check: '✓' (completed) marker alone is NOT activity. Only
	// the in-progress '⊶' counts. The captured pane below shows a finished
	// tool call followed by a fresh ready prompt — should be quiet.
	pane := `
╭────────────────────────────────────────────────────────────────────────────────╮
│ ✓  execute_shell_command (api-bridge MCP Server) {"command":"ls"}              │
│                                                                                │
│ result: OK                                                                     │
╰────────────────────────────────────────────────────────────────────────────────╯

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                            sandbox               /model
 /tmp/project                                      no sandbox   Auto (Gemini 3)
`
	if hasGeminiActivity(pane) {
		t.Fatalf("completed tool call (✓) followed by ready prompt must NOT count as active — otherwise the next user input would be deferred forever")
	}
}

func TestGeminiTrustPromptAcceptsIncludedDirectoryPrompt(t *testing.T) {
	pane := `
 ▝▜▄     Gemini CLI v0.42.0
   ▗▟▀    Authenticated with gemini-api-key /auth

 ╭────────────────────────────────────────────────────────────────────────────╮
 │ Do you trust the following folders being added to this workspace?          │
 │ - /tmp/shared-workspace                                                   │
 │ Trusting a folder allows Gemini to read and perform auto-edits when in     │
 │ auto-approval mode.                                                       │
 │                                                                            │
 │ ● 1. Yes                                                                  │
 │   2. Yes, and remember the directories as trusted                          │
 │   3. No                                                                   │
 ╰────────────────────────────────────────────────────────────────────────────╯
`
	if !hasGeminiTrustPrompt(pane) {
		t.Fatalf("Gemini included-directory trust prompt was not detected")
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

func TestGeminiLeadingStatusCompletionIsPreserved(t *testing.T) {
	prompt := `Reply with STATUS: COMPLETED when done.`
	text := `STATUS: COMPLETED
Impact: done`

	got := stripGeminiLeadingPromptFragments(text, prompt)
	if !strings.Contains(got, "STATUS: COMPLETED") {
		t.Fatalf("stripGeminiLeadingPromptFragments() = %q, want STATUS preserved", got)
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

func TestGeminiCLIStructuredStreamMirrorsAssistantTextToTerminal(t *testing.T) {
	fakeBin := t.TempDir()
	geminiPath := filepath.Join(fakeBin, "gemini")
	script := `#!/bin/sh
printf '%s\n' '{"type":"init","session_id":"session-structured","model":"gemini-3"}'
printf '%s\n' '{"type":"message","role":"assistant","content":"{\"selected_route_id\":\"search\",\"reasoning\":\"assistant terminal mirror ok\"}"}'
printf '%s\n' '{"type":"result","session_id":"session-structured","status":"success"}'
`
	if err := os.WriteFile(geminiPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gemini: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := NewGeminiCLIAdapter("dummy-key", "auto", quietGeminiStreamLogger{})
	streamChan := make(chan llmtypes.StreamChunk, 32)
	resp, err := adapter.GenerateContent(context.Background(),
		[]llmtypes.MessageContent{llmtypes.TextParts(llmtypes.ChatMessageTypeHuman, "route this")},
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || !strings.Contains(resp.Choices[0].Content, "assistant terminal mirror ok") {
		t.Fatalf("unexpected response: %#v", resp)
	}

	var assistantContent strings.Builder
	var terminalContent strings.Builder
	for chunk := range streamChan {
		switch chunk.Type {
		case llmtypes.StreamChunkTypeContent:
			assistantContent.WriteString(chunk.Content)
		case llmtypes.StreamChunkTypeTerminal:
			terminalContent.WriteString(chunk.Content)
			terminalContent.WriteString("\n")
		}
	}
	if !strings.Contains(assistantContent.String(), "assistant terminal mirror ok") {
		t.Fatalf("assistant stream missing final text: %q", assistantContent.String())
	}
	if !strings.Contains(terminalContent.String(), "assistant terminal mirror ok") {
		t.Fatalf("terminal stream missing assistant text:\n%s", terminalContent.String())
	}
}

func TestMapResultToContentResponse(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-3.5-flash", &MockLogger{})

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
	adapter := NewGeminiCLIAdapter("", "gemini-3.5-flash", &MockLogger{})

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
	adapter := NewGeminiCLIAdapter("", "gemini-3.5-flash", &MockLogger{})

	raw := map[string]interface{}{
		"type":     "result",
		"response": "Fallback response text",
	}

	resp := adapter.mapResultToContentResponse(raw, "", "", "Fallback response text", "")

	if resp.Choices[0].Content != "Fallback response text" {
		t.Errorf("Expected fallback to 'response' field, got '%s'", resp.Choices[0].Content)
	}
}

// TestMapResultToContentResponse_FallsBackToResponseField guards the specific
// production case where Gemini emits its final text directly on the result
// event's `response` field with no streaming `content` events. Pre-fix the
// adapter only read accumulatedText and ignored raw["response"], so this
// turn would surface as "choice.Content is empty" upstream even though
// Gemini's response is right there on the wire.
func TestMapResultToContentResponse_FallsBackToResponseField(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-3.5-flash", &MockLogger{})

	raw := map[string]interface{}{
		"type":     "result",
		"response": "Final answer from result event",
		"status":   "success",
	}

	// accumulatedText is intentionally empty — simulates a turn where Gemini
	// only used tools (or otherwise skipped the streaming content channel)
	// and put the final text directly on the result event.
	resp := adapter.mapResultToContentResponse(raw, "session-abc", "gemini-3.5-flash", "", "")

	if resp == nil || len(resp.Choices) == 0 {
		t.Fatalf("expected non-empty response, got %+v", resp)
	}
	if resp.Choices[0].Content != "Final answer from result event" {
		t.Errorf("expected adapter to fall back to raw['response'] when accumulatedText is empty, got %q",
			resp.Choices[0].Content)
	}
}

// TestMapResultToContentResponse_AccumulatedTextWinsOverResponse confirms the
// fallback only kicks in when accumulatedText is empty. When the streaming
// channel produced text, that text is the source of truth even if the result
// event also carries a `response` field (which Gemini can include as a
// final summary).
func TestMapResultToContentResponse_AccumulatedTextWinsOverResponse(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-3.5-flash", &MockLogger{})

	raw := map[string]interface{}{
		"type":     "result",
		"response": "Should NOT be used",
		"status":   "success",
	}

	resp := adapter.mapResultToContentResponse(raw, "session-abc", "gemini-3.5-flash", "Streamed content from the model", "")

	if resp.Choices[0].Content != "Streamed content from the model" {
		t.Errorf("expected streamed accumulatedText to win, got %q", resp.Choices[0].Content)
	}
}

func TestGetModelMetadata(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-3.5-flash", &MockLogger{})

	meta, err := adapter.GetModelMetadata("gemini-3.5-flash")
	if err != nil {
		t.Fatalf("GetModelMetadata() error = %v", err)
	}

	if meta.Provider != "gemini-cli" {
		t.Errorf("Expected provider 'gemini-cli', got '%s'", meta.Provider)
	}
	if meta.InputCostPer1MTokens != 1.50 {
		t.Errorf("Expected input cost 1.50, got %f", meta.InputCostPer1MTokens)
	}
	if meta.OutputCostPer1MTokens != 9.00 {
		t.Errorf("Expected output cost 9.00, got %f", meta.OutputCostPer1MTokens)
	}
}

func TestResolveGeminiCLITierAliases(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{model: "high", want: "gemini-3.1-pro-preview"},
		{model: "medium", want: "gemini-3-flash-preview"},
		{model: "low", want: "gemini-3.1-flash-lite-preview"},
		{model: "gemini-3.1-pro-preview", want: "gemini-3.1-pro-preview"},
	}

	for _, tt := range tests {
		if got := resolveGeminiCLIModelID(tt.model); got != tt.want {
			t.Fatalf("resolveGeminiCLIModelID(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestGetModelID(t *testing.T) {
	adapter := NewGeminiCLIAdapter("", "gemini-3-pro-preview", &MockLogger{})
	if adapter.GetModelID() != "gemini-3-pro-preview" {
		t.Errorf("Expected model ID 'gemini-3-pro-preview', got '%s'", adapter.GetModelID())
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
