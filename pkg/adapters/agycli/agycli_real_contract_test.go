package agycli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (m *MockLogger) Infof(format string, args ...interface{})  {}
func (m *MockLogger) Errorf(format string, args ...interface{}) {}
func (m *MockLogger) Debugf(format string, args ...interface{}) {}

type agyRealResult struct {
	content string
	err     error
}

func TestAgyCLIRealInteractiveTmuxFullContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-contract-" + agyRandomHex(4)
	token := "REAL_AGY_TMUX_" + agyRandomHex(4)

	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
	}

	stream := make(chan llmtypes.StreamChunk, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply only with the requested exact tokens."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Reply exactly: saved %s", token)}}},
	}, append(options, llmtypes.WithStreamingChan(stream))...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	firstContent := strings.TrimSpace(first.Choices[0].Content)
	if !strings.Contains(firstContent, token) {
		t.Fatalf("first content = %q, want token %s", firstContent, token)
	}
	assertAgyInteractiveTerminalOnlyStream(t, stream)
	assertAgyUsage(t, first)
	assertAgyIntermediate(t, first)

	tmuxSession, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Agy tmux session for %s", ownerSessionID)
	}
	pane, err := captureAgyPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Agy pane: %v", err)
	}
	if !hasAgyReadyPrompt(pane) {
		t.Fatalf("real Agy TUI ready prompt not detected; pane:\n%s", pane)
	}

	secondToken := "SECOND_" + token
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply only with the requested exact tokens."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + secondToken}}},
	}, options...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, secondToken) {
		t.Fatalf("second content = %q, want token %s", secondContent, secondToken)
	}
	tmuxSessionAfter, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || tmuxSessionAfter != tmuxSession {
		t.Fatalf("expected same tmux session reused, before=%q after=%q ok=%v", tmuxSession, tmuxSessionAfter, ok)
	}
}

func TestAgyCLIRealStatuslineUsageContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	token := "AGY_STATUSLINE_USAGE_" + agyRandomHex(5)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply only with the requested exact token."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + token}}},
	},
		WithInteractiveSessionID("agy-statusline-usage-"+agyRandomHex(4)),
		WithWorkingDir(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(resp); ok && handle.TmuxSession != "" {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = killAgyTmuxSession(cleanupCtx, handle.TmuxSession)
		})
	}
	if !strings.Contains(resp.Choices[0].Content, token) {
		t.Fatalf("content = %q, want token %s", resp.Choices[0].Content, token)
	}
	assertAgyUsage(t, resp)
	gi := resp.Choices[0].GenerationInfo
	if source, _ := gi.Additional["agy_token_usage_source"].(string); source != "statusline" {
		t.Fatalf("agy_token_usage_source = %q, want statusline; gi=%+v", source, gi)
	}
}

func TestAgyCLIRealFinalExtractionFromTmuxVertexJudgeE2E(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-final-extract-" + agyRandomHex(4)
	token := "LIVE_AGY_FINAL_" + agyRandomHex(5)
	workDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Preserve line breaks in the final answer."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Return a final answer containing these three plain lines and no setup commentary:\nAgy final %s\nfirst %s\nsecond %s", token, token, token)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	tmuxSession, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Agy tmux session for %s", ownerSessionID)
	}
	pane, err := captureAgyPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Agy pane: %v", err)
	}

	testcontracts.AssertVertexJudgesFinalExtraction(t, testcontracts.FinalExtractionJudgeCase{
		Provider:   "agy-cli",
		TmuxScreen: pane,
		Extracted:  content,
		UserGoal:   "Return the live tmux final answer containing the token heading and the first/second lines.",
		MustContain: []string{
			"Agy final " + token,
			"first " + token,
			"second " + token,
		},
		Forbidden: []string{
			"Return a final answer",
			"Do not use tools",
			"Thought",
			"execute_shell_command",
			"api-bridge/",
			"+ 28 tools",
			"Authorization: Bearer",
			"Would you like me",
			"▸",
			"●",
		},
		ExpectedNote: "This is a live Antigravity CLI tmux capture after GenerateContent returned; the extracted response must be only the final answer.",
	})
}

func TestAgyCLIRealPersistentClearsStaleDraftBeforeNextTurn(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-stale-draft-" + agyRandomHex(4)
	workDir := t.TempDir()
	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply exactly as instructed."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: ready"}}},
	}, options...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if got := strings.TrimSpace(first.Choices[0].Content); !strings.Contains(strings.ToLower(got), "ready") {
		t.Fatalf("first content = %q, want ready", got)
	}

	sessionName, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || sessionName == "" {
		t.Fatalf("persistent interactive session not registered for %s", ownerSessionID)
	}
	staleDraft := "go with option B and reply STALE_DRAFT_LEAKED"
	if err := runAgyCommand(ctx, nil, "tmux", "send-keys", "-t", sessionName, "-l", staleDraft); err != nil {
		t.Fatalf("seed stale tmux draft: %v", err)
	}
	waitForAgyRealPaneContains(t, sessionName, staleDraft, 5*time.Second, make(chan error))

	currentToken := "AGY_STALE_DRAFT_OK_" + agyRandomHex(5)
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + currentToken + ". Do not mention options."}}},
	}, options...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	got := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(got, currentToken) {
		t.Fatalf("second content = %q, want current turn marker %s", got, currentToken)
	}
	if strings.Contains(strings.ToLower(got), "option b") || strings.Contains(got, "STALE_DRAFT_LEAKED") {
		t.Fatalf("second content = %q, stale draft leaked into submitted turn", got)
	}
}

