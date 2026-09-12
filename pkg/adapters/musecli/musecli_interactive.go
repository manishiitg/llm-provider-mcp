package musecli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Interactive (tmux) lane for muse-cli. v1 covers session lifecycle —
// launch, settle detection, prompt submit, teardown — behind an explicit
// opt-in (WithTmuxTransport). Turn completion and text extraction live in
// musecli_interactive_turn.go. The exec --json lane stays the default until
// the tmux lane is certified; nothing routes here unless the caller asks.
//
// Transport truth, enforced by tests on every layer: the adapter runs
// exec-only unless this lane is explicitly requested, even though the
// provider contract declares the tmux transport.

// MetadataKeyMuseTmuxTransport explicitly routes one turn through the tmux
// lane. No other option implies it: interactive-session metadata alone still
// runs exec (orchestrators attach those for bookkeeping on both lanes).
const MetadataKeyMuseTmuxTransport = "muse_tmux_transport"

// WithTmuxTransport routes one turn through the interactive tmux lane:
// boot (or reuse) a `muse` TUI in tmux, submit the prompt, wait for
// completion, extract the final text. Requires tmux and the muse binary.
func WithTmuxTransport(enabled bool) llmtypes.CallOption {
	return func(opts *llmtypes.CallOptions) {
		ensureMetadata(opts)
		opts.Metadata.Custom[MetadataKeyMuseTmuxTransport] = enabled
	}
}

func museTmuxTransportRequested(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	requested, _ := opts.Metadata.Custom[MetadataKeyMuseTmuxTransport].(bool)
	return requested
}

// museTmuxSessionPrefix namespaces our panes so a kill can never touch a
// session it did not create.
const museTmuxSessionPrefix = "mlp-muse-"

func museRandomSessionSuffix() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func museTmuxSessionName(owner string) string {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = "bounded-" + museRandomSessionSuffix()
	}
	var clean strings.Builder
	for _, r := range strings.ToLower(owner) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			clean.WriteRune(r)
		default:
			clean.WriteRune('-')
		}
	}
	name := museTmuxSessionPrefix + strings.Trim(clean.String(), "-")
	if len(name) > 60 {
		name = name[:60]
	}
	return name + "-" + museRandomSessionSuffix()
}

// Recognize the active native question widget, not mentions in old output.
func musePendingUserInputError(pane string) error {
	lower := strings.ToLower(pane)
	start := strings.LastIndex(lower, "◆ request user input")
	if start < 0 {
		return nil
	}
	widget := lower[start:]
	question := strings.Contains(widget, "enter to select") && strings.Contains(widget, "esc to interrupt")
	review := strings.Contains(widget, "review answers before submit") && strings.Contains(widget, "enter to edit or submit") && strings.Contains(widget, "esc to go back")
	if !question && !review {
		return nil
	}
	return &llmerrors.Error{Kind: llmerrors.KindUserInputRequired, Provider: "muse-cli", Err: fmt.Errorf("Muse is waiting for your answer. Open its terminal and answer or dismiss the question before continuing:\n%s", strings.TrimSpace(pane[start:]))}
}

// musePaneShowsBlockingGate recognizes a gate surface, not authentication
// words in the conversation. In particular, "log in to <another service>"
// in an assistant answer must never prevent steering the Muse composer.
func musePaneShowsBlockingGate(pane string) bool {
	lower := strings.ToLower(pane)
	lines := strings.Split(lower, "\n")
	// Match complete prompt instructions at line starts. A composer can also
	// be drawn beneath a native dialog, so its presence is not a gate bypass.
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "do you trust ") ||
			strings.HasPrefix(line, "workspace is untrusted") ||
			strings.HasPrefix(line, "this workspace is untrusted") ||
			strings.HasPrefix(line, "run `muse login`") ||
			strings.HasPrefix(line, "run muse login") ||
			strings.HasPrefix(line, "please log in to meta") {
			return true
		}
	}
	return false
}

