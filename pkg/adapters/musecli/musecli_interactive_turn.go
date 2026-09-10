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
// jsonEscapeLogSnippet escapes a plaintext snippet the way JSON string
// encoding does, so it can be found in raw session.jsonl bytes with
// strings.Contains. The discovery below compares against the raw file, not
// decoded records; a snippet holding a literal newline can never match the
// log's backslash-n bytes.
func jsonEscapeLogSnippet(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func museDiscoverSessionSince(dataHome string, since time.Time, promptSnippet string) (sessionID, logPath string, err error) {
	promptSnippet = jsonEscapeLogSnippet(promptSnippet)
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
	persistent := musePersistentInteractiveFromOptions(opts)
	owner := strings.TrimSpace(museInteractiveSessionIDFromOptions(opts))
	launchOnly := llmtypes.CodingProviderLaunchOnlyFromOptions(opts)
	if persistent && owner == "" {
		return nil, fmt.Errorf("muse-cli persistent tmux requires an owner session id (WithMuseInteractiveSessionID)")
	}
	if launchOnly && !persistent {
		return nil, fmt.Errorf("muse-cli launch-only requires the persistent option: a bounded session would die before reuse")
	}
	// Launch-only with an empty prompt is the transport-session handshake:
	// boot (or rebind) the TUI and hand back its handle. Anything else
	// needs a real prompt.
	prompt, err := museBuildExecPrompt(messages)
	if err != nil && !launchOnly {
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

	session := ""
	if persistent {
		entry, _, err := museAcquirePersistentSession(ctx, owner, workdir,
			a.museExecProvider(), strings.TrimSpace(a.modelID),
			strings.TrimSpace(museMCPConfigFromOptions(opts)), musePersistentReadyFile(opts))
		if err != nil {
			return nil, err
		}
		session = entry.tmuxName
	} else {
		session = museTmuxSessionName(museInteractiveSessionIDFromOptions(opts))
		if err := museLaunchTUI(ctx, workdir, session, a.museExecProvider()); err != nil {
			return nil, err
		}
		defer museKillTmuxSession(context.Background(), session)
	}
	if persistent && launchOnly {
		// Acquire already waited for settle (+ MCP readiness on fresh
		// launch). Return the handle so the orchestrator rebinds to this
		// tmux session; the native id is unknown until the first turn.
		gi := &llmtypes.GenerationInfo{}
		llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
			Provider:    "muse-cli",
			Transport:   llmtypes.CodingProviderTransportTmux,
			TmuxSession: session,
			Model:       strings.TrimSpace(a.modelID),
		})
		return &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{
			Content:        "",
			StopReason:     "completed",
			GenerationInfo: gi,
		}}}, nil
	}

	// Bounded sessions boot above; persistent ones are already up but may be
	// mid-turn (a queued follow-up): wait for the idle prompt so turns
	// serialize instead of interleaving in one TUI.
	turnStart := time.Now()
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
		TmuxSession:     session,
		Model:           strings.TrimSpace(a.modelID),
	})
	// Keep the settled post-turn pane on the response: it is the wrapped
	// screen the reply-formatting-fidelity cert compares the transcript
	// extraction against. Bounded sessions are torn down below, so no test
	// can re-capture it afterwards; persistent ones stay live.
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