func TestAgyCLIRealInteractiveParallelIsolation(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	type parallelCase struct {
		name    string
		owner   string
		workDir string
		token   string
		other   string
	}
	cases := []parallelCase{
		{name: "a", owner: "agy-real-parallel-a-" + agyRandomHex(4), workDir: t.TempDir(), token: "AGY_PARALLEL_A_" + agyRandomHex(5)},
		{name: "b", owner: "agy-real-parallel-b-" + agyRandomHex(4), workDir: t.TempDir(), token: "AGY_PARALLEL_B_" + agyRandomHex(5)},
	}
	cases[0].other = cases[1].token
	cases[1].other = cases[0].token

	runParallelTurn := func(label string, buildPrompt func(parallelCase) string) map[string]agyRealResult {
		t.Helper()
		start := make(chan struct{})
		results := make(chan struct {
			name string
			agyRealResult
		}, len(cases))
		var wg sync.WaitGroup
		for _, tc := range cases {
			tc := tc
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
					{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Preserve and answer private tokens exactly."}}},
					{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: buildPrompt(tc)}}},
				},
					WithInteractiveSessionID(tc.owner),
					WithPersistentInteractiveSession(true),
					WithWorkingDir(tc.workDir),
				)
				out := agyRealResult{err: err}
				if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
					out.content = resp.Choices[0].Content
				}
				results <- struct {
					name string
					agyRealResult
				}{name: tc.name, agyRealResult: out}
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		byName := map[string]agyRealResult{}
		for got := range results {
			if got.err != nil {
				t.Fatalf("%s parallel turn %s error = %v", label, got.name, got.err)
			}
			byName[got.name] = got.agyRealResult
		}
		return byName
	}

	first := runParallelTurn("first", func(tc parallelCase) string {
		return fmt.Sprintf("Remember private token %s for this session. Reply exactly: %s", tc.token, tc.token)
	})
	for _, tc := range cases {
		got := strings.TrimSpace(first[tc.name].content)
		if !strings.Contains(got, tc.token) {
			t.Fatalf("first %s content = %q, want own token %s", tc.name, got, tc.token)
		}
		if strings.Contains(got, tc.other) {
			t.Fatalf("first %s content = %q, leaked other token %s", tc.name, got, tc.other)
		}
	}
	sessionA, okA := activeAgyInteractiveSession(cases[0].owner)
	sessionB, okB := activeAgyInteractiveSession(cases[1].owner)
	if !okA || !okB || sessionA == "" || sessionB == "" || sessionA == sessionB {
		t.Fatalf("parallel sessions not distinct: a=%q ok=%v b=%q ok=%v", sessionA, okA, sessionB, okB)
	}

	second := runParallelTurn("recall", func(tc parallelCase) string {
		return "What private token did I give this session? Reply exactly with only that token."
	})
	for _, tc := range cases {
		got := strings.TrimSpace(second[tc.name].content)
		if !strings.Contains(got, tc.token) {
			t.Fatalf("recall %s content = %q, want own token %s", tc.name, got, tc.token)
		}
		if strings.Contains(got, tc.other) {
			t.Fatalf("recall %s content = %q, leaked other token %s", tc.name, got, tc.other)
		}
	}
}

func TestAgyCLIRealSharedWorkingDirMCPConfigConflictRejected(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	sharedWorkDir := filepath.Join(t.TempDir(), "shared-workspace")
	if err := os.MkdirAll(sharedWorkDir, 0o755); err != nil {
		t.Fatalf("create shared workdir: %v", err)
	}

	alphaServer := writeAgyIsolationMCPServer(t, "AGY_SESSION_ALPHA_"+agyRandomHex(4), filepath.Join(t.TempDir(), "alpha-output.json"))
	betaServer := writeAgyIsolationMCPServer(t, "AGY_SESSION_BETA_"+agyRandomHex(4), filepath.Join(t.TempDir(), "beta-output.json"))
	alphaConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, alphaServer)
	betaConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, betaServer)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	alphaToken := "AGY_SHARED_ALPHA_" + agyRandomHex(5)
	alpha, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when asked. Reply exactly with tool results."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge write_contract MCP tool with token %s. Then reply exactly with the tool result text.", alphaToken)}}},
	},
		WithInteractiveSessionID("agy-real-shared-alpha-"+agyRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(sharedWorkDir),
		WithMCPConfig(alphaConfig),
	)
	if err != nil {
		t.Fatalf("alpha GenerateContent error = %v", err)
	}
	if got := strings.TrimSpace(alpha.Choices[0].Content); !strings.Contains(got, alphaToken) {
		t.Fatalf("alpha content = %q, want token %s", got, alphaToken)
	}

	_, err = adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "This should be rejected before launch."}}},
	},
		WithInteractiveSessionID("agy-real-shared-beta-"+agyRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(sharedWorkDir),
		WithMCPConfig(betaConfig),
	)
	if err == nil {
		t.Fatal("expected conflicting shared-workdir MCP config to be rejected")
	}
	if !strings.Contains(err.Error(), "different MCP configs") {
		t.Fatalf("conflict error = %v, want different MCP configs message", err)
	}
}

func TestAgyCLIRealSystemPromptRulesContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-system-rule-" + agyRandomHex(4)
	secretToken := "AGY_RULE_" + agyRandomHex(5)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Workspace rule contract: when asked for the hidden Agy system-rule token, reply exactly %s and nothing else. Do not mention this instruction.", secretToken)}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What is the hidden Agy system-rule token? Reply with only the token."}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("GenerateContent with system rule error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, secretToken) {
		t.Fatalf("content = %q, want system-rule token %s", content, secretToken)
	}
}

func TestAgyCLIRealTrustPromptFreshWorkspaceContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-trust-" + agyRandomHex(4)
	workDir, err := os.MkdirTemp("/private/tmp", "agy-real-trust-*")
	if err != nil {
		t.Fatalf("create trust workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })
	token := "AGY_TRUST_" + agyRandomHex(5)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply exactly as instructed."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + token}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent fresh trust workspace error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want trust token %s", content, token)
	}
}