// museTUISufficientlySettled pins the exact ready markers observed live
// (fresh_launch green 2026-09-10): the "Muse Code <version>" banner, the
// "❯" input prompt, and the "<provider> · <workdir>" status line (meta
// shows "<model> · <effort> · <workdir>", echo shows "echo · <workdir>").
// No gate may be present. Asserting on real markers, not line counts.
// Boot-only: long answers scroll the banner off, so turn-completion waits
// use museTUIAtPrompt instead.
func museTUISufficientlySettled(pane string) bool {
	if musePaneShowsBlockingGate(pane) {
		return false
	}
	return strings.Contains(pane, "Muse Code") &&
		strings.Contains(pane, "❯") &&
		strings.Contains(pane, " · ")
}

// museTUIAtPrompt reports whether the pane shows a live TUI at its input
// prompt, without requiring the boot banner (which scrolls off during long
// turns — proven live 2026-09-10 when a completed 300-char-line answer sat
// under a fresh ❯ for five minutes while the banner-gated waiter timed
// out). The prompt glyph plus the "<a> · <b>" status line is the idle
// shape; streaming panes share it, so this is only ever half of a
// completion check (stability + log quiet are the other half).
func museTUIAtPrompt(pane string) bool {
	if musePaneShowsBlockingGate(pane) {
		return false
	}
	return strings.Contains(pane, "❯") && strings.Contains(pane, " · ")
}

func museTmuxCapturePane(ctx context.Context, session string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-t", session).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func museTmuxSessionAlive(ctx context.Context, session string) bool {
	return exec.CommandContext(ctx, "tmux", "has-session", "-t", session).Run() == nil
}

func museKillTmuxSession(ctx context.Context, session string) {
	_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", session).Run()
}

// museLaunchTUI boots `muse --trust-workspace` in a detached tmux session.
// --trust-workspace keeps launch about launch, not gate UX: trust gates are
// CertTrustAuthPrompts territory, and the settle waiter below fails loudly
// if the flag ever stops working.
// museTUIApprovalArgv is the approval posture for every MCP-mounted TUI,
// persistent or bounded: --disable-approval alone does NOT suppress MCP-tool
// approval (proven live 2026-09-10, a mounted turn calling
// mcp__api_bridge__execute_shell_command parked in approval_wait.effect.started
// and hung the full 8-minute ctx while --disable-approval was passed).
// --approval-mode never is the flag that actually covers MCP tools; both are
// passed (autonomous coding-agent operation, same posture as the
// codex/claude adapters). One helper so the two launch paths cannot drift.
func museTUIApprovalArgv() []string {
	return []string{"--disable-approval", "--approval-mode", "never"}
}

// museNativeContainmentArgv is defense in depth for bridge-routed turns.
// The PreToolUse hook is the primary execution allowlist; these first-class
// switches keep native shell and filesystem writes unavailable even if hook
// loading or matching ever regresses.
func museNativeContainmentArgv() []string {
	return []string{"--disable-shell", "--disable-write"}
}

// museLaunchTUI boots one bounded (one-turn) TUI and returns its isolated
// config cleanup. mcpJSON mirrors the persistent lane: a mounted turn carries
// museTUIApprovalArgv. Unmounted turns boot bare.
func museLaunchTUI(ctx context.Context, workdir, session, provider, mcpJSON string, toolAllowlist []string) (func(), error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; muse-cli tmux mode requires tmux: %w", err)
	}
	if _, err := exec.LookPath("muse"); err != nil {
		return nil, fmt.Errorf("muse CLI not in PATH: %w", err)
	}
	configHome, cleanup, err := musePrepareIsolatedConfig(strings.TrimSpace(mcpJSON), toolAllowlist)
	if err != nil {
		return nil, err
	}
	argv := []string{"env", "XDG_CONFIG_HOME=" + configHome, "XDG_DATA_HOME=" + museXDGDataHome(), "muse", "--trust-workspace", "--provider", provider}
	if strings.TrimSpace(mcpJSON) != "" {
		argv = append(argv, museTUIApprovalArgv()...)
	}
	if toolAllowlist != nil {
		argv = append(argv, museNativeContainmentArgv()...)
	}
	launch := exec.CommandContext(ctx, "tmux", append([]string{"new-session", "-d", "-s", session,
		"-x", "200", "-y", "50", "-c", workdir}, argv...)...)
	if out, err := launch.CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("tmux new-session: %w\n%s", err, out)
	}
	return cleanup, nil
}

