package musecli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/testcontracts"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Live P0 certification turns for muse-cli. All tests in this file run
// against the real CLI with Meta auth (stored `muse login`) on the
// contributor tier, gated by -coding-cli-p0-live. Budgets are generous on
// purpose: a slow model must still certify, and a timeout fails loudly with
// the latest pane/log attached rather than passing vacuously.

func requireMetaMuseCLIE2E(t *testing.T) {
	t.Helper()
	requireRealMuseCLIE2E(t)
	if os.Getenv(EnvMuseCLIExecProvider) != "" {
		t.Skip("live Meta certs need the real provider, not " + os.Getenv(EnvMuseCLIExecProvider))
	}
}

// museLiveAdapter returns an adapter on CLI defaults: empty modelID means
// no --model flag, so the CLI resolves muse-spark-1.3-contributor itself
// (single model everywhere; the constructor's second arg is the model id,
// not the provider name).
func museLiveAdapter() *MuseCLIAdapter {
	return NewMuseCLIAdapter("", "", museTestLogger{})
}

// museDrainStream non-blockingly collects everything buffered on ch.
func museDrainStream(ch chan llmtypes.StreamChunk) []llmtypes.StreamChunk {
	var out []llmtypes.StreamChunk
	for {
		select {
		case c := <-ch:
			out = append(out, c)
		default:
			return out
		}
	}
}

// museTranscriptFinal returns the last assistant text for a native session,
// failing the test when the sidecar is missing or empty.
func museTranscriptFinal(t *testing.T, nativeSessionID string) string {
	t.Helper()
	path := museSessionLogPath(nativeSessionID)
	if path == "" {
		t.Fatalf("no session log for native session %s", nativeSessionID)
	}
	transcript, ok := readMuseTranscriptMessages(path, "")
	if !ok {
		t.Fatalf("unreadable session log at %s", path)
	}
	final := museLastAssistantText(transcript)
	if strings.TrimSpace(final) == "" {
		t.Fatalf("no assistant text in %s", path)
	}
	return final
}

// TestMuseCLIRealExecTurnContract is the exec-lane P0: one Meta turn through
// the adapter proves completion detection (clean return, no error), final
// extraction (returned text == sidecar transcript's last assistant text),
// and the tokens/costs path (usage + shadow estimate attached live).
func TestMuseCLIRealExecTurnContract(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	adapter := museLiveAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	streamChan := make(chan llmtypes.StreamChunk, 256)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{
			llmtypes.TextContent{Text: "Do not use any tools. Answer directly with exactly what is asked."}}},
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{
			llmtypes.TextContent{Text: "Reply with exactly: PINEAPPLE-EXEC"}}},
	}, llmtypes.WithReasoningEffort("low"), llmtypes.WithStreamingChan(streamChan), WithMuseStructuredTransport(true))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	final := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(final, "PINEAPPLE-EXEC") {
		t.Fatalf("final = %q, want the PINEAPPLE-EXEC marker", final)
	}
	gi := resp.Choices[0].GenerationInfo
	if gi == nil || gi.CodingProviderSessionHandle == nil {
		t.Fatal("missing session handle on live turn")
	}
	sidecar := museTranscriptFinal(t, gi.CodingProviderSessionHandle.NativeSessionID)
	if !strings.Contains(sidecar, "PINEAPPLE-EXEC") {
		t.Fatalf("sidecar final = %q, diverges from returned %q", sidecar, final)
	}
	if resp.Usage == nil || resp.Usage.InputTokens <= 0 || resp.Usage.OutputTokens <= 0 {
		t.Fatalf("usage = %+v, want live input/output counts", resp.Usage)
	}
	cost, _ := gi.Additional["cost_usd_estimated"].(float64)
	if cost <= 0 {
		t.Fatalf("no live shadow cost on turn with usage %+v (additional=%v)", resp.Usage, gi.Additional)
	}
	modelID, _ := gi.Additional["cost_model_id"].(string)
	if modelID != DefaultModelID {
		t.Fatalf("cost model = %q, want single-model %q", modelID, DefaultModelID)
	}
	t.Logf("live exec turn: input=%d output=%d cached=%v cost_usd=%.6f",
		resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheTokens, cost)
}