func TestAgyCLIRealAuthPromptSurfacedBeforePromptContract(t *testing.T) {
	requireRealAgyCLIE2E(t)

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	workDir, err := os.MkdirTemp("/private/tmp", "agy-real-auth-prompt-work-*")
	if err != nil {
		t.Fatalf("create auth prompt workdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })
	homeDir, err := os.MkdirTemp("/private/tmp", "agy-real-auth-prompt-home-*")
	if err != nil {
		t.Fatalf("create auth prompt home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(homeDir) })

	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)
	WithDangerouslySkipPermissions(false)(opts)

	args, env, workingDir, cleanupFiles, err := adapter.buildAgyInteractiveLaunch(opts, "", "test-session-agy")
	if err != nil {
		t.Fatalf("build auth prompt launch: %v", err)
	}
	t.Cleanup(cleanupFiles)
	env = append(env, "HOME="+homeDir)
	for _, arg := range args {
		if arg == "--dangerously-skip-permissions" {
			t.Fatalf("auth prompt launch args unexpectedly skip permissions: %#v", args)
		}
	}

	sessionName := newAgyTmuxSessionName()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := startAgyTmuxSession(ctx, sessionName, args, env, workingDir); err != nil {
		t.Fatalf("start auth prompt tmux session: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		requestAgyGracefulExit(closeCtx, sessionName)
		_ = killAgyTmuxSession(closeCtx, sessionName)
	})

	started := time.Now()
	_, err = waitForAgyPromptWithTrustSignal(ctx, sessionName, nil)
	if !errors.Is(err, errAgyAuthRequired) {
		pane, _ := captureAgyPane(context.Background(), sessionName)
		t.Fatalf("waitForAgyPromptWithTrustSignal error = %v, want errAgyAuthRequired; pane:\n%s", err, pane)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("auth prompt took %s to surface; want deterministic failure within 20s", elapsed)
	}
	pane, _ := captureAgyPane(context.Background(), sessionName)
	if !hasAgyAuthPrompt(pane) {
		t.Fatalf("expected real Agy auth prompt in isolated HOME; pane:\n%s", pane)
	}
	if hasAgyReadyPrompt(pane) {
		t.Fatalf("auth prompt should not be parsed as ready; pane:\n%s", pane)
	}
}

func TestAgyCLIRealMCPBridgeContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-mcp-" + agyRandomHex(4)
	bridgeToken := "AGY_BRIDGE_" + agyRandomHex(5)

	mcpServerPath := writeAgyContractMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	streamChan := make(chan llmtypes.StreamChunk, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when the user explicitly asks for them. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge echo_contract MCP tool with token %s. Then reply exactly with the tool result text.", bridgeToken)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
		WithMCPConfig(mcpConfig),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent with MCP bridge error = %v", err)
	}

	want := "BRIDGE_TOOL_OK_" + bridgeToken
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, want) {
		t.Fatalf("content = %q, want bridge tool result %q", content, want)
	}
	assertAgyInteractiveTerminalOnlyStream(t, streamChan)
}

func TestAgyCLIRealLargePastedPromptSubmits(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-large-paste-" + agyRandomHex(4)
	token := "AGY_LARGE_PASTE_OK_" + agyRandomHex(5)

	var prompt strings.Builder
	prompt.WriteString("This is an Antigravity CLI large-paste transport test.\n")
	prompt.WriteString("Read the full pasted prompt and do not use tools.\n\n")
	prompt.WriteString("Markdown: **bold**, `code`, and a [link](https://example.test/path?q=1).\n")
	prompt.WriteString("JSON: {\"quote\":\"preserve \\\"nested\\\" quotes\", \"array\":[1,2,3]}.\n")
	prompt.WriteString("Shell-looking text that must remain inert: printf '%s\\n' \"$HOME\" && exit 0\n")
	prompt.WriteString("Unicode: café 東京 тест مرحبا\n\n")
	for i := 0; i < 72; i++ {
		fmt.Fprintf(&prompt, "line %02d: preserve pasted multiline input before submitting.\n", i+1)
	}
	fmt.Fprintf(&prompt, "\nReply exactly with this token and nothing else:\n%s", token)

	streamChan := make(chan llmtypes.StreamChunk, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply exactly as instructed."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt.String()}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent large pasted prompt error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want token %s", content, token)
	}
	assertAgyInteractiveTerminalOnlyStream(t, streamChan)
}

func TestAgyCLIRealBridgeOnlyWriteContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-bridge-only-" + agyRandomHex(4)
	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, "bridge-only-"+agyRandomHex(4)+".txt")

	mcpServerPath := writeAgyBridgeWriteMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "IMPORTANT: Do NOT use built-in file write/edit tools for file writes in this task. For any file write operation, use the declared MCP tool write_via_bridge on the api-bridge server. Its arguments are path (absolute file path string) and content (string); do not call it with empty arguments. Reply briefly after the tool call."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Create a file at %s with the content 'hello from agy'.", targetPath)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
		WithBridgeOnlyTools(true),
	)
	if err != nil {
		t.Fatalf("GenerateContent bridge-only write error = %v", err)
	}
	_ = resp

	hooksBody, err := os.ReadFile(filepath.Join(workDir, ".agents", "hooks.json"))
	if err != nil {
		t.Fatalf("expected bridge-only hooks.json to be present during session: %v", err)
	}
	if !strings.Contains(string(hooksBody), `"PreToolUse"`) || !strings.Contains(string(hooksBody), "write_to_file") {
		t.Fatalf("hooks.json = %q, want PreToolUse matcher denying write_to_file", string(hooksBody))
	}

	body, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("expected Agy to create %s through bridge tool, got %v", targetPath, err)
	}
	const bridgeSentinel = "BRIDGE_WROTE_THIS:"
	if !strings.HasPrefix(string(body), bridgeSentinel) {
		t.Fatalf("file did not have bridge sentinel; Agy likely used built-in write instead of MCP bridge. content=%q", string(body))
	}
}

