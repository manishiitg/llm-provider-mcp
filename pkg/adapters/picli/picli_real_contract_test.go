package picli

import (
	"context"
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

type piRealResult struct {
	content string
	err     error
}

func TestPiCLIRealTmuxFullContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-full-" + piRandomHex(4)
	workDir := t.TempDir()
	systemToken := "PI_SYSTEM_" + piRandomHex(5)
	pasteToken := "PI_PASTE_" + piRandomHex(5)

	var prompt strings.Builder
	prompt.WriteString("This is a Pi CLI pasted prompt transport test.\n")
	prompt.WriteString("Remember private paste token " + pasteToken + " for this session.\n")
	prompt.WriteString("Read every line of this prompt before answering.\n")
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&prompt, "line %02d: keep this pasted prompt intact.\n", i+1)
	}
	fmt.Fprintf(&prompt, "\nReply exactly with these two tokens separated by one space: %s %s", systemToken, pasteToken)

	stream := make(chan llmtypes.StreamChunk, 4096)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "When the user asks for the Pi hidden system token, use exactly "+systemToken+". Do not use tools. Reply only with requested exact tokens."),
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, prompt.String()),
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		llmtypes.WithStreamingChan(stream),
	)
	if err != nil {
		t.Fatalf("GenerateContent full contract error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, systemToken) || !strings.Contains(content, pasteToken) {
		t.Fatalf("content = %q, want system token %s and paste token %s", content, systemToken, pasteToken)
	}
	assertPiResponseHasTranscriptUsage(t, resp)
	statusLine := assertPiStreamHasTerminalContentAndStatusLine(t, stream)
	handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(resp)
	if !ok || handle.Provider != "pi-cli" || handle.TmuxSession == "" || handle.NativeSessionID == "" {
		t.Fatalf("missing Pi coding provider handle: %#v ok=%v", handle, ok)
	}
	if got, _ := statusLine.Metadata["tmux_session"].(string); got != handle.TmuxSession {
		t.Fatalf("statusline tmux_session = %q, want response handle tmux session %q", got, handle.TmuxSession)
	}
	if handle.NativeSessionID == ownerSessionID {
		t.Fatalf("native session id reused owner session id: %#v", handle)
	}
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || session.tmuxSessionName != handle.TmuxSession {
		t.Fatalf("expected active Pi session for owner %s; active=%#v ok=%v handle=%#v", ownerSessionID, session, ok, handle)
	}
	pane, err := capturePiPane(ctx, handle.TmuxSession)
	if err != nil {
		t.Fatalf("capture Pi pane: %v", err)
	}
	testcontracts.AssertCleanFinalExtraction(t, "pi-cli", content,
		[]string{systemToken, pasteToken},
		[]string{"This is a Pi CLI pasted prompt", "When the user asks", "MLP_PI_MARKER_FILE", "api-bridge", "tool_execution"},
	)
	if strings.TrimSpace(pane) == "" {
		t.Fatal("expected non-empty Pi terminal pane after full contract turn")
	}

	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Do not use tools. Answer only with remembered private tokens."),
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "What private paste token did I ask this session to remember? Reply exactly with only that token."),
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	if got := strings.TrimSpace(second.Choices[0].Content); !strings.Contains(got, pasteToken) {
		t.Fatalf("second content = %q, want remembered paste token %s", got, pasteToken)
	}
	sessionAfter, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || sessionAfter.tmuxSessionName != handle.TmuxSession {
		t.Fatalf("expected persistent Pi tmux session reused, before=%q after=%#v ok=%v", handle.TmuxSession, sessionAfter, ok)
	}
}