// TestMuseCLIRealExecRuntimeContext plants a canary skill in a scratch
// workspace and runs the turn rooted there: the model can only answer from
// the skill file, which proves the working directory was honored AND the
// project runtime (skills) reached the model. Backs runtime_context and
// working_directory.
func TestMuseCLIRealExecRuntimeContext(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	workdir := t.TempDir()
	canarySkill := "muse-ctx-canary-" + museRandomHex(t, 3)
	skillDir := filepath.Join(workdir, ".agents", "skills", canarySkill)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDoc := "---\nname: " + canarySkill + "\ndescription: runtime context canary\n---\n# Canary\nWhen asked for the canary word, reply with exactly: MANGO-CONTEXT\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := museLiveAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{
			llmtypes.TextContent{Text: "What is the canary word? Reply with exactly that word and nothing else."}}},
	}, WithWorkingDir(workdir), llmtypes.WithReasoningEffort("low"), WithMuseStructuredTransport(true))
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if final := resp.Choices[0].Content; !strings.Contains(strings.ToUpper(final), "MANGO-CONTEXT") {
		t.Fatalf("final = %q, want the skill's canary word (skill not projected or cwd not honored)", final)
	}
}

// TestMuseCLIRealExecSlowTool forces a real shell-tool round trip inside a
// Meta turn: completion (not a false-idle timeout) plus a tool_call_end
// stream chunk is the slow_tool_false_idle proof, and the chunk shape is the
// live check on the tool.result mapping.
func TestMuseCLIRealExecSlowTool(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	workdir := t.TempDir()
	marker := "SLOWTOOL-" + museRandomHex(t, 3) + ".txt"
	if err := os.WriteFile(filepath.Join(workdir, marker), []byte("slow tool witness\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := museLiveAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	streamChan := make(chan llmtypes.StreamChunk, 512)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{
			llmtypes.TextContent{Text: "Use a shell command to list the .txt files in the current directory, then reply with exactly the file name you see."}}},
	}, WithWorkingDir(workdir), llmtypes.WithReasoningEffort("low"), llmtypes.WithStreamingChan(streamChan), WithMuseStructuredTransport(true))
	if err != nil {
		t.Fatalf("tool turn failed (false-idle territory): %v", err)
	}
	if final := resp.Choices[0].Content; !strings.Contains(final, marker) {
		t.Fatalf("final = %q, want the listed file %q", final, marker)
	}
	var toolStarts, toolEnds []llmtypes.StreamChunk
	for _, c := range museDrainStream(streamChan) {
		switch c.Type {
		case llmtypes.StreamChunkTypeToolCallStart:
			toolStarts = append(toolStarts, c)
		case llmtypes.StreamChunkTypeToolCallEnd:
			toolEnds = append(toolEnds, c)
		}
	}
	if len(toolEnds) == 0 {
		t.Fatal("tool turn completed but no tool_call_end chunk streamed")
	}
	// The wire has no tool-started event, so the lane synthesizes the start
	// immediately before its end: the pair must share id, name, and args or
	// product rows render an orphan end.
	end := toolEnds[0]
	var start *llmtypes.StreamChunk
	for i, c := range toolStarts {
		if c.ToolCallID == end.ToolCallID {
			start = &toolStarts[i]
			break
		}
	}
	if start == nil {
		t.Fatalf("tool_call_end %q streamed with no matching synthetic start", end.ToolCallID)
	}
	if start.ToolName != end.ToolName || start.ToolArgs != end.ToolArgs {
		t.Fatalf("pair mismatch: start (%q, %q) vs end (%q, %q)", start.ToolName, start.ToolArgs, end.ToolName, end.ToolArgs)
	}
	t.Logf("tool_call pair: tool=%q call=%q args=%q", end.ToolName, end.ToolCallID, end.ToolArgs)
}

// museLiveBootTUI launches a Meta TUI in workdir and waits for the settled
// idle pane. The session is killed on test cleanup.
func museLiveBootTUI(t *testing.T, ctx context.Context, workdir string) string {
	t.Helper()
	session := "mlp-muse-live-" + museRandomHex(t, 3)
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", session).Run()
	})
	if err := museLaunchTUI(ctx, workdir, session, "meta"); err != nil {
		t.Fatalf("launch TUI: %v", err)
	}
	if _, err := museWaitSettled(ctx, session, 90*time.Second); err != nil {
		t.Fatalf("TUI never settled: %v", err)
	}
	return session
}

// museLiveSubmitTurn sends one prompt, verifies intake through the session
// log, and waits for quiescence, returning the finished pane and the turn's
// session log path.
func museLiveSubmitTurn(t *testing.T, ctx context.Context, session, prompt string) (pane, logPath string) {
	t.Helper()
	turnStart := time.Now()
	if err := museSendPrompt(ctx, session, prompt); err != nil {
		t.Fatalf("send prompt: %v", err)
	}
	_, path, err := museWaitIntake(ctx, session, turnStart, promptSnippet(prompt))
	if err != nil {
		t.Fatalf("prompt never taken in: %v", err)
	}
	after, err := museWaitTurnQuiescent(ctx, session, path, 5*time.Minute)
	if err != nil {
		t.Fatalf("turn never completed: %v", err)
	}
	return after, path
}