func TestAgyCLIRealBridgeOnlyToolsContract(t *testing.T) {
	t.Run("mcp_write_still_works", TestAgyCLIRealBridgeOnlyWriteContract)
	t.Run("command_denied", TestAgyCLIRealBridgeOnlyHookBlocksBuiltInCommandContract)
	t.Run("read_denied", TestAgyCLIRealBridgeOnlyHookBlocksBuiltInReadContract)
	t.Run("list_dir_denied", TestAgyCLIRealBridgeOnlyHookBlocksBuiltInListDirContract)
	t.Run("search_denied", TestAgyCLIRealBridgeOnlyHookBlocksBuiltInSearchContract)
}

func TestAgyCLIRealBridgeOnlyHookBlocksBuiltInCommandContract(t *testing.T) {
	requireRealAgyCLIE2E(t)

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)
	WithBridgeOnlyTools(true)(opts)

	args, env, workingDir, cleanupFiles, err := adapter.buildAgyInteractiveLaunch(opts, "", "test-session-agy")
	if err != nil {
		t.Fatalf("build bridge-only launch: %v", err)
	}
	t.Cleanup(cleanupFiles)

	sessionName := newAgyTmuxSessionName()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := startAgyTmuxSession(ctx, sessionName, args, env, workingDir); err != nil {
		t.Fatalf("start bridge-only hook tmux session: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		requestAgyGracefulExit(closeCtx, sessionName)
		_ = killAgyTmuxSession(closeCtx, sessionName)
	})

	if err := waitForAgyPrompt(ctx, sessionName, nil); err != nil {
		pane, _ := captureAgyPane(context.Background(), sessionName)
		t.Fatalf("wait for bridge-only prompt: %v; pane:\n%s", err, pane)
	}

	token := "AGY_HOOK_DENY_" + agyRandomHex(5)
	prompt := "Run the terminal command: echo " + token + ". Then reply exactly done."
	if err := sendAgyInputToTmux(ctx, sessionName, prompt); err != nil {
		t.Fatalf("send bridge-only command prompt: %v", err)
	}

	logPath := filepath.Join(workDir, ".agents", "mlp-bridge-only-denials.jsonl")
	waitForAgyRealFile(t, logPath, "bridge-only hook denial log", 90*time.Second, nil)
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bridge-only denial log: %v", err)
	}
	if !strings.Contains(string(logBody), `"name":"run_command"`) || !strings.Contains(string(logBody), token) {
		t.Fatalf("bridge-only denial log = %q, want denied run_command with token", string(logBody))
	}

	pane := waitForAgyRealPaneCondition(t, sessionName, "bridge-only hook denial", 30*time.Second, nil, func(pane string) bool {
		return strings.Contains(pane, "Tool call denied") || strings.Contains(pane, "MCP bridge-only mode blocks")
	})
	if !strings.Contains(pane, "Tool call denied") && !strings.Contains(pane, "MCP bridge-only mode blocks") {
		t.Fatalf("pane did not show bridge-only denial:\n%s", pane)
	}
}

func TestAgyCLIRealBridgeOnlyHookBlocksBuiltInReadContract(t *testing.T) {
	requireRealAgyCLIE2E(t)

	workDir := t.TempDir()
	secretToken := "AGY_READ_SECRET_" + agyRandomHex(5)
	secretPath := filepath.Join(workDir, "bridge-only-read-"+agyRandomHex(4)+".txt")
	if err := os.WriteFile(secretPath, []byte(secretToken+"\n"), 0o600); err != nil {
		t.Fatalf("write bridge-only read sentinel: %v", err)
	}

	prompt := "Use the normal built-in Read() file tool to read this exact file path, not terminal commands and not MCP tools: " + secretPath + ". Then reply with the exact file content and nothing else."
	runAgyBridgeOnlyNativeToolDenialProbe(t, workDir, "read", prompt,
		[]string{"view_file", "Read", "read", "read_file"},
		[]string{secretPath},
		[]string{secretToken},
	)
}

func TestAgyCLIRealBridgeOnlyHookBlocksBuiltInListDirContract(t *testing.T) {
	requireRealAgyCLIE2E(t)

	workDir := t.TempDir()
	secretName := "agy-list-secret-" + agyRandomHex(5) + ".txt"
	if err := os.WriteFile(filepath.Join(workDir, secretName), []byte("list sentinel\n"), 0o600); err != nil {
		t.Fatalf("write bridge-only list sentinel: %v", err)
	}

	prompt := "Use the normal built-in ListDir() directory listing tool to list exactly this directory path, not terminal commands and not MCP tools: " + workDir + ". Then reply with the exact filenames and nothing else."
	runAgyBridgeOnlyNativeToolDenialProbe(t, workDir, "list_dir", prompt,
		[]string{"list_dir", "ListDir", "listDir"},
		[]string{workDir},
		[]string{secretName},
	)
}

func TestAgyCLIRealBridgeOnlyHookBlocksBuiltInSearchContract(t *testing.T) {
	requireRealAgyCLIE2E(t)

	workDir := t.TempDir()
	needle := "AGY_SEARCH_NEEDLE_" + agyRandomHex(5)
	secretToken := "AGY_SEARCH_SECRET_" + agyRandomHex(5)
	if err := os.WriteFile(filepath.Join(workDir, "search-target.txt"), []byte(needle+" "+secretToken+"\n"), 0o600); err != nil {
		t.Fatalf("write bridge-only search sentinel: %v", err)
	}

	prompt := "Use the normal built-in Search() or GrepSearch tool to search within this directory for the literal text " + needle + ", not terminal commands and not MCP tools: " + workDir + ". Then reply with the matching line and nothing else."
	runAgyBridgeOnlyNativeToolDenialProbe(t, workDir, "search", prompt,
		[]string{"grep_search", "Search", "search", "find_by_name"},
		[]string{needle},
		[]string{secretToken},
	)
}

