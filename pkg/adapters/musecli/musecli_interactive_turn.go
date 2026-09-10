package musecli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Turn driving for the interactive (tmux) lane. v1 is a bounded single
// turn: boot a TUI, submit one prompt, wait for the pane to re-settle,
// extract the final text from the session transcript, tear the session
// down. Persistent pane reuse across turns is a later slice; requesting it
// fails loudly rather than silently running uncontained single turns.

// museDiscoverSessionSince finds the TUI session log produced after `since`
// whose content includes promptSnippet. It walks the dated session tree
// ($XDG_DATA_HOME/muse/sessions/YYYY/MM/DD/<id>/session.jsonl) and returns
// the session id and log path. Pure filesystem work — unit-tested with
// fixture trees, no CLI.
func museDiscoverSessionSince(dataHome string, since time.Time, promptSnippet string) (sessionID, logPath string, err error) {
	root := filepath.Join(dataHome, "muse", "sessions")
	var bestPath string
	var bestMod time.Time
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || d.Name() != "session.jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if promptSnippet != "" && !strings.Contains(string(raw), promptSnippet) {
			return nil
		}
		if info.ModTime().After(bestMod) {
			bestMod = info.ModTime()
			bestPath = path
		}
		return nil
	})
	if walkErr != nil {
		return "", "", fmt.Errorf("walk muse sessions: %w", walkErr)
	}
	if bestPath == "" {
		return "", "", fmt.Errorf("no muse session log modified since %s mentions the prompt", since.Format(time.RFC3339))
	}
	return filepath.Base(filepath.Dir(bestPath)), bestPath, nil
}

// museLastAssistantText returns the last AI message text in order.
func museLastAssistantText(messages []llmtypes.MessageContent) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llmtypes.ChatMessageTypeAI {
			continue
		}
		var parts []string
		for _, part := range messages[i].Parts {
			switch c := part.(type) {
			case llmtypes.TextContent:
				parts = append(parts, c.Text)
			case *llmtypes.TextContent:
				if c != nil {
					parts = append(parts, c.Text)
				}
			}
		}
		if text := strings.TrimSpace(strings.Join(parts, "")); text != "" {
			return text
		}
	}
	return ""
}

// museWaitIntake verifies the TUI actually took in the prompt: the native
// session log must show the turn after submit. The TUI can swallow an early
// Enter (observed live), so Enter is re-sent a bounded number of times until
// the log proves intake or the deadline passes.
func museWaitIntake(ctx context.Context, session string, turnStart time.Time, snippet string) (nativeSessionID, logPath string, err error) {
	dataHome := museXDGDataHome()
	intakeDeadline := time.Now().Add(60 * time.Second)
	for attempt := 0; ; attempt++ {
		id, path, findErr := museDiscoverSessionSince(dataHome, turnStart, snippet)
		if findErr == nil {
			return id, path, nil
		}
		err = findErr
		if time.Now().After(intakeDeadline) {
			return "", "", fmt.Errorf("muse TUI never took in the prompt after submit; last discovery error: %w", err)
		}
		if attempt > 0 {
			time.Sleep(5 * time.Second)
		}
		enter := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "Enter")
		if out, enterErr := enter.CombinedOutput(); enterErr != nil {
			return "", "", fmt.Errorf("tmux re-send Enter: %w\n%s", enterErr, out)
		}
	}
}

// museLogQuietSince reports whether the native session log has been
// untouched for at least quietFor.
func museLogQuietSince(logPath string, quietFor time.Duration) bool {
	info, err := os.Stat(logPath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) >= quietFor
}