func museLiveLogText(t *testing.T, logPath string) string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	return string(raw)
}

// TestMuseCLIRealPersistentSession proves the pooled lane the orchestrator
// uses for terminal attach: launch-only boots one TUI and hands back its
// tmux name, two turns run on that SAME live session (same tmux name, same
// native session), and KillMusePersistentSession tears it down. Backs the
// builder-tmux work (no new cert: continuity itself is covered by the
// multi-turn cert; this pins ownership).
func TestMuseCLIRealPersistentSession(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	adapter := museLiveAdapter()
	owner := "mlp-persist-" + museRandomHex(t, 3)
	t.Cleanup(func() { KillMusePersistentSession(owner) })
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	// One workdir for the whole test: the pool relaunches when it moves,
	// so rotating temp dirs would defeat the reuse assertion below.
	workdir := t.TempDir()
	persistent := func(o ...llmtypes.CallOption) []llmtypes.CallOption {
		return append([]llmtypes.CallOption{
			WithPersistentInteractiveSession(true),
			WithInteractiveSessionID(owner),
			WithWorkingDir(workdir),
			llmtypes.WithReasoningEffort("low"),
		}, o...)
	}

	launch, err := adapter.GenerateContent(ctx, nil,
		append(persistent(), llmtypes.WithCodingProviderLaunchOnly())...)
	if err != nil {
		t.Fatalf("launch-only: %v", err)
	}
	tmuxName := launch.Choices[0].GenerationInfo.CodingProviderSessionHandle.TmuxSession
	if tmuxName == "" {
		t.Fatal("launch-only returned no tmux session to attach the terminal to")
	}
	if !museTmuxSessionAlive(ctx, tmuxName) {
		t.Fatalf("pooled TUI %q not alive after launch", tmuxName)
	}

	turn := func(word string) (string, string) {
		t.Helper()
		resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Reply with exactly " + word + " and nothing else."}}},
		}, persistent()...)
		if err != nil {
			t.Fatalf("persistent turn: %v", err)
		}
		h := resp.Choices[0].GenerationInfo.CodingProviderSessionHandle
		if h.TmuxSession != tmuxName {
			t.Fatalf("turn ran on %q, want pooled %q", h.TmuxSession, tmuxName)
		}
		if !strings.Contains(resp.Choices[0].Content, word) {
			t.Fatalf("answer = %q, want %q", resp.Choices[0].Content, word)
		}
		return h.NativeSessionID, resp.Choices[0].Content
	}
	token1 := "PERS1-" + museRandomHex(t, 3)
	native1, _ := turn(token1)
	token2 := "PERS2-" + museRandomHex(t, 3)
	native2, _ := turn(token2)
	if native1 == "" || native1 != native2 {
		t.Fatalf("native sessions %q vs %q: pooled turns must share one native session", native1, native2)
	}
	KillMusePersistentSession(owner)
	if museTmuxSessionAlive(ctx, tmuxName) {
		t.Fatalf("pooled TUI %q still alive after kill", tmuxName)
	}
}

// TestMuseCLIRealTmuxMultiTurn drives two turns through ONE TUI session:
// per-turn completion detection (re-settle after each submit), continuity
// (both answers in one native transcript), and transcript final extraction.
// Backs multi_turn and done_detection.
func TestMuseCLIRealTmuxMultiTurn(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	session := museLiveBootTUI(t, ctx, t.TempDir())

	token1 := "MULTI1-" + museRandomHex(t, 3)
	_, log1 := museLiveSubmitTurn(t, ctx, session, "Reply with exactly "+token1+" and nothing else.")
	token2 := "MULTI2-" + museRandomHex(t, 3)
	after2, log2 := museLiveSubmitTurn(t, ctx, session, "Reply with exactly "+token2+" and nothing else.")
	if log1 != log2 {
		t.Fatalf("turns landed in different native sessions: %s vs %s (no continuity)", log1, log2)
	}
	logText := museLiveLogText(t, log1)
	if !strings.Contains(logText, token1) || !strings.Contains(logText, token2) {
		t.Fatal("native transcript does not hold both turns' prompts")
	}
	transcript, ok := readMuseTranscriptMessages(log1, "")
	if !ok {
		t.Fatal("unreadable native transcript")
	}
	if final := museLastAssistantText(transcript); !strings.Contains(final, token2) {
		t.Fatalf("last assistant text = %q, want turn 2 answer %q", final, token2)
	}
	if !museTUIAtPrompt(after2) {
		t.Fatal("post-turn-2 pane not settled idle")
	}
}