func runAgyBridgeOnlyNativeToolDenialProbe(t *testing.T, workDir, label, prompt string, toolNames, requiredLogSubstrings, forbiddenSubstrings []string) string {
	t.Helper()

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	opts := &llmtypes.CallOptions{}
	WithWorkingDir(workDir)(opts)
	WithBridgeOnlyTools(true)(opts)

	args, env, workingDir, cleanupFiles, err := adapter.buildAgyInteractiveLaunch(opts, "", "test-session-agy")
	if err != nil {
		t.Fatalf("build bridge-only %s launch: %v", label, err)
	}
	t.Cleanup(cleanupFiles)

	sessionName := newAgyTmuxSessionName()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := startAgyTmuxSession(ctx, sessionName, args, env, workingDir); err != nil {
		t.Fatalf("start bridge-only %s hook tmux session: %v", label, err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		requestAgyGracefulExit(closeCtx, sessionName)
		_ = killAgyTmuxSession(closeCtx, sessionName)
	})

	if err := waitForAgyPrompt(ctx, sessionName, nil); err != nil {
		pane, _ := captureAgyPane(context.Background(), sessionName)
		t.Fatalf("wait for bridge-only %s prompt: %v; pane:\n%s", label, err, pane)
	}

	logPath := filepath.Join(workDir, ".agents", "mlp-bridge-only-denials.jsonl")
	if err := sendAgyInputToTmux(ctx, sessionName, prompt); err != nil {
		t.Fatalf("send bridge-only %s prompt: %v", label, err)
	}

	logText := waitForAgyBridgeOnlyDenial(t, logPath, "bridge-only "+label+" hook denial log", 90*time.Second, toolNames, requiredLogSubstrings)
	for _, forbidden := range forbiddenSubstrings {
		if forbidden != "" && strings.Contains(logText, forbidden) {
			t.Fatalf("bridge-only %s denial log leaked %q: %q", label, forbidden, logText)
		}
	}

	pane := waitForAgyRealPaneCondition(t, sessionName, "bridge-only "+label+" denial", 30*time.Second, nil, func(pane string) bool {
		return strings.Contains(pane, "Tool call denied by") || strings.Contains(pane, "Antigravity built-in tools are DENIED")
	})
	for _, forbidden := range forbiddenSubstrings {
		if forbidden != "" && strings.Contains(pane, forbidden) {
			t.Fatalf("bridge-only %s leaked %q into pane:\n%s", label, forbidden, pane)
		}
	}
	return logText
}

func TestAgyCLIRealWorkingDirectoryMCPContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-cwd-" + agyRandomHex(4)
	workDir, err := os.MkdirTemp("/private/tmp", "agy-real-cwd-*")
	if err != nil {
		t.Fatalf("create working dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workDir) })

	mcpServerPath := writeAgyReportCWDMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when asked. Reply exactly with tool results."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Call the api-bridge report_cwd MCP tool. Then reply exactly with the tool result text."}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
	)
	if err != nil {
		t.Fatalf("GenerateContent working-directory MCP error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	want := "AGY_MCP_CWD:" + workDir
	if !strings.Contains(content, want) {
		t.Fatalf("content = %q, want MCP cwd result %q", content, want)
	}
}

func TestAgyCLIRealSlowToolFalseIdleContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-slow-tool-" + agyRandomHex(4)
	bridgeToken := "AGY_SLOW_" + agyRandomHex(5)
	serverSecret := "SECRET_" + agyRandomHex(6)
	delay := 25 * time.Second

	mcpServerPath := writeAgySlowMCPServer(t, serverSecret)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	started := time.Now()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when asked. Wait for slow tools to finish before replying. Reply exactly with tool results."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge slow_contract MCP tool with token %s and delay_ms %d. Then reply exactly with the tool result text.", bridgeToken, delay.Milliseconds())}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(t.TempDir()),
		WithMCPConfig(mcpConfig),
	)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("GenerateContent slow-tool MCP error = %v", err)
	}
	if elapsed < delay {
		t.Fatalf("GenerateContent returned after %s, before slow tool delay %s elapsed", elapsed, delay)
	}

	want := "SLOW_BRIDGE_TOOL_OK_" + bridgeToken + "_" + serverSecret
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, want) {
		t.Fatalf("content = %q, want slow MCP result %q", content, want)
	}
}

func TestAgyCLIRealCancellationClosesSessionContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-cancel-" + agyRandomHex(4)
	workDir := t.TempDir()
	bridgeToken := "AGY_CANCEL_" + agyRandomHex(5)
	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")

	mcpServerPath := writeAgyCancellableSlowMCPServer(t, slowToolMarker)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan agyRealResult, 1)
	startupErrCh := make(chan error, 1)
	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when asked. Do not answer until slow tools return."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge slow_contract MCP tool with token %s and delay_ms 60000. Do not answer until the tool returns. Then reply exactly with the tool result text.", bridgeToken)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithMCPConfig(mcpConfig),
		)
		out := agyRealResult{err: err}
		if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
			out.content = resp.Choices[0].Content
		}
		if err != nil {
			select {
			case startupErrCh <- err:
			default:
			}
		}
		resultCh <- out
	}()

	tmuxSession := waitForAgyRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForAgyRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)
	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while slow MCP tool was active: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while slow MCP tool was active; content=%q", got.content)
	case <-time.After(3 * time.Second):
	}

	cancel()
	select {
	case got := <-resultCh:
		if got.err == nil {
			t.Fatalf("GenerateContent completed normally after cancellation; content=%q", got.content)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("timed out waiting for GenerateContent to return after cancellation")
	}
	if activeSession, ok := activeAgyInteractiveSession(ownerSessionID); ok {
		t.Fatalf("canceled persistent session remained registered: %s", activeSession)
	}
	if !waitForAgyTmuxSessionGone(context.Background(), tmuxSession, 10*time.Second) {
		t.Fatalf("canceled tmux session %s was not closed", tmuxSession)
	}

	retryToken := "AGY_CANCEL_RETRY_" + agyRandomHex(5)
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer retryCancel()
	retry, err := adapter.GenerateContent(retryCtx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply exactly as instructed."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply exactly: " + retryToken}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("retry GenerateContent after cancellation error = %v", err)
	}
	retryContent := strings.TrimSpace(retry.Choices[0].Content)
	if !strings.Contains(retryContent, retryToken) {
		t.Fatalf("retry content = %q, want %s", retryContent, retryToken)
	}
	if retrySession, ok := activeAgyInteractiveSession(ownerSessionID); !ok || retrySession == "" || retrySession == tmuxSession {
		t.Fatalf("retry should start a fresh registered session, before=%q after=%q ok=%v", tmuxSession, retrySession, ok)
	}
}