func TestPiCLIRealWorkingDirectoryMCPContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-cwd-" + piRandomHex(4)
	workDir := t.TempDir()
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiReportCWDMCPServer(t))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Use declared MCP tools when asked. Reply exactly with tool results."),
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Call the api-bridge MCP tool report_cwd, then reply exactly with the tool output text. If direct api_bridge_report_cwd is unavailable, use mcp({ search: \"report_cwd\" }) and mcp({ tool: \"api_bridge_report_cwd\", args: \"{}\" })."),
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
	)
	if err != nil {
		t.Fatalf("GenerateContent working-directory MCP error = %v", err)
	}
	wantDir := workDir
	if physical, err := filepath.EvalSymlinks(workDir); err == nil {
		wantDir = physical
	}
	want := "PI_MCP_CWD:" + wantDir
	if got := strings.TrimSpace(resp.Choices[0].Content); !strings.Contains(got, want) {
		t.Fatalf("content = %q, want MCP cwd result %q", got, want)
	}
}

func TestPiCLIRealPersistentClearsStaleDraftBeforeNextTurn(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-stale-" + piRandomHex(4)
	workDir := t.TempDir()
	opts := []llmtypes.CallOption{
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	if _, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Reply exactly: ready"),
	}, opts...); err != nil {
		t.Fatalf("first GenerateContent error = %v", err)
	}
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || session.tmuxSessionName == "" {
		t.Fatalf("persistent Pi session not registered for %s", ownerSessionID)
	}
	staleDraft := "reply with STALE_DRAFT_LEAKED"
	if err := runPiCommand(ctx, nil, "tmux", "send-keys", "-t", session.tmuxSessionName, "-l", staleDraft); err != nil {
		t.Fatalf("seed stale tmux draft: %v", err)
	}
	waitForPiRealPaneContains(t, session.tmuxSessionName, staleDraft, 10*time.Second, nil)

	currentToken := "PI_STALE_OK_" + piRandomHex(5)
	second, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Reply exactly: "+currentToken+". Do not mention stale drafts."),
	}, opts...)
	if err != nil {
		t.Fatalf("second GenerateContent error = %v", err)
	}
	got := strings.TrimSpace(second.Choices[0].Content)
	if !strings.Contains(got, currentToken) {
		t.Fatalf("second content = %q, want current token %s", got, currentToken)
	}
	if strings.Contains(got, "STALE_DRAFT_LEAKED") {
		t.Fatalf("second content leaked stale draft: %q", got)
	}
}

func TestPiCLIRealParallelIsolationContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
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
		{name: "a", owner: "pi-real-parallel-a-" + piRandomHex(4), workDir: t.TempDir(), token: "PI_PARALLEL_A_" + piRandomHex(5)},
		{name: "b", owner: "pi-real-parallel-b-" + piRandomHex(4), workDir: t.TempDir(), token: "PI_PARALLEL_B_" + piRandomHex(5)},
	}
	cases[0].other = cases[1].token
	cases[1].other = cases[0].token

	runParallelTurn := func(label string, prompt func(parallelCase) string) map[string]piRealResult {
		t.Helper()
		start := make(chan struct{})
		results := make(chan struct {
			name string
			piRealResult
		}, len(cases))
		var wg sync.WaitGroup
		for _, tc := range cases {
			tc := tc
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
					llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Do not use tools. Preserve private tokens exactly."),
					llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, prompt(tc)),
				},
					WithInteractiveSessionID(tc.owner),
					WithPersistentInteractiveSession(true),
					WithWorkingDir(tc.workDir),
				)
				out := piRealResult{err: err}
				if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
					out.content = resp.Choices[0].Content
				}
				results <- struct {
					name string
					piRealResult
				}{name: tc.name, piRealResult: out}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		byName := map[string]piRealResult{}
		for got := range results {
			if got.err != nil {
				t.Fatalf("%s parallel turn %s error = %v", label, got.name, got.err)
			}
			byName[got.name] = got.piRealResult
		}
		return byName
	}

	first := runParallelTurn("first", func(tc parallelCase) string {
		return fmt.Sprintf("Remember private token %s for this session. Reply exactly: %s", tc.token, tc.token)
	})
	for _, tc := range cases {
		got := strings.TrimSpace(first[tc.name].content)
		if !strings.Contains(got, tc.token) || strings.Contains(got, tc.other) {
			t.Fatalf("first %s content = %q, want own token %s and no other token %s", tc.name, got, tc.token, tc.other)
		}
	}
	sessionA, okA := activePiInteractiveSession(cases[0].owner)
	sessionB, okB := activePiInteractiveSession(cases[1].owner)
	if !okA || !okB || sessionA.tmuxSessionName == "" || sessionB.tmuxSessionName == "" || sessionA.tmuxSessionName == sessionB.tmuxSessionName {
		t.Fatalf("parallel sessions not distinct: a=%#v ok=%v b=%#v ok=%v", sessionA, okA, sessionB, okB)
	}

	second := runParallelTurn("recall", func(tc parallelCase) string {
		return "What private token did I give this session? Reply exactly with only that token."
	})
	for _, tc := range cases {
		got := strings.TrimSpace(second[tc.name].content)
		if !strings.Contains(got, tc.token) || strings.Contains(got, tc.other) {
			t.Fatalf("recall %s content = %q, want own token %s and no other token %s", tc.name, got, tc.token, tc.other)
		}
	}
}