// TestMuseCLIRealTmuxReplyFidelity runs the formatting prompt through one
// bounded TUI turn and judges the transcript extraction against the wrapped
// pane. Backs reply_formatting_fidelity (agent-review sign-off included).
func TestMuseCLIRealTmuxReplyFidelity(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	session := museLiveBootTUI(t, ctx, t.TempDir())

	// Single line on purpose: the TUI submits on pasted newlines, so a
	// multi-line prompt fragments into several turns (proven live
	// 2026-09-10: the 120-char cross-newline snippet never matched intake).
	// The structure under test is what the MODEL must produce, not what we
	// paste in.
	//
	// The 300-char minimum is load-bearing for the agent-review
	// fingerprint, not decoration: the pane buffer keeps boot-width (200)
	// chrome lines, so the authored lines must clear 200 for the
	// unwrapped bit to hold run after run. Narrowing the pane to 80 makes
	// the deterministic wrap comparison decisive on top of that.
	token := "FMT_" + museRandomHex(t, 5)
	if err := exec.CommandContext(ctx, "tmux", "resize-window", "-t", session, "-x", "80", "-y", "24").Run(); err != nil {
		t.Fatalf("resize pane to 80 cols: %v", err)
	}
	time.Sleep(2 * time.Second)
	prompt := "TEXT FORMATTING test " + token + ", no tools. Output ONLY the markdown described next, no preamble, no closing. " +
		"First: one paragraph about " + token + " as a single line of at least 300 characters so a narrow terminal must wrap it. " +
		"Then exactly one blank line. " +
		"Then a second paragraph about " + token + ", again one single line of at least 300 characters. " +
		"Then three lines, each starting with '- ', three bullets about " + token + ", nothing after."
	_, logPath := museLiveSubmitTurn(t, ctx, session, prompt)
	// Drop scrollback (boot-width chrome) so the captured screen is the
	// 80-col turn rendering, then re-capture the settled visible pane.
	if err := exec.CommandContext(ctx, "tmux", "clear-history", "-t", session).Run(); err != nil {
		t.Fatalf("clear scrollback: %v", err)
	}
	pane, err := museTmuxCapturePane(ctx, session)
	if err != nil {
		t.Fatalf("re-capture pane: %v", err)
	}
	if !museTUIAtPrompt(pane) {
		t.Fatalf("pane not settled after scrollback clear:\n%s", pane)
	}
	transcript, ok := readMuseTranscriptMessages(logPath, "")
	if !ok {
		t.Fatal("unreadable native transcript")
	}
	content := strings.TrimSpace(museLastAssistantText(transcript))
	testcontracts.AssertFinalAnswerPreservesStructure(t, testcontracts.ReplyFormattingCase{
		Provider:       "muse-cli",
		TmuxScreen:     pane,
		Extracted:      content,
		WantParagraphs: 2,
		WantBullets:    3,
		UserGoal:       "Return the two long paragraphs and the three-item list with their markdown structure intact.",
		ExpectedNote: "Live muse TUI turn. The pane capture is expected to be hard-wrapped; the extracted answer must NOT be, " +
			"because it comes from the session.jsonl transcript via museLastAssistantText rather than from the screen.",
	})
}