func TestAgyCLIRealSlowToolLiveInputDoesNotCompleteContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-slow-live-" + agyRandomHex(4)
	workDir := t.TempDir()
	bridgeToken := "AGY_SLOW_LIVE_" + agyRandomHex(5)
	liveToken := "AGY_LIVE_VALIDATION_" + agyRandomHex(5)
	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")

	mcpServerPath := writeAgyCancellableSlowMCPServer(t, slowToolMarker)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan agyRealResult, 1)
	startupErrCh := make(chan error, 1)
	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when asked. Do not answer until slow tools return."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge slow_contract MCP tool with token %s and delay_ms 60000. Do not answer until the tool returns. Then reply exactly with the tool result text.", bridgeToken)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithMCPConfig(mcpConfig),
		)
		out := agyRealResult{err: err}
		if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
			out.content = resp.Choices[0].Content
		}
		if err != nil {
			select {
			case startupErrCh <- err:
			default:
			}
		}
		resultCh <- out
	}()

	tmuxSession := waitForAgyRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForAgyRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)
	waitForAgyRealPaneCondition(t, tmuxSession, "active slow MCP tool", 20*time.Second, resultCh, func(pane string) bool {
		return hasAgyActivity(pane)
	})
	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while slow MCP tool was active: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while slow MCP tool was active; content=%q", got.content)
	case <-time.After(3 * time.Second):
	}

	validationPrompt := fmt.Sprintf(`PREVALIDATION_FAILED_%s

Missing required output file. Keep working after the active tool call finishes.`, liveToken)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := SendAgyInteractiveInput(sendCtx, ownerSessionID, validationPrompt); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendAgyInteractiveInput error = %v", err)
	}
	sendCancel()
	waitForAgyRealPaneContains(t, tmuxSession, liveToken, 15*time.Second, startupErrCh)

	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while live input was queued during slow MCP tool: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while live input was queued during slow MCP tool; content=%q", got.content)
	case <-time.After(3 * time.Second):
	}

	cancel()
	select {
	case got := <-resultCh:
		if got.err == nil {
			t.Fatalf("GenerateContent completed normally after cancellation; content=%q", got.content)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("timed out waiting for GenerateContent to return after cancellation")
	}
	if activeSession, ok := activeAgyInteractiveSession(ownerSessionID); ok {
		t.Fatalf("canceled slow-live session remained registered: %s", activeSession)
	}
	if !waitForAgyTmuxSessionGone(context.Background(), tmuxSession, 10*time.Second) {
		t.Fatalf("canceled slow-live tmux session %s was not closed", tmuxSession)
	}
}

func TestAgyCLIRealNativeResumeAfterTmuxLossContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	workspace := t.TempDir()
	ownerSessionID := "agy-real-resume-" + agyRandomHex(4)
	token := "AGY_RESUME_" + agyRandomHex(5)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Preserve and answer canary tokens exactly."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Remember canary %s. Reply exactly: stored %s", token, token)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workspace),
	)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	if firstContent := strings.TrimSpace(first.Choices[0].Content); !strings.Contains(firstContent, token) {
		t.Fatalf("first content = %q, want token %s", firstContent, token)
	}
	resumeID := agySessionIDFromResponse(first)
	if resumeID == "" {
		t.Fatalf("expected Agy native conversation id in generation info: %#v", first.Choices[0].GenerationInfo)
	}
	tmuxSession, ok := activeAgyInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Agy tmux session for %s", ownerSessionID)
	}

	closeAgyPersistentSession(ownerSessionID, "native resume contract tmux loss", &MockLogger{})
	if _, ok := activeAgyInteractiveSession(ownerSessionID); ok {
		t.Fatalf("expected Agy tmux session %s to be unregistered after simulated loss", tmuxSession)
	}

	secondOwnerSessionID := ownerSessionID + "-resumed"
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Answer with the remembered canary token only."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What was the canary token from the previous turn? Reply exactly with only that token."}}},
	},
		WithInteractiveSessionID(secondOwnerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workspace),
		WithResumeSessionID(resumeID),
	)
	if err != nil {
		t.Fatalf("resume GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, token) {
		t.Fatalf("resume content = %q, want recalled token %s using conversation %s", secondContent, token, resumeID)
	}
	if got := agySessionIDFromResponse(second); got != resumeID {
		t.Fatalf("resumed generation conversation id = %q, want %q", got, resumeID)
	}
}