// museWaitSettled polls a freshly launched pane until the TUI settles, a
// blocking gate appears, or the deadline passes. It deliberately requires
// the boot banner; reused sessions must call museWaitAtPrompt because a long
// conversation scrolls that banner out of the pane.
func museWaitSettled(ctx context.Context, session string, timeout time.Duration) (string, error) {
	return museWaitForReadyPane(ctx, session, timeout, museTUISufficientlySettled, "muse TUI to settle")
}

// museWaitAtPrompt waits for a reused TUI to become ready for another turn.
// Unlike the fresh-boot waiter it only requires the idle prompt/status chrome,
// which remains visible after the startup banner has scrolled away.
func museWaitAtPrompt(ctx context.Context, session string, timeout time.Duration) (string, error) {
	return museWaitForReadyPane(ctx, session, timeout, museTUIAtPrompt, "muse TUI to return to the prompt")
}

func museWaitForReadyPane(ctx context.Context, session string, timeout time.Duration, ready func(string) bool, waitDescription string) (string, error) {
	deadline := time.Now().Add(timeout)
	var pane string
	for {
		if !museTmuxSessionAlive(ctx, session) {
			return "", fmt.Errorf("muse tmux session %q died while waiting for %s", session, waitDescription)
		}
		p, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return "", fmt.Errorf("capture pane: %w", err)
		}
		pane = p
		pending, err := museHandlePendingQuestion(ctx, session, pane)
		if err != nil {
			return "", err
		}
		if pending {
			if time.Now().After(deadline) {
				return "", musePendingUserInputError(pane)
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		if ready(pane) && musePaneStable(ctx, session, pane) {
			return pane, nil
		}
		if musePaneShowsBlockingGate(pane) {
			return "", fmt.Errorf("muse TUI blocked on trust/auth gate (trust_auth_prompts cert territory); pane:\n%s", pane)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for %s; latest pane:\n%s", waitDescription, pane)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("settle wait canceled: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// musePaneStable reports whether the pane content stops changing across a
// short window: chrome can render seconds before the input loop accepts
// submissions (observed live: an Enter sent right after first render is
// swallowed). Settling requires markers AND stability.
func musePaneStable(ctx context.Context, session, pane string) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Second):
	}
	again, err := museTmuxCapturePane(ctx, session)
	return err == nil && again == pane
}

// museVisibleInputChunkRunes bounds each literal tmux key injection. tmux
// rejects a single oversized send-keys argument ("command too long",
// reproduced live with a 100KB builder prompt), so prompts are typed in
// bounded literal chunks with embedded newlines sent as Ctrl+J (newline
// without submit).
const museVisibleInputChunkRunes = 160

const (
	// Prompts at or above these sizes go through one atomic tmux buffer
	// paste instead of dozens of rapid send-keys calls: same thresholds as
	// cursor, whose editor demonstrably drops parts of large literal bursts.
	museAtomicPasteMinRunes = 2048
	museAtomicPasteMinLines = 16
)

// museSendPrompt types the prompt into the TUI pane and submits it, retrying
// the submit (not just the typing) a bounded number of times.
//
// Confirming the draft is visible (writeVisibleDraftAndConfirm) is not
// enough: observed live 2026-09-10, a fully visible, confirmed "hi" sat
// unsubmitted at the prompt indefinitely after Enter -- tmux send-keys
// reported success, but the TUI never acted on it (no reply, no streaming,
// pane byte-for-byte unchanged). museWaitIntake's downstream Enter-resend
// loop couldn't recover it: that loop only resends a bare Enter into
// whatever state the pane is already in, so a keystroke genuinely dropped
// by the TUI just gets dropped again. This is the same render-race class
// museWaitSettled already guards at boot ("an Enter sent right after first
// render is swallowed"); submission needs the equivalent guard.
func museSendPrompt(ctx context.Context, session, prompt string) error {
	const maxSubmitAttempts = 3
	for attempt := 1; ; attempt++ {
		if err := museWaitLiveInputComposer(ctx, session); err != nil {
			return err
		}
		if musePromptNeedsAtomicPaste(prompt) {
			if err := pasteMuseDraftToTmux(ctx, session, prompt); err != nil {
				return err
			}
		} else if err := writeVisibleDraftAndConfirm(ctx, session, prompt); err != nil {
			return err
		}
		beforePane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return fmt.Errorf("capture pane before Enter: %w", err)
		}
		// Give the just-confirmed draft a beat to finish rendering before
		// firing Enter into it -- the same guard museWaitSettled applies at
		// boot, applied here at submit time.
		musePaneStable(ctx, session, beforePane)
		enter := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "Enter")
		if out, err := enter.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux send-keys Enter: %w\n%s", err, out)
		}
		if museEnterTookEffect(ctx, session, beforePane) {
			return nil
		}
		if attempt >= maxSubmitAttempts {
			return fmt.Errorf("muse TUI did not act on Enter after %d submit attempts; the keystroke may have been swallowed mid-render", maxSubmitAttempts)
		}
		clear := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "C-u")
		if out, err := clear.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux clear input before resubmit retry: %w\n%s", err, out)
		}
	}
}