// TestMuseCLIRealTmuxLiveInput queues a follow-up while a long turn is
// busy: the follow-up must be accepted mid-turn and answered after it.
// Backs live_input and busy_live_input.
func TestMuseCLIRealTmuxLiveInput(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	session := museLiveBootTUI(t, ctx, t.TempDir())

	token := "QUEUED-" + museRandomHex(t, 3)
	essay := "Write a 400-word essay on the history of concrete with extensive detail. Token " + museRandomHex(t, 2) + "."
	essayStart := time.Now()
	if err := museSendPrompt(ctx, session, essay); err != nil {
		t.Fatalf("send long turn: %v", err)
	}
	if _, _, err := museWaitIntake(ctx, session, essayStart, promptSnippet(essay)); err != nil {
		t.Fatalf("long turn never taken in: %v", err)
	}
	time.Sleep(8 * time.Second)
	pane, err := museTmuxCapturePane(ctx, session)
	if err != nil {
		t.Fatalf("capture busy pane: %v", err)
	}
	if museTUIAtPrompt(pane) && musePaneStable(ctx, session, pane) {
		t.Log("long turn finished before the 8s queue window; follow-up still exercises live input on an idle pane")
	}
	queueStart := time.Now()
	if err := museSendPrompt(ctx, session, "Reply with exactly "+token+" and nothing else."); err != nil {
		t.Fatalf("send queued follow-up: %v", err)
	}
	_, logPath, err := museWaitIntake(ctx, session, queueStart, token)
	if err != nil {
		t.Fatalf("queued follow-up never taken in: %v", err)
	}
	deadline := time.Now().Add(6 * time.Minute)
	for {
		pane, err = museTmuxCapturePane(ctx, session)
		if err != nil {
			t.Fatalf("capture pane: %v", err)
		}
		if strings.Contains(pane, token) && museTUIAtPrompt(pane) &&
			musePaneStable(ctx, session, pane) && museLogQuietSince(logPath, 5*time.Second) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued follow-up never answered; latest pane:\n%s", pane)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait canceled: %v", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if logText := museLiveLogText(t, logPath); !strings.Contains(logText, token) {
		t.Fatal("native transcript missing the queued follow-up")
	}
}

// TestMuseCLIRealTmuxCancellation interrupts a running turn with Ctrl-C:
// the TUI must report the interrupt, return to idle, and still take the
// next turn. Backs cancellation.
func TestMuseCLIRealTmuxCancellation(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	session := museLiveBootTUI(t, ctx, t.TempDir())

	essayStart := time.Now()
	if err := museSendPrompt(ctx, session, "Write a 600-word essay on glass manufacturing history with extensive detail."); err != nil {
		t.Fatalf("send long turn: %v", err)
	}
	if _, _, err := museWaitIntake(ctx, session, essayStart, "glass manufacturing"); err != nil {
		t.Fatalf("long turn never taken in: %v", err)
	}
	// Prove the model is mid-answer before interrupting: the "◆" answer
	// marker must be on screen. Interrupting typed-but-unsubmitted input
	// just clears the line (no turn runs, no marker) — that failure mode
	// wasted a 90s poll in the first version of this test.
	streamDeadline := time.Now().Add(90 * time.Second)
	for {
		pane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			t.Fatalf("capture pane: %v", err)
		}
		if strings.Contains(pane, "◆") {
			break
		}
		if time.Now().After(streamDeadline) {
			t.Fatalf("long turn never started streaming; latest pane:\n%s", pane)
		}
		time.Sleep(time.Second)
	}
	if err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "C-c").Run(); err != nil {
		t.Fatalf("send Ctrl-C: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	var pane string
	for {
		p, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			t.Fatalf("capture pane: %v", err)
		}
		pane = p
		if strings.Contains(pane, "Interrupt") && museTUIAtPrompt(pane) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no interrupt marker after Ctrl-C; latest pane:\n%s", pane)
		}
		time.Sleep(time.Second)
	}
	token := "AFTERCANCEL-" + museRandomHex(t, 3)
	after, logPath := museLiveSubmitTurn(t, ctx, session, "Reply with exactly "+token+" and nothing else.")
	if !strings.Contains(museLiveLogText(t, logPath), token) {
		t.Fatal("TUI did not take a fresh turn after cancellation")
	}
	if !museTUIAtPrompt(after) {
		t.Fatal("pane not settled after post-cancel turn")
	}
}

// TestMuseCLIRealTmuxParallelIsolation runs two TUI sessions in two
// workdirs at once with distinct prompts: each native transcript must hold
// its own answer and never the other's. Backs parallel_isolation.
func TestMuseCLIRealTmuxParallelIsolation(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	workA, workB := t.TempDir(), t.TempDir()
	sessA := "mlp-muse-par-a-" + museRandomHex(t, 3)
	sessB := "mlp-muse-par-b-" + museRandomHex(t, 3)
	for _, s := range []string{sessA, sessB} {
		s := s
		t.Cleanup(func() {
			_ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", s).Run()
		})
	}
	if err := museLaunchTUI(ctx, workA, sessA, "meta"); err != nil {
		t.Fatalf("launch A: %v", err)
	}
	if err := museLaunchTUI(ctx, workB, sessB, "meta"); err != nil {
		t.Fatalf("launch B: %v", err)
	}
	if _, err := museWaitSettled(ctx, sessA, 90*time.Second); err != nil {
		t.Fatalf("A never settled: %v", err)
	}
	if _, err := museWaitSettled(ctx, sessB, 90*time.Second); err != nil {
		t.Fatalf("B never settled: %v", err)
	}
	tokenA := "PARA-" + museRandomHex(t, 3)
	tokenB := "PARB-" + museRandomHex(t, 3)
	startA := time.Now()
	if err := museSendPrompt(ctx, sessA, "Reply with exactly "+tokenA+" and nothing else."); err != nil {
		t.Fatalf("send A: %v", err)
	}
	startB := time.Now()
	if err := museSendPrompt(ctx, sessB, "Reply with exactly "+tokenB+" and nothing else."); err != nil {
		t.Fatalf("send B: %v", err)
	}
	_, logA, err := museWaitIntake(ctx, sessA, startA, tokenA)
	if err != nil {
		t.Fatalf("A prompt never taken in: %v", err)
	}
	_, logB, err := museWaitIntake(ctx, sessB, startB, tokenB)
	if err != nil {
		t.Fatalf("B prompt never taken in: %v", err)
	}
	if _, err := museWaitTurnQuiescent(ctx, sessA, logA, 5*time.Minute); err != nil {
		t.Fatalf("A never completed: %v", err)
	}
	if _, err := museWaitTurnQuiescent(ctx, sessB, logB, 5*time.Minute); err != nil {
		t.Fatalf("B never completed: %v", err)
	}
	if logA == logB {
		t.Fatal("both parallel turns landed in one native session (no isolation)")
	}
	textA, textB := museLiveLogText(t, logA), museLiveLogText(t, logB)
	if !strings.Contains(textA, tokenA) || strings.Contains(textA, tokenB) {
		t.Fatal("session A transcript crossed with B")
	}
	if !strings.Contains(textB, tokenB) || strings.Contains(textB, tokenA) {
		t.Fatal("session B transcript crossed with A")
	}
}

