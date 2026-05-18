package geminicli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestGeminiCLIRealInteractiveTmuxFullContract(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-contract-" + geminiRandomHex(4)
	token := "REAL_GEMINI_TMUX_" + geminiRandomHex(4)

	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "*"
decision = "deny"
priority = 999
deny_message = "No tools are needed for this contract test."
`)

	options := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectSettings(`{}`),
		WithAdminPolicyPath(policyPath),
		WithApprovalMode("yolo"),
	}

	largeSystemPrompt := strings.Repeat("You are testing the Gemini CLI tmux transport. Do not use tools. Keep exact-token replies concise.\n", 80)
	firstPrompt := fmt.Sprintf(`This is a real Gemini CLI tmux contract test.

Preserve input safely:

blank line above
JSON: {"token": %q, "items": ["alpha", "beta"]}
Shell-looking text that must not execute: echo SHOULD_NOT_RUN
Unicode: नमस्ते

Take note of the word %s. Do not save it to memory.

Reply exactly:
noted %s`, token, token, token)

	firstStream := make(chan llmtypes.StreamChunk, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	firstOptions := append([]llmtypes.CallOption{}, options...)
	firstOptions = append(firstOptions, llmtypes.WithStreamingChan(firstStream))

	first, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: largeSystemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: firstPrompt}}},
	}, firstOptions...)
	if err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	firstContent := strings.TrimSpace(first.Choices[0].Content)
	if !strings.Contains(firstContent, token) {
		t.Fatalf("first content = %q, want token %s", firstContent, token)
	}
	assertGeminiInteractiveTerminalOnlyStream(t, firstStream)

	tmuxSession, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Gemini tmux session for %s", ownerSessionID)
	}
	pane, err := captureGeminiPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Gemini pane: %v", err)
	}
	if !hasGeminiReadyPrompt(pane) {
		t.Fatalf("real Gemini TUI ready prompt not detected; pane:\n%s", pane)
	}
	if hasGeminiActivity(pane) {
		t.Fatalf("real Gemini TUI should be idle after first turn; pane:\n%s", pane)
	}

	secondStream := make(chan llmtypes.StreamChunk, 64)
	secondOptions := append([]llmtypes.CallOption{}, options...)
	secondOptions = append(secondOptions, llmtypes.WithStreamingChan(secondStream))
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: largeSystemPrompt}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What exact word did I ask you to take note of? Reply with only that word."}}},
	}, secondOptions...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	secondContent := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(secondContent, token) {
		t.Fatalf("second content = %q, want remembered token %s from native tmux session", secondContent, token)
	}
	if strings.Contains(secondContent, "saved "+token) {
		t.Fatalf("second content replayed first assistant response: %q", secondContent)
	}
	assertGeminiInteractiveTerminalOnlyStream(t, secondStream)

	tmuxSessionAfter, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok || tmuxSessionAfter != tmuxSession {
		t.Fatalf("expected same tmux session reused, before=%q after=%q ok=%v", tmuxSession, tmuxSessionAfter, ok)
	}
}

func TestGeminiCLIRealInteractiveLargePastedPromptSubmits(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-large-paste-" + geminiRandomHex(4)
	token := "LARGE_PASTE_OK_" + geminiRandomHex(4)

	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "*"
decision = "deny"
priority = 999
deny_message = "No tools are needed for this large-paste transport test."
`)

	var prompt strings.Builder
	prompt.WriteString("This is a Gemini CLI large-paste transport test.\n")
	prompt.WriteString("Read the full pasted prompt and do not use tools.\n\n")
	for i := 0; i < 72; i++ {
		fmt.Fprintf(&prompt, "line %02d: preserve pasted multiline input before submitting.\n", i+1)
	}
	fmt.Fprintf(&prompt, "\nReply exactly with this token and nothing else:\n%s", token)

	streamChan := make(chan llmtypes.StreamChunk, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply exactly as instructed."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt.String()}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectSettings(`{}`),
		WithAdminPolicyPath(policyPath),
		WithApprovalMode("yolo"),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent large pasted prompt error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want token %s", content, token)
	}
	assertGeminiInteractiveTerminalOnlyStream(t, streamChan)
}