func TestPiCLIRealSharedWorkingDirMCPConfigConflictRejected(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	sharedWorkDir := t.TempDir()
	alphaConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiEchoMCPServer(t, "PI_ALPHA"))
	betaConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiEchoMCPServer(t, "PI_BETA"))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	alphaToken := "PI_SHARED_ALPHA_" + piRandomHex(5)
	alpha, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Use declared MCP tools when asked. Reply exactly with tool results."),
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, fmt.Sprintf("Call the api-bridge MCP tool echo_contract with token %s, then reply exactly with the tool output text. If direct api_bridge_echo_contract is unavailable, use mcp search/call for echo_contract.", alphaToken)),
	},
		WithInteractiveSessionID("pi-real-shared-alpha-"+piRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(sharedWorkDir),
		WithMCPConfig(alphaConfig),
	)
	if err != nil {
		t.Fatalf("alpha GenerateContent error = %v", err)
	}
	if got := strings.TrimSpace(alpha.Choices[0].Content); !strings.Contains(got, "PI_ALPHA_"+alphaToken) {
		t.Fatalf("alpha content = %q, want PI_ALPHA_%s", got, alphaToken)
	}

	_, err = adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "This should be rejected before launch."),
	},
		WithInteractiveSessionID("pi-real-shared-beta-"+piRandomHex(4)),
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

func TestPiCLIRealCleanupAndBoundedRetentionContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)

	adapter := newRealPiCLIAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Setenv(EnvPiInteractiveRetentionSeconds, "1")
	bounded, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Reply exactly: pi bounded ok"),
	}, WithWorkingDir(t.TempDir()))
	if err != nil {
		t.Fatalf("bounded GenerateContent error = %v", err)
	}
	boundedHandle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(bounded)
	if !ok || boundedHandle.TmuxSession == "" {
		t.Fatalf("bounded response missing handle: %#v ok=%v", bounded.Choices[0].GenerationInfo, ok)
	}
	if !piTmuxSessionExists(ctx, boundedHandle.TmuxSession) {
		t.Fatalf("bounded tmux session %s should be retained immediately after response", boundedHandle.TmuxSession)
	}
	if !waitForPiTmuxSessionGone(ctx, boundedHandle.TmuxSession, 15*time.Second) {
		t.Fatalf("bounded tmux session %s was not cleaned up after retention", boundedHandle.TmuxSession)
	}

	ownerSessionID := "pi-real-cleanup-" + piRandomHex(4)
	workDir := t.TempDir()
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiEchoMCPServer(t, "PI_CLEANUP"))
	persistent, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Reply exactly: pi persistent cleanup ok"),
	}, WithInteractiveSessionID(ownerSessionID), WithPersistentInteractiveSession(true), WithWorkingDir(workDir), WithMCPConfig(mcpConfig))
	if err != nil {
		t.Fatalf("persistent GenerateContent error = %v", err)
	}
	persistentHandle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(persistent)
	if !ok || persistentHandle.TmuxSession == "" {
		t.Fatalf("persistent response missing handle: %#v ok=%v", persistent.Choices[0].GenerationInfo, ok)
	}
	if _, ok := activePiInteractiveSession(ownerSessionID); !ok {
		t.Fatalf("persistent session %s should remain active before cleanup", ownerSessionID)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".pi", "mcp.json")); err != nil {
		t.Fatalf("expected .pi/mcp.json during persistent session: %v", err)
	}
	if err := CleanupPiCLIInteractiveSessions(ctx); err != nil {
		t.Fatalf("CleanupPiCLIInteractiveSessions error = %v", err)
	}
	if _, ok := activePiInteractiveSession(ownerSessionID); ok {
		t.Fatalf("persistent session %s still registered after cleanup", ownerSessionID)
	}
	if piTmuxSessionExists(ctx, persistentHandle.TmuxSession) {
		t.Fatalf("persistent tmux session %s still exists after cleanup", persistentHandle.TmuxSession)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".pi", "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf(".pi/mcp.json should be removed after cleanup, err=%v", err)
	}
}