// museEnterTookEffect reports whether the pane changed within a short
// window after Enter was sent -- proof the TUI actually acted on the
// keystroke rather than swallowing it. Any change counts (cleared input,
// a thinking indicator, streamed text, or an already-complete reply); the
// failure mode this guards is the pane staying byte-for-byte identical to
// before Enter was sent.
func museEnterTookEffect(ctx context.Context, session, beforePane string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		pane, err := museTmuxCapturePane(ctx, session)
		if err == nil && pane != beforePane {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// writeVisibleDraftAndConfirm types the prompt via writeMuseVisibleDraftToTmux
// and waits for it to actually appear in the pane before returning, retyping
// (after clearing the line) a bounded number of times if it doesn't.
//
// pasteMuseDraftToTmux (the large-prompt path) already waits for its own
// collapse/visible markers before its caller submits; this plain-typed path
// had no equivalent check, so a keystroke burst swallowed mid-render (the
// same class of race documented on musePaneStable -- "an Enter sent right
// after first render is swallowed") went straight to museSendPrompt's Enter
// with an empty input box. museWaitIntake's retry loop only re-sends a bare
// Enter, never retypes, so every retry kept submitting nothing for the full
// 60s deadline: "muse TUI never took in the prompt after submit" no matter
// how many Enters it resent. Observed live 2026-09-10 on a trivial one-word
// prompt ("hi"), well under the atomic-paste threshold.
func writeVisibleDraftAndConfirm(ctx context.Context, session, prompt string) error {
	snippet := prompt
	if idx := strings.Index(snippet, "\n"); idx >= 0 {
		snippet = snippet[:idx]
	}
	snippet = strings.TrimSpace(snippet)
	if len([]rune(snippet)) > 40 {
		snippet = string([]rune(snippet)[:40])
	}
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		if err := museWaitLiveInputComposer(ctx, session); err != nil {
			return err
		}
		if err := writeMuseVisibleDraftToTmux(ctx, session, prompt); err != nil {
			return err
		}
		if snippet == "" {
			return nil // nothing to verify (a genuinely empty prompt)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			pane, err := museTmuxCapturePane(ctx, session)
			if err == nil && strings.Contains(pane, snippet) {
				return nil
			}
			if time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("muse type-and-confirm wait canceled: %w", ctx.Err())
			case <-time.After(150 * time.Millisecond):
			}
		}
		if attempt >= maxAttempts {
			return fmt.Errorf("muse TUI did not show the typed prompt after %d attempts; the keystrokes may have been swallowed mid-render", maxAttempts)
		}
		clear := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "C-u")
		if out, err := clear.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux clear input before retry: %w\n%s", err, out)
		}
	}
}