func TestGeminiCLIRealInteractiveMarkdownBulletCompletionDoesNotLookUnsubmitted(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-bullet-complete-" + geminiRandomHex(4)
	token := "BULLET_COMPLETE_" + geminiRandomHex(4)

	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "*"
decision = "deny"
priority = 999
deny_message = "No tools are needed for this markdown completion test."
`)

	var prompt strings.Builder
	prompt.WriteString("This is a Gemini CLI tmux completion-state contract test.\n")
	prompt.WriteString("Read the full pasted prompt and do not use tools.\n")
	for i := 0; i < 48; i++ {
		fmt.Fprintf(&prompt, "context line %02d: keep the prompt large enough to render as a pasted block.\n", i+1)
	}
	fmt.Fprintf(&prompt, `
Do not echo these instructions. Finish with a four-line answer:
first line is the status text STATUS: COMPLETED;
the next three lines are markdown bullet lines that start with "*".
One bullet must be an Impact bullet containing token %s.
Keep the remaining bullets short and about completion state.`, token)

	streamChan := make(chan llmtypes.StreamChunk, 128)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. Reply exactly as instructed."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt.String()}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectSettings(`{}`),
		WithAdminPolicyPath(policyPath),
		WithApprovalMode("yolo"),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent markdown bullet completion error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) || !strings.Contains(content, "STATUS: COMPLETED") {
		if tmuxSession, ok := activeGeminiInteractiveSession(ownerSessionID); ok && tmuxSession != "" {
			if pane, captureErr := captureGeminiPane(ctx, tmuxSession); captureErr == nil {
				t.Fatalf("content = %q, want completed markdown bullet answer with token %s; pane:\n%s", content, token, pane)
			}
		}
		t.Fatalf("content = %q, want completed markdown bullet answer with token %s", content, token)
	}

	tmuxSession, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok || tmuxSession == "" {
		t.Fatalf("expected active Gemini tmux session for %s", ownerSessionID)
	}
	pane, err := captureGeminiPane(ctx, tmuxSession)
	if err != nil {
		t.Fatalf("capture Gemini pane: %v", err)
	}
	if !hasGeminiReadyPrompt(pane) {
		t.Fatalf("real Gemini TUI ready prompt not detected after markdown completion; pane:\n%s", pane)
	}
	if hasGeminiUnsubmittedDraft(pane) {
		t.Fatalf("completed markdown bullet answer was misdetected as unsubmitted draft; pane:\n%s", pane)
	}
	assertGeminiInteractiveTerminalOnlyStream(t, streamChan)
}

func TestGeminiCLIRealInteractiveMCPBridgeContract(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-mcp-" + geminiRandomHex(4)
	bridgeToken := "BRIDGE_REAL_" + geminiRandomHex(4)

	mcpServerPath := writeGeminiContractMCPServer(t)
	settings := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":%q}}}`, mcpServerPath)
	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "mcp_api-bridge_echo_contract"
decision = "allow"
priority = 999