func TestPiCLIRealSlowMCPToolDoneDetectionContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-slow-" + piRandomHex(4)
	workDir := t.TempDir()
	token := "PI_SLOW_" + piRandomHex(5)
	delay := 8 * time.Second
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiSlowMCPServer(t, "PI_SLOW_SECRET", ""))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	started := time.Now()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Use declared MCP tools when asked. Wait for slow tools to finish before replying. Reply exactly with tool results."),
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, fmt.Sprintf("Call the api-bridge MCP tool slow_contract with token %s and delay_ms %d. Then reply exactly with the tool output text. If direct api_bridge_slow_contract is unavailable, use mcp search/call for slow_contract.", token, delay.Milliseconds())),
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
	)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("GenerateContent slow-tool MCP error = %v", err)
	}
	if elapsed < delay {
		t.Fatalf("GenerateContent returned after %s, before slow tool delay %s elapsed", elapsed, delay)
	}
	want := "SLOW_PI_TOOL_OK_" + token + "_PI_SLOW_SECRET"
	if got := strings.TrimSpace(resp.Choices[0].Content); !strings.Contains(got, want) {
		t.Fatalf("content = %q, want slow MCP result %q", got, want)
	}
}

func TestPiCLIRealSlowToolLiveInputAndCancellationContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-live-cancel-" + piRandomHex(4)
	workDir := t.TempDir()
	token := "PI_CANCEL_" + piRandomHex(5)
	liveToken := "PI_LIVE_" + piRandomHex(5)
	markerPath := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiSlowMCPServer(t, "PI_CANCEL_SECRET", markerPath))

	parentCtx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan piRealResult, 1)
	startupErrCh := make(chan error, 1)
	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Use declared MCP tools when asked. Do not answer until slow tools return."),
			llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, fmt.Sprintf("Call the api-bridge MCP tool slow_contract with token %s and delay_ms 60000. Do not answer until the tool returns. Then reply exactly with the tool output text.", token)),
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithMCPConfig(mcpConfig),
		)
		out := piRealResult{err: err}
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

	session := waitForPiRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForPiRealFile(t, markerPath, "slow MCP tool call start", 90*time.Second, resultCh)
	waitForPiRealPaneCondition(t, session.tmuxSessionName, "active slow MCP tool", 20*time.Second, resultCh, func(pane string) bool {
		return strings.TrimSpace(pane) != ""
	})
	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while slow MCP tool was active: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while slow MCP tool was active; content=%q", got.content)
	case <-time.After(2 * time.Second):
	}

	liveMessage := "PREVALIDATION_FAILED_" + liveToken + "\nKeep working after the active tool call finishes."
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := SendPiInteractiveInput(sendCtx, ownerSessionID, liveMessage); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendPiInteractiveInput error = %v", err)
	}
	sendCancel()
	waitForPiRealPaneContains(t, session.tmuxSessionName, liveToken, 15*time.Second, startupErrCh)
	select {
	case got := <-resultCh:
		cancel()
		if got.err != nil {
			t.Fatalf("GenerateContent returned while live input was queued during slow MCP tool: err=%v content=%q", got.err, got.content)
		}
		t.Fatalf("GenerateContent completed while live input was queued during slow MCP tool; content=%q", got.content)
	case <-time.After(2 * time.Second):
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
	if active, ok := activePiInteractiveSession(ownerSessionID); ok {
		t.Fatalf("canceled Pi persistent session remained registered: %#v", active)
	}
	if !waitForPiTmuxSessionGone(context.Background(), session.tmuxSessionName, 10*time.Second) {
		t.Fatalf("canceled Pi tmux session %s was not closed", session.tmuxSessionName)
	}

	retryToken := "PI_CANCEL_RETRY_" + piRandomHex(5)
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer retryCancel()
	retry, err := adapter.GenerateContent(retryCtx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Do not use tools. Reply exactly as instructed."),
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Reply exactly: "+retryToken),
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("retry GenerateContent after cancellation error = %v", err)
	}
	if got := strings.TrimSpace(retry.Choices[0].Content); !strings.Contains(got, retryToken) {
		t.Fatalf("retry content = %q, want %s", got, retryToken)
	}
	retrySession, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || retrySession.tmuxSessionName == "" || retrySession.tmuxSessionName == session.tmuxSessionName {
		t.Fatalf("retry should start a fresh registered session, before=%q after=%#v ok=%v", session.tmuxSessionName, retrySession, ok)
	}
}