// museWaitTurnQuiescent waits for a submitted, taken-in turn to finish:
// the pane has returned to settled idle and the native session log has been
// quiet for a beat. Callers must prove intake first (museWaitIntake): pane
// markers alone cannot signal completion, because the settled chrome
// (banner, input prompt, status line) persists while a turn streams —
// proven live 2026-09-10, when completion-by-growth returned on
// typed-but-unsubmitted input. Streaming deltas, tool phases, and reminder
// subagents all append to the log, so a quiet log plus a settled, stable
// pane means the turn is done, not paused.
func museWaitTurnQuiescent(ctx context.Context, session, logPath string, timeout time.Duration) (string, error) {
	const quietFor = 5 * time.Second
	deadline := time.Now().Add(timeout)
	for {
		if !museTmuxSessionAlive(ctx, session) {
			return "", fmt.Errorf("muse tmux session %q died mid-turn", session)
		}
		pane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return "", fmt.Errorf("capture pane waiting for turn: %w", err)
		}
		if musePaneShowsBlockingGate(pane) {
			return "", fmt.Errorf("muse TUI hit a trust/auth gate mid-turn; pane:\n%s", pane)
		}
		if museTUIAtPrompt(pane) && musePaneStable(ctx, session, pane) &&
			museLogQuietSince(logPath, quietFor) {
			return pane, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for muse turn to quiesce (commits=%d); latest pane:\n%s",
				museTurnCommits(logPath), pane)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("turn wait canceled: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

// generateContentTmux runs one bounded turn through a fresh TUI session.
func (a *MuseCLIAdapter) generateContentTmux(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if musePersistentInteractiveFromOptions(opts) {
		return nil, fmt.Errorf("muse-cli tmux lane does not support persistent sessions yet; run bounded turns")
	}
	prompt, err := museBuildExecPrompt(messages)
	if err != nil {
		return nil, err
	}
	workdir := strings.TrimSpace(museWorkingDirFromOptions(opts))
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working dir for muse tmux lane: %w", err)
		}
	}
	session := museTmuxSessionName(museInteractiveSessionIDFromOptions(opts))
	turnStart := time.Now()
	if err := museLaunchTUI(ctx, workdir, session, a.museExecProvider()); err != nil {
		return nil, err
	}
	defer museKillTmuxSession(context.Background(), session)

	if _, err := museWaitSettled(ctx, session, 90*time.Second); err != nil {
		return nil, err
	}
	if err := museSendPrompt(ctx, session, prompt); err != nil {
		return nil, err
	}
	nativeSessionID, logPath, err := museWaitIntake(ctx, session, turnStart, promptSnippet(prompt))
	if err != nil {
		return nil, err
	}
	after, err := museWaitTurnQuiescent(ctx, session, logPath, 5*time.Minute)
	if err != nil {
		return nil, err
	}

	transcript, ok := readMuseTranscriptMessages(logPath, "")
	if !ok {
		return nil, fmt.Errorf("read muse TUI transcript at %s", logPath)
	}
	final := museLastAssistantText(transcript)
	if strings.TrimSpace(final) == "" {
		return nil, fmt.Errorf("muse TUI turn produced no assistant text (session %s)", nativeSessionID)
	}
	gi := &llmtypes.GenerationInfo{}
	llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
		Provider:        "muse-cli",
		Transport:       llmtypes.CodingProviderTransportTmux,
		NativeSessionID: nativeSessionID,
		Model:           strings.TrimSpace(a.modelID),
	})
	// Keep the settled post-turn pane on the response: it is the wrapped
	// screen the reply-formatting-fidelity cert compares the transcript
	// extraction against, and the session is torn down below so no test can
	// re-capture it afterwards.
	if gi.Additional == nil {
		gi.Additional = map[string]any{}
	}
	gi.Additional["final_tmux_pane"] = after
	resp := &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{
		Content:        final,
		StopReason:     "completed",
		GenerationInfo: gi,
	}}}
	if usage, ok := readMuseTranscriptUsage(logPath, ""); ok {
		resp.Usage = &usage
		museAttachTurnCost(gi, strings.TrimSpace(a.modelID), &usage)
	}
	return resp, nil
}

// promptSnippet is the discovery anchor: a long-enough slice of the prompt
// that identifies this turn's session log without false-matching.
func promptSnippet(prompt string) string {
	const maxSnippet = 120
	snippet := strings.TrimSpace(prompt)
	if len(snippet) > maxSnippet {
		snippet = strings.TrimSpace(snippet[:maxSnippet])
	}
	return snippet
}