[[rule]]
toolName = "*"
decision = "deny"
priority = 998
deny_message = "Use only the api-bridge echo_contract MCP tool for this contract test."
`)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 128)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge echo_contract MCP tool with token %s. Then reply exactly with the tool result text.", bridgeToken)}}},
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithProjectSettings(settings),
		WithAdminPolicyPath(policyPath),
		WithApprovalMode("yolo"),
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
	assertGeminiInteractiveTerminalOnlyStream(t, streamChan)
}

func TestGeminiCLIRealInteractiveSharedWorkingDirMCPIsolation(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	sharedWorkDir := filepath.Join(t.TempDir(), "shared-workspace")
	if err := os.MkdirAll(sharedWorkDir, 0o755); err != nil {
		t.Fatalf("create shared workdir: %v", err)
	}

	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "mcp_api-bridge_write_contract"
decision = "allow"
priority = 999

[[rule]]
toolName = "*"
decision = "deny"
priority = 998
deny_message = "Use only the api-bridge write_contract MCP tool for this isolation test."
`)

	type runSpec struct {
		name          string
		ownerSession  string
		projectDirID  string
		sessionMarker string
		token         string
		outputPath    string
		mcpServerPath string
	}
	specs := []runSpec{
		{
			name:          "alpha",
			ownerSession:  "gemini-real-shared-alpha-" + geminiRandomHex(4),
			projectDirID:  "shared-alpha-" + geminiRandomHex(4),
			sessionMarker: "SESSION_ALPHA_" + geminiRandomHex(4),
			token:         "TOKEN_ALPHA_" + geminiRandomHex(4),
			outputPath:    filepath.Join(t.TempDir(), "alpha-output.json"),
		},
		{
			name:          "beta",
			ownerSession:  "gemini-real-shared-beta-" + geminiRandomHex(4),
			projectDirID:  "shared-beta-" + geminiRandomHex(4),
			sessionMarker: "SESSION_BETA_" + geminiRandomHex(4),
			token:         "TOKEN_BETA_" + geminiRandomHex(4),
			outputPath:    filepath.Join(t.TempDir(), "beta-output.json"),
		},
	}
	for i := range specs {
		specs[i].mcpServerPath = writeGeminiIsolationMCPServer(t, specs[i].sessionMarker, specs[i].outputPath)
	}

	type runResult struct {
		spec    runSpec
		content string
		err     error
		genInfo map[string]interface{}
	}
	resultCh := make(chan runResult, len(specs))
	for _, spec := range specs {
		spec := spec
		go func() {
			settings := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":%q}}}`, spec.mcpServerPath)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
				{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
				{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge write_contract MCP tool with token %s. Then reply exactly with the tool result text.", spec.token)}}},
			},
				WithInteractiveSessionID(spec.ownerSession),
				WithPersistentInteractiveSession(true),
				WithWorkingDir(sharedWorkDir),
				WithProjectDirID(spec.projectDirID),
				WithProjectSettings(settings),
				WithAdminPolicyPath(policyPath),
				WithApprovalMode("yolo"),
			)
			result := runResult{spec: spec, err: err}
			if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
				result.content = resp.Choices[0].Content
				if resp.Choices[0].GenerationInfo != nil {
					result.genInfo = resp.Choices[0].GenerationInfo.Additional
				}
			}
			resultCh <- result
		}()
	}

	results := make(map[string]runResult, len(specs))
	for range specs {
		got := <-resultCh
		if got.err != nil {
			t.Fatalf("%s GenerateContent error = %v", got.spec.name, got.err)
		}
		results[got.spec.name] = got
	}

	seenProjectDirs := map[string]string{}
	for _, spec := range specs {
		got := results[spec.name]
		want := "ISOLATED_OK_" + spec.sessionMarker + "_" + spec.token
		if !strings.Contains(got.content, want) {
			t.Fatalf("%s content = %q, want isolated tool result %q", spec.name, got.content, want)
		}
		data, err := os.ReadFile(spec.outputPath)
		if err != nil {
			t.Fatalf("%s output file missing at %s: %v", spec.name, spec.outputPath, err)
		}
		forbiddenMarker := ""
		for _, other := range specs {
			if other.name != spec.name {
				forbiddenMarker = other.sessionMarker
				break
			}
		}
		text := string(data)
		if !strings.Contains(text, spec.sessionMarker) || !strings.Contains(text, spec.token) {
			t.Fatalf("%s output = %s, want session %s and token %s", spec.name, text, spec.sessionMarker, spec.token)
		}
		if forbiddenMarker != "" && strings.Contains(text, forbiddenMarker) {
			t.Fatalf("%s output crossed sessions: %s", spec.name, text)
		}
		projectDirID, _ := got.genInfo["gemini_project_dir_id"].(string)
		if projectDirID != spec.projectDirID {
			t.Fatalf("%s gemini_project_dir_id = %q, want %q; genInfo=%#v", spec.name, projectDirID, spec.projectDirID, got.genInfo)
		}
		projectDir := filepath.Join(os.TempDir(), "gemini-cli-project-"+projectDirID)
		if projectDir == sharedWorkDir {
			t.Fatalf("%s project dir must not equal shared working dir %q", spec.name, sharedWorkDir)
		}
		if owner, exists := seenProjectDirs[projectDir]; exists {
			t.Fatalf("%s reused project dir %q already used by %s", spec.name, projectDir, owner)
		}
		seenProjectDirs[projectDir] = spec.name
	}

	if _, err := os.Stat(filepath.Join(sharedWorkDir, ".gemini", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("shared working directory must not contain Gemini settings; stat err=%v", err)
	}
}

func TestGeminiCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-queued-validation-" + geminiRandomHex(4)
	bridgeToken := "SLOW_BRIDGE_REAL_" + geminiRandomHex(4)

	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpServerPath := writeGeminiSlowContractMCPServer(t, slowToolMarker)
	settings := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":%q}}}`, mcpServerPath)
	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "mcp_api-bridge_slow_contract"
decision = "allow"
priority = 999

[[rule]]
toolName = "*"
decision = "deny"
priority = 998
deny_message = "Use only the api-bridge slow_contract MCP tool for this contract test."
`)

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan geminiRealResult, 1)
	startupErrCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge slow_contract MCP tool with token %s and delay_ms 30000. Do not answer until the tool returns. Then reply exactly with the tool result text.", bridgeToken)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithProjectSettings(settings),
			WithAdminPolicyPath(policyPath),
			WithApprovalMode("yolo"),
			llmtypes.WithStreamingChan(streamChan),
		)
		out := geminiRealResult{err: err}
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

	waitForGeminiRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForGeminiRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)

	activeSession, ok := activeGeminiInteractiveSession(ownerSessionID)
	if !ok || activeSession == "" {
		cancel()
		t.Fatalf("expected active Gemini tmux session for %s", ownerSessionID)
	}
	activePane := waitForGeminiRealPaneCondition(t, activeSession, "active slow MCP tool", 15*time.Second, resultCh, func(pane string) bool {
		return hasGeminiActivity(pane)
	})
	if hasGeminiReadyPrompt(activePane) && !hasGeminiActivity(activePane) {
		cancel()
		t.Fatalf("Gemini pane looked idle-ready while slow MCP tool was still active:\n%s", activePane)
	}
	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while slow MCP tool was active: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while slow MCP tool was active; content=%q", got.content)
	case <-time.After(3 * time.Second):
	}

	validationPrompt := `## Pre-validation failed (retry attempt 3)

❌ PRE-VALIDATION FAILED

Missing required output file. Fix the specific issue above and re-produce the required outputs.`
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := SendGeminiInteractiveInput(sendCtx, ownerSessionID, validationPrompt); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendGeminiInteractiveInput error = %v", err)
	}
	sendCancel()

	pendingLiveInput := ""
	if session, ok := geminiPersistentSession(ownerSessionID); ok {
		session.liveMu.Lock()
		pendingLiveInput = strings.Join(session.pendingLiveInputs, "\n")
		session.liveMu.Unlock()
	}
	if !strings.Contains(pendingLiveInput, "Pre-validation failed") {
		cancel()
		t.Fatalf("validation input was not queued in adapter; pending=%q", pendingLiveInput)
	}
	if pane, err := captureGeminiPane(context.Background(), activeSession); err == nil && hasGeminiReadyPrompt(pane) && !hasGeminiActivity(pane) {
		cancel()
		t.Fatalf("Gemini queued validation pane looked idle-ready:\n%s", pane)
	}

	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while validation input was queued: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while validation input was queued; content=%q", got.content)
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
	_ = drainGeminiStream(streamChan)
}

func TestGeminiCLIRealInteractiveLiveInputContract(t *testing.T) {
	requireRealGeminiCLIE2E(t)
	t.Cleanup(func() { _ = CleanupGeminiCLIInteractiveSessions(context.Background()) })

	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	adapter := NewGeminiCLIAdapter(apiKey, geminiCLIContractModel, &MockLogger{})
	ownerSessionID := "gemini-real-live-" + geminiRandomHex(4)
	token := "LIVE_REAL_" + geminiRandomHex(4)

	policyPath := writeGeminiRealPolicy(t, `[[rule]]