// TestPiCLIRealInteractiveLiveInputProcessesQueuedFollowupContract verifies the
// property the steer-vs-queue removal depends on for the Pi CLI: a message
// delivered mid-turn (while Pi is busy in a slow MCP tool) is queued by the Pi
// CLI ITSELF and processed once the current turn completes. The follow-up is
// delivered via raw SendPiInteractiveInput (bypassing the server queue), so a
// processed follow-up proves Pi's own native queue did the work. Unlike the
// cancellation contract, GenerateContent is allowed to COMPLETE.
func TestPiCLIRealInteractiveLiveInputProcessesQueuedFollowupContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-live-process-" + piRandomHex(4)
	workDir := t.TempDir()
	token := "PI_LIVE_PROCESS_" + piRandomHex(5)
	firstDone := "PI_FIRST_DONE_" + piRandomHex(5)
	liveAck := "PI_LIVE_ACK_" + piRandomHex(5)
	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiSlowMCPServer(t, "PI_LIVE_SECRET", slowToolMarker))

	parentCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resultCh := make(chan piRealResult, 1)
	startupErrCh := make(chan error, 1)
	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Use declared MCP tools when asked. Do not answer until slow tools return. If a follow-up user message arrives while you are working, handle it after the current tool call finishes."),
			llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, fmt.Sprintf("Call the api-bridge MCP tool slow_contract with token %s and delay_ms 8000. Do not answer until the tool returns. Then reply exactly %s.", token, firstDone)),
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithMCPConfig(mcpConfig),
		)
		out := piRealResult{err: err}
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

	session := waitForPiRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForPiRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)

	// Deliver the follow-up WHILE Pi is busy in the slow tool, via the raw
	// adapter path (no server queue in between).
	liveMessage := fmt.Sprintf("Follow-up task: after the current answer completes, also reply exactly %s and nothing else.", liveAck)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := SendPiInteractiveInput(sendCtx, ownerSessionID, liveMessage); err != nil {
		sendCancel()
		cancel()
		t.Fatalf("SendPiInteractiveInput error = %v", err)
	}
	sendCancel()

	// Let GenerateContent COMPLETE (do NOT cancel). The queued follow-up must be
	// processed natively by Pi and surface in the final content.
	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("GenerateContent error = %v (content=%q)", got.err, got.content)
		}
		if !strings.Contains(got.content, liveAck) {
			pane, _ := capturePiPane(context.Background(), session.tmuxSessionName)
			t.Fatalf("queued follow-up was NOT processed by the Pi CLI; final content=%q\npane:\n%s", got.content, pane)
		}
		t.Logf("OK: Pi CLI natively queued + processed the mid-turn follow-up (found %s in final content)", liveAck)
	case <-time.After(3 * time.Minute):
		pane, _ := capturePiPane(context.Background(), session.tmuxSessionName)
		t.Fatalf("timed out waiting for Pi to process the queued live input; pane:\n%s", pane)
	}
}