// TestMuseCLIRealTrustGate boots a FRESH workspace without --trust-workspace:
// the trust gate must appear (never a silent hang), accepting it must settle
// the TUI, and the first turn must work with no gate text leaking into the
// answer. Backs trust_auth_prompts.
func TestMuseCLIRealTrustGate(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	workdir := t.TempDir()
	session := "mlp-muse-trust-" + museRandomHex(t, 3)
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", session).Run()
	})
	launch := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session,
		"-x", "200", "-y", "50", "-c", workdir, "muse", "--provider", "echo")
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	gateDeadline := time.Now().Add(60 * time.Second)
	var pane string
	for {
		p, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			t.Fatalf("capture pane: %v", err)
		}
		pane = p
		if musePaneShowsBlockingGate(pane) {
			break
		}
		if museTUISufficientlySettled(pane) {
			t.Fatalf("fresh workspace settled with no trust gate; pane:\n%s", pane)
		}
		if time.Now().After(gateDeadline) {
			t.Fatalf("neither gate nor settled TUI in a fresh workspace; pane:\n%s", pane)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(strings.ToLower(pane), "trust") {
		t.Fatalf("blocking gate is not the trust prompt; pane:\n%s", pane)
	}
	if err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "-l", "1").Run(); err != nil {
		t.Fatalf("send trust choice: %v", err)
	}
	if err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "Enter").Run(); err != nil {
		t.Fatalf("send Enter: %v", err)
	}
	if _, err := museWaitSettled(ctx, session, 90*time.Second); err != nil {
		t.Fatalf("TUI never settled after trusting: %v", err)
	}
	token := "TRUSTOK-" + museRandomHex(t, 3)
	after, logPath := museLiveSubmitTurn(t, ctx, session, "Reply with exactly "+token+" and nothing else.")
	transcript, ok := readMuseTranscriptMessages(logPath, "")
	if !ok {
		t.Fatal("unreadable native transcript")
	}
	final := museLastAssistantText(transcript)
	if !strings.Contains(final, token) {
		t.Fatalf("first turn after trust = %q, want %q", final, token)
	}
	if strings.Contains(strings.ToLower(final), "do you trust") {
		t.Fatalf("gate text leaked into the answer: %q", final)
	}
	if !museTUIAtPrompt(after) {
		t.Fatal("pane not settled after post-trust turn")
	}
}

// museProbeMCPStub is an in-process MCP Streamable-HTTP server with one
// tool, probe_echo(text) -> "STUB_ECHO:<text>". Shape proven against the
// real CLI 2026-09-10: stateless initialize/initialized/list per connection,
// plain-JSON (non-SSE) responses accepted.
func museProbeMCPStub(called *atomic.Int32) *httptest.Server {
	write := func(w http.ResponseWriter, code int, obj any) {
		body, _ := json.Marshal(obj)
		if obj == nil {
			body = nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if body != nil {
			_, _ = w.Write(body)
		}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			write(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
			return
		}
		var msg struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			write(w, 400, map[string]any{"error": "bad json"})
			return
		}
		switch msg.Method {
		case "initialize":
			res := map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "mlp-probe-stub", "version": "0.1"}}
			write(w, 200, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": res})
		case "notifications/initialized":
			write(w, 202, nil)
		case "tools/list":
			tools := []any{map[string]any{
				"name":        "probe_echo",
				"description": "Echo back the caller's text with a STUB_ECHO prefix. Call this when asked to run the probe echo.",
				"inputSchema": map[string]any{"type": "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []any{"text"}},
			}}
			write(w, 200, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"tools": tools}})
		case "tools/call":
			text, _ := msg.Params["arguments"].(map[string]any)["text"].(string)
			called.Add(1)
			content := []any{map[string]any{"type": "text", "text": "STUB_ECHO:" + text}}
			write(w, 200, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"content": content}})
		default:
			write(w, 200, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
		}
	}))
}