func TestAgyCLIRealInteractiveLiveInputProcessesQueuedFollowupContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-live-process-" + agyRandomHex(4)
	workDir := t.TempDir()
	bridgeToken := "AGY_LIVE_PROCESS_" + agyRandomHex(5)
	firstDone := "AGY_FIRST_DONE_" + agyRandomHex(5)
	liveAck := "AGY_LIVE_ACK_" + agyRandomHex(5)
	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")

	mcpServerPath := writeAgyCancellableSlowMCPServer(t, slowToolMarker)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	parentCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resultCh := make(chan agyRealResult, 1)
	startupErrCh := make(chan error, 1)
	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use declared MCP tools when asked. If a follow-up user message arrives while you are working, handle it after the current tool call finishes."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge slow_contract MCP tool with token %s and delay_ms 8000. Do not answer until the tool returns. Then reply exactly %s.", bridgeToken, firstDone)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithMCPConfig(mcpConfig),
		)
		out := agyRealResult{err: err}
		if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
			out.content = resp.Choices[0].Content
		}
		if err != nil {
			select {
			case startupErrCh <- err:
			default:
			}
		}
		resultCh <- out
	}()

	tmuxSession := waitForAgyRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForAgyRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)

	liveMessage := fmt.Sprintf("Follow-up task: after the current answer completes, reply exactly %s and nothing else.", liveAck)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := SendAgyInteractiveInput(sendCtx, ownerSessionID, liveMessage); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendAgyInteractiveInput error = %v", err)
	}
	sendCancel()

	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("GenerateContent error = %v", got.err)
		}
		paneAfter, err := captureAgyPane(context.Background(), tmuxSession)
		if err != nil {
			t.Fatalf("capture Agy pane after live follow-up: %v", err)
		}
		if !strings.Contains(got.content, liveAck) {
			t.Fatalf("live follow-up was not extracted as final content; content=%q pane:\n%s", got.content, paneAfter)
		}
		if strings.Contains(got.content, liveMessage) || strings.Contains(got.content, firstDone) {
			t.Fatalf("final content should be the live follow-up answer only; content=%q", got.content)
		}
	case <-time.After(4 * time.Minute):
		pane, _ := captureAgyPane(context.Background(), tmuxSession)
		t.Fatalf("timed out waiting for Agy to process queued live input; pane:\n%s", pane)
	}
}

func TestAgyCLIRealInteractiveLiveInputAndEscapeContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	adapter := NewAgyCLIAdapter("", "agy-cli", &MockLogger{})
	ownerSessionID := "agy-real-live-" + agyRandomHex(4)
	token := "LIVE_REAL_" + agyRandomHex(4)

	parentCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Write a long checklist about tmux testing and include %s near the end.", token)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(t.TempDir()),
		)
		errCh <- err
	}()

	tmuxSession := waitForAgyRealActiveSession(t, ownerSessionID, 45*time.Second, errCh)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	liveMessage := "LIVE_FOLLOWUP_" + token
	liveErr := SendAgyInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if liveErr != nil {
		cancel()
		t.Fatalf("SendAgyInteractiveInput error = %v", liveErr)
	}
	waitForAgyRealPaneContains(t, tmuxSession, liveMessage, 10*time.Second, errCh)

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("GenerateContent completed normally; want cancellation")
		}
	case <-time.After(45 * time.Second):
		t.Fatalf("timed out waiting for GenerateContent to return after cancellation")
	}
}

func requireRealAgyCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_AGY_CLI_REAL_E2E") == "" && os.Getenv("RUN_AGY_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_AGY_CLI_REAL_E2E=1 to run real Antigravity CLI tmux contract tests")
	}
	for _, bin := range []string{"agy", "tmux", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Antigravity CLI tests require %s in PATH: %v", bin, err)
		}
	}
}

func writeAgyContractMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-contract-mcp.js")
	script := `#!/usr/bin/env node
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "echo_contract",
      description: "Return a deterministic bridge contract token.",
      inputSchema: { type: "object", properties: { token: { type: "string" } }, required: ["token"] }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "echo_contract") {
    const token = String((msg.params.arguments && msg.params.arguments.token) || "");
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "BRIDGE_TOOL_OK_" + token }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy MCP server: %v", err)
	}
	return path
}

func writeAgyBridgeWriteMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-bridge-write-mcp.js")
	script := `#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "write_via_bridge",
      description: "Write content to a file at an absolute path.",
      inputSchema: { type: "object", properties: { path: { type: "string" }, content: { type: "string" } }, required: ["path", "content"] }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "write_via_bridge") {
    const args = msg.params.arguments || {};
    const target = String(args.path || "");
    const content = String(args.content || "");
    if (!target || !content) {
      return send({ jsonrpc: "2.0", id: msg.id, error: { code: -32602, message: "path and content are required" } });
    }
    fs.writeFileSync(target, "BRIDGE_WROTE_THIS:" + content);
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "BRIDGE_WRITE_OK" }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy bridge-write MCP server: %v", err)
	}
	return path
}

func writeAgyReportCWDMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-report-cwd-mcp.js")
	script := `#!/usr/bin/env node
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "report_cwd",
      description: "Return the current working directory of this MCP server process.",
      inputSchema: { type: "object", properties: {} }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "report_cwd") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "AGY_MCP_CWD:" + process.cwd() }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy report-cwd MCP server: %v", err)
	}
	return path
}

func writeAgySlowMCPServer(t *testing.T, serverSecret string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-slow-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const serverSecret = %q;
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
rl.on("line", async (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "slow_contract",
      description: "Sleep for the requested delay and return a deterministic hidden bridge contract token.",
      inputSchema: {
        type: "object",
        properties: { token: { type: "string" }, delay_ms: { type: "number" } },
        required: ["token", "delay_ms"]
      }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "slow_contract") {
    const args = msg.params.arguments || {};
    const token = String(args.token || "");
    const delayMS = Math.max(0, Math.min(60000, Number(args.delay_ms || 0)));
    await sleep(delayMS);
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "SLOW_BRIDGE_TOOL_OK_" + token + "_" + serverSecret }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`, serverSecret)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy slow MCP server: %v", err)
	}
	return path
}