// TestPiCLIRealProjectTrustLoadsProjectLocalResourceContract is the reproducer
// for the "workspace trust" bug. pi only loads project-local `.pi` resources
// when the (dynamic temp) workspace is TRUSTED for the run. The adapter used to
// launch pi with `--no-approve` ("Ignore project-local files for this run"),
// which left every dynamic temp workspace untrusted, so project-local `.pi`
// resources were silently ignored.
//
// We use `.pi/APPEND_SYSTEM.md` as the trust signal because it is gated ONLY by
// project trust (per docs/security.md it is one of the resources that "require
// trust"), and it is NOT disabled by any of the hermetic flags the adapter
// keeps: `--no-extensions`/`--no-skills` only gate `.pi/extensions`/`.pi/skills`,
// and `--no-context-files` only gates AGENTS.md/CLAUDE.md context files, not the
// SYSTEM.md / APPEND_SYSTEM.md system-prompt files. Per-run `--approve` /
// `--no-approve` overrides project trust regardless of mode and saved decisions,
// so this test isolates exactly the trust flag.
//
// The appended system prompt declares an unguessable random passcode. The user
// prompt asks for that passcode WITHOUT ever stating it, so the model can only
// answer correctly if pi actually loaded the trusted project-local file:
//   - before the fix (--no-approve): file ignored -> passcode absent  -> RED
//   - after fix   (--approve):     file loaded   -> passcode present -> GREEN
//
// The passcode is random hex, so a false GREEN (model guessing it) is
// effectively impossible. Routed through the adapter so it guards the real
// launch path.
func TestPiCLIRealProjectTrustLoadsProjectLocalResourceContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-trust-" + piRandomHex(4)
	workDir := t.TempDir()

	passcode := "PI_TRUST_PASSCODE_" + piRandomHex(8)
	piDir := filepath.Join(workDir, ".pi")
	if err := os.MkdirAll(piDir, 0o700); err != nil {
		t.Fatalf("create project-local .pi dir: %v", err)
	}
	appendSystem := "This workspace has a configured project passcode: " + passcode + "\n" +
		"When the user asks for the project passcode, reply with exactly that passcode and nothing else."
	if err := os.WriteFile(filepath.Join(piDir, "APPEND_SYSTEM.md"), []byte(appendSystem), 0o600); err != nil {
		t.Fatalf("write project-local .pi/APPEND_SYSTEM.md: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Do not use tools. Answer only from your instructions."),
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "What is the project passcode configured for this workspace? Reply with exactly the passcode and nothing else. Do not guess."),
	},
		WithInteractiveSessionID(ownerSessionID),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent project-trust error = %v", err)
	}
	got := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(got, passcode) {
		t.Fatalf("content = %q, want project passcode %s from trusted project-local .pi/APPEND_SYSTEM.md; "+
			"absent passcode means the workspace was launched untrusted (pi ignored project-local .pi resources)", got, passcode)
	}
}

func requireRealPiCLIContractE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_PI_CLI_REAL_E2E") == "" {
		t.Skip("set RUN_PI_CLI_REAL_E2E=1 to run real Pi CLI tmux contract tests")
	}
	for _, bin := range []string{"tmux", "node"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Pi CLI tests require %s in PATH: %v", bin, err)
		}
	}
	if _, _, err := piCommandPrefix(); err != nil {
		t.Skip(err)
	}
	if firstNonEmptyPiTestEnv("GEMINI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY, GOOGLE_API_KEY, or PI_API_KEY is required for real Pi CLI contract tests")
	}
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())
}