// musePromptNeedsAtomicPaste reports whether the prompt is large enough that
// literal chunk typing would take enough tmux round-trips to become
// unreliable.
func musePromptNeedsAtomicPaste(prompt string) bool {
	return utf8.RuneCountInString(prompt) >= museAtomicPasteMinRunes ||
		strings.Count(prompt, "\n")+1 >= museAtomicPasteMinLines
}

// pasteMuseDraftToTmux transfers a large prompt through stdin rather than
// argv, then asks tmux to emit it as one bracketed paste. This avoids both
// tmux's command-size rejection and partial delivery from dozens of rapid
// send-keys subprocesses. The deferred delete best-effort cleans the named
// buffer on every path.
//
// Muse collapses a bracketed paste into a "[Pasted Content N chars]"
// attachment whose own chrome instructs "paste again to expand for editing"
// (proven live: a single paste leaves the payload collapsed, a second paste
// expands it into visible text exactly once — never duplicated). So this
// waits for the collapse marker and pastes the same buffer again; when the
// prompt is already visible with no collapse, the second paste is skipped so
// content can never double.
func pasteMuseDraftToTmux(ctx context.Context, session, prompt string) error {
	bufferName := "mlp-muse-input-" + museRandomSessionSuffix()
	load := exec.CommandContext(ctx, "tmux", "load-buffer", "-b", bufferName, "-")
	load.Stdin = strings.NewReader(prompt)
	if out, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer muse input: %w\n%s", err, out)
	}
	defer func() {
		_ = exec.CommandContext(context.Background(), "tmux", "delete-buffer", "-b", bufferName).Run()
	}()
	paste := func(deleteAfter bool) error {
		args := []string{"paste-buffer", "-p", "-r", "-b", bufferName, "-t", session}
		if deleteAfter {
			args = []string{"paste-buffer", "-d", "-p", "-r", "-b", bufferName, "-t", session}
		}
		cmd := exec.CommandContext(ctx, "tmux", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux paste-buffer muse input: %w\n%s", err, out)
		}
		return nil
	}
	if err := paste(false); err != nil {
		return err
	}
	snippet := prompt
	if idx := strings.Index(snippet, "\n"); idx >= 0 {
		snippet = snippet[:idx]
	}
	if len([]rune(snippet)) > 40 {
		snippet = string([]rune(snippet)[:40])
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		pane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return fmt.Errorf("tmux capture after muse paste: %w", err)
		}
		if strings.Contains(pane, "Pasted Content") || strings.Contains(pane, "Paste again to expand") {
			return paste(true)
		}
		if snippet != "" && strings.Contains(pane, snippet) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("muse TUI showed neither pasted-content collapse nor prompt text after atomic paste")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("muse paste-expand wait canceled: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// writeMuseVisibleDraftToTmux types the prompt in bounded literal chunks so
// no single tmux command approaches the size rejection. A "--" separator
// keeps a chunk that starts with "-" from parsing as a flag.
func writeMuseVisibleDraftToTmux(ctx context.Context, session, prompt string) error {
	prompt = strings.ReplaceAll(prompt, "\r\n", "\n")
	prompt = strings.ReplaceAll(prompt, "\r", "\n")
	lines := strings.Split(prompt, "\n")
	for lineIndex, line := range lines {
		runes := []rune(line)
		for len(runes) > 0 {
			chunkSize := min(len(runes), museVisibleInputChunkRunes)
			chunk := string(runes[:chunkSize])
			send := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "-l", "--", chunk)
			if out, err := send.CombinedOutput(); err != nil {
				return fmt.Errorf("tmux send-keys prompt chunk: %w\n%s", err, out)
			}
			runes = runes[chunkSize:]
		}
		if lineIndex < len(lines)-1 {
			nl := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "C-j")
			if out, err := nl.CombinedOutput(); err != nil {
				return fmt.Errorf("tmux send-keys newline: %w\n%s", err, out)
			}
		}
	}
	return nil
}