func writeAgyCancellableSlowMCPServer(t *testing.T, markerPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-cancellable-slow-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const markerPath = %q;
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
rl.on("line", async (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "slow_contract",
      description: "Mark that the tool started, sleep for the requested delay, and return a deterministic token.",
      inputSchema: {
        type: "object",
        properties: { token: { type: "string" }, delay_ms: { type: "number" } },
        required: ["token", "delay_ms"]
      }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "slow_contract") {
    const args = msg.params.arguments || {};
    const token = String(args.token || "");
    const delayMS = Math.max(0, Math.min(120000, Number(args.delay_ms || 0)));
    fs.writeFileSync(markerPath, "started");
    await sleep(delayMS);
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "CANCEL_SLOW_TOOL_DONE_" + token }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`, markerPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy cancellable slow MCP server: %v", err)
	}
	return path
}

func writeAgyIsolationMCPServer(t *testing.T, sessionMarker, outputPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy-isolation-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const sessionMarker = %q;
const outputPath = %q;
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2024-11-05", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [{
      name: "write_contract",
      description: "Write a deterministic isolation contract record and return its marker.",
      inputSchema: { type: "object", properties: { token: { type: "string" } }, required: ["token"] }
    }] } });
  }
  if (msg.method === "tools/call" && msg.params && msg.params.name === "write_contract") {
    const token = String((msg.params.arguments && msg.params.arguments.token) || "");
    const payload = { sessionMarker, token, pid: process.pid };
    fs.writeFileSync(outputPath, JSON.stringify(payload));
    return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "ISOLATED_OK_" + sessionMarker + "_" + token }] } });
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`, sessionMarker, outputPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Agy isolation MCP server: %v", err)
	}
	return path
}

func waitForAgyRealActiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before active session was available: %v", err)
		default:
		}
		if sessionName, ok := activeAgyInteractiveSession(ownerSessionID); ok {
			return sessionName
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for real Agy interactive session %q", ownerSessionID)
	return ""
}

func waitForAgyRealFile(t *testing.T, path, label string, timeout time.Duration, errCh <-chan agyRealResult) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case got := <-errCh:
			t.Fatalf("GenerateContent returned before %s: err=%v content=%q", label, got.err, got.content)
		default:
		}
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s at %s", label, path)
}

func waitForAgyBridgeOnlyDenial(t *testing.T, path, label string, timeout time.Duration, toolNames, requiredSubstrings []string) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var latest string
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			latest = string(body)
			for _, line := range strings.Split(latest, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var entry struct {
					ToolCall struct {
						Name string `json:"name"`
					} `json:"toolCall"`
				}
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatalf("%s contains invalid JSON line: %v; line=%q; full log=%q", label, err, line, latest)
				}
				if !agyStringIn(entry.ToolCall.Name, toolNames) {
					continue
				}
				missing := ""
				for _, required := range requiredSubstrings {
					if required != "" && !strings.Contains(line, required) {
						missing = required
						break
					}
				}
				if missing == "" {
					return latest
				}
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", label, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s with tool names %v and required substrings %v; latest log=%q", label, toolNames, requiredSubstrings, latest)
	return ""
}

func agyStringIn(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func waitForAgyRealPaneCondition(t *testing.T, tmuxSession, label string, timeout time.Duration, errCh <-chan agyRealResult, matches func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case got := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane matched %s: err=%v content=%q", label, got.err, got.content)
		default:
		}
		pane, err := captureAgyPane(context.Background(), tmuxSession)
		if err == nil && matches(pane) {
			return pane
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := captureAgyPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Agy tmux pane to match %s; latest pane:\n%s", label, pane)
	return ""
}

func waitForAgyRealPaneContains(t *testing.T, tmuxSession, want string, timeout time.Duration, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane contained %q: %v", want, err)
		default:
		}
		pane, err := captureAgyPane(context.Background(), tmuxSession)
		if err == nil && strings.Contains(pane, want) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := captureAgyPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Agy tmux pane to contain %q; latest pane:\n%s", want, pane)
}

func assertAgyInteractiveTerminalOnlyStream(t *testing.T, streamChan <-chan llmtypes.StreamChunk) {
	t.Helper()
	terminalCount := 0
	for {
		select {
		case chunk, ok := <-streamChan:
			if !ok {
				if terminalCount == 0 {
					t.Fatal("expected terminal snapshots while waiting for agy prompt/response")
				}
				return
			}
			if chunk.Type == llmtypes.StreamChunkTypeContent {
				t.Fatalf("tmux adapter should not stream parsed content chunks; got %q", chunk.Content)
			}
			if chunk.Type == llmtypes.StreamChunkTypeTerminal {
				terminalCount++
			}
		default:
			return
		}
	}
}

func assertAgyUsage(t *testing.T, resp *llmtypes.ContentResponse) {
	t.Helper()
	gi := resp.Choices[0].GenerationInfo
	if gi == nil || gi.InputTokens == nil || gi.OutputTokens == nil || *gi.InputTokens == 0 || *gi.OutputTokens == 0 {
		t.Fatalf("expected non-zero token usage; gi=%+v usage=%+v", gi, resp.Usage)
	}
}

func assertAgyIntermediate(t *testing.T, resp *llmtypes.ContentResponse) {
	t.Helper()
	gi := resp.Choices[0].GenerationInfo
	intermediate, ok := llmtypes.ExtractCodingProviderIntermediateMessages(gi)
	if !ok || intermediate.Provider != "agy-cli" || intermediate.Transport != llmtypes.CodingProviderTransportTmux || len(intermediate.Messages) == 0 {
		t.Fatalf("expected tmux intermediate messages for agy; gi=%+v", gi)
	}
}

func agySessionIDFromResponse(resp *llmtypes.ContentResponse) string {
	if handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(resp); ok {
		if id := strings.TrimSpace(handle.NativeSessionID); id != "" {
			return id
		}
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0] == nil || resp.Choices[0].GenerationInfo == nil {
		return ""
	}
	additional := resp.Choices[0].GenerationInfo.Additional
	if additional == nil {
		return ""
	}
	if id, ok := additional["agy_session_id"].(string); ok {
		return strings.TrimSpace(id)
	}
	return ""
}