// TestMuseCLIRealMCPBridge mounts the in-process stub through the real
// settings merge and runs a Meta turn that must call it: the stub must see
// the call, the wire must carry the tool result, and the final answer must
// be the tool's output. The merge is restored afterwards — verified, not
// trusted. Backs mcp_bridge.
func TestMuseCLIRealMCPBridge(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	settingsPath, err := museSettingsPath()
	if err != nil {
		t.Fatalf("settings path: %v", err)
	}
	before, _ := os.ReadFile(settingsPath)
	// Byte-exact: the merge helper snapshots and rewrites the pre-run bytes
	// verbatim, so anything else means a leak or a clobber.
	t.Cleanup(func() {
		after, _ := os.ReadFile(settingsPath)
		if string(after) != string(before) {
			t.Errorf("settings.json not restored byte-exact after mounted run:\nbefore: %s\nafter: %s", before, after)
		}
	})

	var called atomic.Int32
	stub := museProbeMCPStub(&called)
	defer stub.Close()

	token := "BRIDGE-" + museRandomHex(t, 3)
	adapter := museLiveAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	streamChan := make(chan llmtypes.StreamChunk, 512)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{
			llmtypes.TextContent{Text: "You have an MCP tool called probe_echo. Call it with text " + token + ", then reply with ONLY the tool's raw output."}}},
	}, WithMCPConfig(`{"mcpServers": {"probe-stub": {"url": "`+stub.URL+`/mcp"}}}`),
		WithWorkingDir(t.TempDir()), llmtypes.WithReasoningEffort("low"),
		llmtypes.WithStreamingChan(streamChan), WithMuseStructuredTransport(true))
	if err != nil {
		t.Fatalf("bridge turn: %v", err)
	}
	t.Logf("bridge turn final: %q", resp.Choices[0].Content)
	if called.Load() == 0 {
		t.Fatal("stub never saw tools/call (mount did not reach the model)")
	}
	if final := resp.Choices[0].Content; !strings.Contains(final, "STUB_ECHO:"+token) {
		t.Fatalf("final = %q, want the stub tool output", final)
	}
	var toolEnds []llmtypes.StreamChunk
	for _, c := range museDrainStream(streamChan) {
		if c.Type == llmtypes.StreamChunkTypeToolCallEnd && strings.Contains(c.ToolName, "probe_echo") {
			toolEnds = append(toolEnds, c)
		}
	}
	if len(toolEnds) == 0 {
		t.Fatal("bridge tool ran but no probe_echo tool_call_end chunk streamed")
	}
	t.Logf("bridge tool_call_end: tool=%q result=%q", toolEnds[0].ToolName, toolEnds[0].ToolResult)
}