func newRealPiCLIAdapter(t *testing.T) *PiCLIAdapter {
	t.Helper()
	model := strings.TrimSpace(os.Getenv("PI_CLI_REAL_CONTRACT_MODEL"))
	if model == "" {
		model = DefaultModelID
	}
	return NewPiCLIAdapter(firstNonEmptyPiTestEnv("GEMINI_API_KEY", "GOOGLE_API_KEY", "PI_API_KEY"), model, &mockLogger{})
}

func assertPiStreamHasTerminalContentAndStatusLine(t *testing.T, streamChan <-chan llmtypes.StreamChunk) *llmtypes.StatusLine {
	t.Helper()
	terminalCount := 0
	contentCount := 0
	var statusLine *llmtypes.StatusLine
	for chunk := range streamChan {
		if chunk.Type == llmtypes.StreamChunkTypeTerminal {
			terminalCount++
		}
		if chunk.Type == llmtypes.StreamChunkTypeContent {
			contentCount++
		}
		if chunk.Type == llmtypes.StreamChunkTypeStatusLine {
			statusLine = chunk.StatusLine
		}
	}
	if terminalCount == 0 {
		t.Fatal("expected Pi terminal snapshots while waiting for prompt/response")
	}
	if contentCount == 0 {
		t.Fatal("expected Pi parsed content chunks")
	}
	testcontracts.AssertStatusLineContract(t, statusLine, "pi-cli", true)
	if strings.TrimSpace(statusLine.Model) == "" {
		t.Fatal("expected Pi statusline to include selected provider/model route")
	}
	if got := statusLine.Metadata["pi_token_usage_source"]; got != "transcript-file" {
		t.Fatalf("statusline pi_token_usage_source = %#v, want transcript-file", got)
	}
	return statusLine
}

func assertPiResponseHasTranscriptUsage(t *testing.T, resp *llmtypes.ContentResponse) {
	t.Helper()
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0] == nil || resp.Choices[0].GenerationInfo == nil {
		t.Fatalf("missing Pi GenerationInfo: %#v", resp)
	}
	gi := resp.Choices[0].GenerationInfo
	if gi.InputTokens == nil || *gi.InputTokens == 0 || gi.OutputTokens == nil || *gi.OutputTokens == 0 || gi.TotalTokens == nil || *gi.TotalTokens == 0 {
		t.Fatalf("expected transcript-backed token usage, got GenerationInfo=%#v", gi)
	}
	additional := gi.Additional
	if got := additional["pi_token_usage_source"]; got != "transcript-file" {
		t.Fatalf("pi_token_usage_source = %#v, want transcript-file; additional=%#v", got, additional)
	}
	if got, _ := additional["pi_transcript_file"].(string); strings.TrimSpace(got) == "" {
		t.Fatalf("missing pi_transcript_file in additional=%#v", additional)
	}
	if got, ok := additional["cost_usd"].(float64); !ok || got <= 0 {
		t.Fatalf("cost_usd = %#v, want positive float64; additional=%#v", additional["cost_usd"], additional)
	}
	intermediate, ok := llmtypes.ExtractCodingProviderIntermediateMessages(gi)
	if !ok || len(intermediate.Messages) < 2 {
		t.Fatalf("expected Pi transcript conversation messages, got %#v ok=%v", intermediate, ok)
	}
}

func waitForPiRealActiveSession(t *testing.T, ownerSessionID string, timeout time.Duration, errCh <-chan error) *piInteractiveSession {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before active Pi session was available: %v", err)
		default:
		}
		if session, ok := activePiInteractiveSession(ownerSessionID); ok {
			return session
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for real Pi interactive session %q", ownerSessionID)
	return nil
}