toolName = "*"
decision = "deny"
priority = 999
deny_message = "No tools are needed for this live-input contract test."
`)

	parentCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	type liveResult struct {
		content string
		err     error
	}
	errCh := make(chan liveResult, 1)
	startupErrCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Do not use tools. This is a live input transport test."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Draft a detailed 500 word checklist about tmux transport testing. Include token %s near the end only.", token)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithProjectSettings(`{}`),
			WithAdminPolicyPath(policyPath),
			WithApprovalMode("yolo"),
			llmtypes.WithStreamingChan(streamChan),
		)
		result := liveResult{err: err}
		if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
			result.content = resp.Choices[0].Content
		} else if err != nil {
			startupErrCh <- err
		}
		errCh <- result
	}()

	tmuxSession := waitForGeminiRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	liveAck := "LIVE_ACK_" + token
	liveMessage := "Queued live follow-up: after finishing the current answer, reply exactly " + liveAck
	liveErr := SendGeminiInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if liveErr != nil {
		t.Fatalf("SendGeminiInteractiveInput error = %v", liveErr)
	}

	pane, err := captureGeminiPane(context.Background(), tmuxSession)
	if err != nil {
		t.Fatalf("capture Gemini pane after live input: %v", err)
	}
	pendingLiveInput := ""
	if session, ok := geminiPersistentSession(ownerSessionID); ok {
		session.liveMu.Lock()
		pendingLiveInput = strings.Join(session.pendingLiveInputs, "\n")
		session.liveMu.Unlock()
	}
	if !strings.Contains(pane, liveMessage) && !strings.Contains(pendingLiveInput, liveMessage) {
		t.Fatalf("live input was neither visible in tmux pane nor queued in adapter; pending=%q pane:\n%s", pendingLiveInput, pane)
	}

	select {
	case result := <-errCh:
		if result.err != nil {
			t.Fatalf("GenerateContent error = %v", result.err)
		}
		paneAfter, err := captureGeminiPane(context.Background(), tmuxSession)
		if err != nil {
			t.Fatalf("capture Gemini pane after live response: %v", err)
		}
		if !strings.Contains(result.content, liveAck) && !strings.Contains(paneAfter, liveAck) {
			t.Fatalf("live input was queued but not processed; content=%q pane:\n%s", result.content, paneAfter)
		}
	case <-time.After(4 * time.Minute):
		t.Fatalf("timed out waiting for Gemini to process live input")
	}
	_ = drainGeminiStream(streamChan)
}

func requireRealGeminiCLIE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_GEMINI_CLI_REAL_E2E") == "" && os.Getenv("RUN_GEMINI_CLI_INTERACTIVE_E2E") == "" {
		t.Skip("set RUN_GEMINI_CLI_REAL_E2E=1 to run real Gemini CLI tmux contract tests")
	}
	if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" {
		t.Fatal("real Gemini CLI tests require GEMINI_API_KEY")
	}
	for _, bin := range []string{"gemini", "tmux", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Gemini CLI tests require %s in PATH: %v", bin, err)
		}
	}
}

func waitForGeminiRealActiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before active session was available: %v", err)
		default:
		}
		if sessionName, ok := activeGeminiInteractiveSession(ownerSessionID); ok {
			return sessionName
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for real Gemini interactive session %q", ownerSessionID)
	return ""
}

func waitForGeminiRealFile(t *testing.T, path, label string, timeout time.Duration, errCh <-chan geminiRealResult) {
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

func waitForGeminiRealPaneCondition(t *testing.T, tmuxSession, label string, timeout time.Duration, errCh <-chan geminiRealResult, matches func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case got := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane matched %s: err=%v content=%q", label, got.err, got.content)
		default:
		}
		pane, err := captureGeminiPane(context.Background(), tmuxSession)
		if err == nil && matches(pane) {
			return pane
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := captureGeminiPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Gemini tmux pane to match %s; latest pane:\n%s", label, pane)
	return ""
}

type geminiRealResult struct {
	content string
	err     error
}

func writeGeminiRealPolicy(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "restrict-tools.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write Gemini policy: %v", err)
	}
	return path
}

func writeGeminiContractMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gemini-contract-mcp.js")
	script := `#!/usr/bin/env node
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    return;
  }
  if (msg.method === "initialize") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "api-bridge", version: "1.0.0" }
      }
    });
    return;
  }
  if (msg.method === "notifications/initialized") {
    return;
  }
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "echo_contract",
          description: "Return a deterministic contract token.",
          inputSchema: {
            type: "object",
            properties: { token: { type: "string" } },
            required: ["token"]
          }
        }]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    const token = String(args.token || "");
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        content: [{ type: "text", text: "BRIDGE_TOOL_OK_" + token }],
        isError: false
      }
    });
    return;
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, result: {} });
  }
});
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write MCP server: %v", err)
	}
	return path
}