// TestMuseCLIRealLargePromptDraftDelivery pins large-payload injection
// against the real TUI (regression: builder-scale prompts failed with
// tmux "command too long" when sent as one send-keys argument). Launch-only,
// chunked-typed, and buffer-pasted drafts must all land visibly. No Enter is
// sent, so no model turn runs; cleanup kills the session.
func TestMuseCLIRealLargePromptDraftDelivery(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	adapter := museLiveAdapter()
	owner := "mlp-draft-" + museRandomHex(t, 3)
	t.Cleanup(func() { KillMusePersistentSession(owner) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	launch, err := adapter.GenerateContent(ctx, nil, []llmtypes.CallOption{
		WithPersistentInteractiveSession(true),
		WithInteractiveSessionID(owner),
		WithWorkingDir(t.TempDir()),
		llmtypes.WithCodingProviderLaunchOnly(),
	}...)
	if err != nil {
		t.Fatalf("launch-only: %v", err)
	}
	tmuxName := launch.Choices[0].GenerationInfo.CodingProviderSessionHandle.TmuxSession
	if tmuxName == "" {
		t.Fatal("launch-only returned no tmux session")
	}

	chunkMarker := "CHUNKMARK-" + museRandomHex(t, 4)
	if err := writeMuseVisibleDraftToTmux(ctx, tmuxName, "first line\n"+chunkMarker+"\nthird line"); err != nil {
		t.Fatalf("chunked draft: %v", err)
	}
	time.Sleep(2 * time.Second)
	pane, err := museTmuxCapturePane(ctx, tmuxName)
	if err != nil {
		t.Fatalf("capture after chunked draft: %v", err)
	}
	if !strings.Contains(pane, chunkMarker) {
		t.Fatalf("chunked draft marker %q not visible in pane", chunkMarker)
	}

	pasteMarker := "PASTEMARK-" + museRandomHex(t, 4)
	big := strings.Repeat("pasted filler line for volume. ", 100) + "\n" + pasteMarker + "\n" + strings.Repeat("trailing pasted filler. ", 40)
	if !musePromptNeedsAtomicPaste(big) {
		t.Fatal("draft prompt should route to atomic paste")
	}
	if err := pasteMuseDraftToTmux(ctx, tmuxName, big); err != nil {
		t.Fatalf("atomic paste: %v", err)
	}
	time.Sleep(2 * time.Second)
	pane2, err := museTmuxCapturePane(ctx, tmuxName)
	if err != nil {
		t.Fatalf("capture after paste: %v", err)
	}
	if n := strings.Count(pane2, pasteMarker); n != 1 {
		t.Fatalf("pasted marker %q appears %d times, want exactly once", pasteMarker, n)
	}
}

// TestMuseCLIRealInteractiveTmuxShortPromptRoundTrip is the one real,
// full-turn certification the interactive tmux lane was missing: every other
// live test in this file either exercises WithTmuxTransport's plumbing in
// isolation (draft typing alone, settle/gate detection via raw tmux) or goes
// through the exec --json lane (WithMuseStructuredTransport). None combined
// a live GenerateContent call with WithTmuxTransport(true) and let
// museSendPrompt -> museWaitIntake -> museWaitTurnQuiescent run together as
// one real turn -- which is exactly what silently swallowed a short prompt's
// keystrokes in production (a bare "hi", well under the atomic-paste
// threshold, submitted an empty input box for the full 60s intake deadline;
// see writeVisibleDraftAndConfirm). A short, deterministic prompt here is
// deliberate: it is the case a large-prompt-only cert would never catch.
func TestMuseCLIRealInteractiveTmuxShortPromptRoundTrip(t *testing.T) {
	requireRealMuseCLIE2E(t)
	adapter := museLiveAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{
			llmtypes.TextContent{Text: "hi, reply with exactly PINEAPPLE-TMUX and nothing else"}}},
	}, WithTmuxTransport(true), WithWorkingDir(t.TempDir()), llmtypes.WithReasoningEffort("low"))
	if err != nil {
		t.Fatalf("interactive tmux round trip failed (this is the case that silently ate a short prompt in production): %v", err)
	}
	final := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(final, "PINEAPPLE-TMUX") {
		t.Fatalf("final = %q, want the PINEAPPLE-TMUX marker", final)
	}
	h := resp.Choices[0].GenerationInfo.CodingProviderSessionHandle
	if h.Provider != "muse-cli" || h.Transport != llmtypes.CodingProviderTransportTmux {
		t.Fatalf("session handle = %+v, want provider=muse-cli transport=tmux", h)
	}
	if h.NativeSessionID == "" {
		t.Fatal("expected a native session id from a completed tmux turn")
	}
}

// TestMuseCLIRealProjectInstructionOnly certifies file-only mode end to
// end: the system prompt travels solely via the projected AGENTS.md while
// the typed turn is a bare human message. The marker rule proves the TUI
// read the file (not the typed text, which never contains it).
func TestMuseCLIRealProjectInstructionOnly(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	adapter := museLiveAdapter()
	owner := "mlp-agents-" + museRandomHex(t, 3)
	t.Cleanup(func() { KillMusePersistentSession(owner) })
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	workdir := t.TempDir()
	marker := "RULEWORD-" + museRandomHex(t, 4)

	systemMsg := llmtypes.MessageContent{
		Role: llmtypes.ChatMessageTypeSystem,
		Parts: []llmtypes.ContentPart{llmtypes.TextContent{
			Text: "Whenever asked for the code word, reply with exactly " + marker + " and nothing else.",
		}},
	}
	humanMsg := llmtypes.MessageContent{
		Role: llmtypes.ChatMessageTypeHuman,
		Parts: []llmtypes.ContentPart{llmtypes.TextContent{
			Text: "What is the code word?",
		}},
	}
	base := []llmtypes.CallOption{
		WithPersistentInteractiveSession(true),
		WithInteractiveSessionID(owner),
		WithWorkingDir(workdir),
		WithProjectInstructionOnly(true),
		WithRestoreProjectFiles(true),
		llmtypes.WithReasoningEffort("low"),
	}
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{systemMsg, humanMsg}, base...)
	if err != nil {
		t.Fatalf("file-only turn: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("file-only turn returned no choices")
	}
	if final := resp.Choices[0].Content; !strings.Contains(final, marker) {
		t.Fatalf("file-only turn answer missing rule marker %q:\n%s", marker, final)
	}
	// The projected file must live until teardown, then be restored away.
	// Kill explicitly here (idempotent with the deferred cleanup) so the
	// absence assertion below actually covers the restore path.
	KillMusePersistentSession(owner)
	if _, err := os.Stat(filepath.Join(workdir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("projected AGENTS.md leaked past KillMusePersistentSession cleanup")
	}
}