func waitForPiRealFile(t *testing.T, path, label string, timeout time.Duration, errCh <-chan piRealResult) {
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

func waitForPiRealPaneContains(t *testing.T, tmuxSession, want string, timeout time.Duration, errCh <-chan error) {
	t.Helper()
	waitForPiRealPaneCondition(t, tmuxSession, "contains "+want, timeout, nil, func(pane string) bool {
		select {
		case err := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane contained %q: %v", want, err)
		default:
		}
		return strings.Contains(pane, want)
	})
}

func waitForPiRealPaneCondition(t *testing.T, tmuxSession, label string, timeout time.Duration, errCh <-chan piRealResult, matches func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case got := <-errCh:
			t.Fatalf("GenerateContent returned before tmux pane matched %s: err=%v content=%q", label, got.err, got.content)
		default:
		}
		pane, err := capturePiPane(context.Background(), tmuxSession)
		if err == nil && matches(pane) {
			return pane
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane, _ := capturePiPane(context.Background(), tmuxSession)
	t.Fatalf("timed out waiting for Pi tmux pane to match %s; latest pane:\n%s", label, pane)
	return ""
}

func waitForPiTmuxSessionGone(ctx context.Context, sessionName string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !piTmuxSessionExists(ctx, sessionName) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return !piTmuxSessionExists(ctx, sessionName)
}

func writePiReportCWDMCPServer(t *testing.T) string {
	t.Helper()
	return writePiMCPServer(t, "PI_CWD", "")
}

func writePiEchoMCPServer(t *testing.T, prefix string) string {
	t.Helper()
	return writePiMCPServer(t, prefix, "")
}

func writePiSlowMCPServer(t *testing.T, secret, markerPath string) string {
	t.Helper()
	return writePiMCPServer(t, secret, markerPath)
}

func writePiMCPServer(t *testing.T, prefixOrSecret, markerPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi-contract-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const prefixOrSecret = %q;
const markerPath = %q;
function send(msg) { process.stdout.write(JSON.stringify(msg) + "\n"); }
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
rl.on("line", async (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.method === "initialize") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "api-bridge", version: "1.0.0" } } });
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "ping") {
    return send({ jsonrpc: "2.0", id: msg.id, result: {} });
  }
  if (msg.method === "tools/list") {
    return send({ jsonrpc: "2.0", id: msg.id, result: { tools: [
      {
        name: "report_cwd",
        description: "Return the current working directory of this MCP server process.",
        inputSchema: { type: "object", properties: {}, additionalProperties: false }
      },
      {
        name: "echo_contract",
        description: "Return a deterministic Pi bridge contract token.",
        inputSchema: { type: "object", properties: { token: { type: "string" } }, required: ["token"] }
      },
      {
        name: "slow_contract",
        description: "Sleep for the requested delay and return a deterministic Pi bridge contract token.",
        inputSchema: { type: "object", properties: { token: { type: "string" }, delay_ms: { type: "number" } }, required: ["token", "delay_ms"] }
      }
    ] } });
  }
  if (msg.method === "tools/call" && msg.params) {
    const args = msg.params.arguments || {};
    if (msg.params.name === "report_cwd") {
      return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "PI_MCP_CWD:" + process.cwd() }], isError: false } });
    }
    if (msg.params.name === "echo_contract") {
      const token = String(args.token || "");
      return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: prefixOrSecret + "_" + token }], isError: false } });
    }
    if (msg.params.name === "slow_contract") {
      const token = String(args.token || "");
      const delayMS = Math.max(0, Math.min(120000, Number(args.delay_ms || 0)));
      if (markerPath) fs.writeFileSync(markerPath, "started");
      await sleep(delayMS);
      return send({ jsonrpc: "2.0", id: msg.id, result: { content: [{ type: "text", text: "SLOW_PI_TOOL_OK_" + token + "_" + prefixOrSecret }], isError: false } });
    }
  }
  if (msg.id !== undefined) {
    send({ jsonrpc: "2.0", id: msg.id, error: { code: -32601, message: "method not found" } });
  }
});
`, prefixOrSecret, markerPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write Pi MCP server: %v", err)
	}
	return path
}