func writeGeminiIsolationMCPServer(t *testing.T, sessionMarker, outputPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gemini-isolation-contract-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const sessionMarker = %q;
const outputPath = %q;

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    return;
  }
  if (msg.method === "initialize") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "api-bridge", version: "1.0.0" }
      }
    });
    return;
  }
  if (msg.method === "notifications/initialized") {
    return;
  }
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "write_contract",
          description: "Write a deterministic marker proving this MCP server/session was used.",
          inputSchema: {
            type: "object",
            properties: { token: { type: "string" } },
            required: ["token"]
          }
        }]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    const token = String(args.token || "");
    const payload = {
      session_marker: sessionMarker,
      token,
      cwd: process.cwd(),
      gemini_project_dir: process.env.GEMINI_PROJECT_DIR || "",
      timestamp: new Date().toISOString()
    };
    fs.writeFileSync(outputPath, JSON.stringify(payload, null, 2));
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        content: [{ type: "text", text: "ISOLATED_OK_" + sessionMarker + "_" + token }],
        isError: false
      }
    });
    return;
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, result: {} });
  }
});
`, sessionMarker, outputPath)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write isolation MCP server: %v", err)
	}
	return path
}

func writeGeminiSlowContractMCPServer(t *testing.T, markerPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gemini-slow-contract-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const markerPath = %q;

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    return;
  }
  if (msg.method === "initialize") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "api-bridge", version: "1.0.0" }
      }
    });
    return;
  }
  if (msg.method === "notifications/initialized") {
    return;
  }
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "slow_contract",
          description: "Return a deterministic contract token after a caller-provided delay.",
          inputSchema: {
            type: "object",
            properties: {
              token: { type: "string" },
              delay_ms: { type: "number" }
            },
            required: ["token"]
          }
        }]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    const token = String(args.token || "");
    const delay = Math.max(0, Math.min(Number(args.delay_ms || 30000), 60000));
    fs.writeFileSync(markerPath, JSON.stringify({ token, delay, started_at: new Date().toISOString() }));
    setTimeout(() => {
      send({
        jsonrpc: "2.0",
        id: msg.id,
        result: {
          content: [{ type: "text", text: "SLOW_BRIDGE_TOOL_OK_" + token }],
          isError: false
        }
      });
    }, delay);
    return;
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, result: {} });
  }
});
`, markerPath)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write slow MCP server: %v", err)
	}
	return path
}

func assertGeminiDoesNotContainAny(t *testing.T, label, got string, forbidden ...string) {
	t.Helper()
	for _, item := range forbidden {
		if strings.Contains(got, item) {
			t.Fatalf("%s leaked %q in %q", label, item, got)
		}
	}
}
