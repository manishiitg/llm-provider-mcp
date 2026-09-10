package musecli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

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

// musePaneShowsBlockingGate reports whether the pane is stuck on a trust,
// auth, or login gate instead of the interactive TUI.
func musePaneShowsBlockingGate(pane string) bool {
	lower := strings.ToLower(pane)
	for _, marker := range []string{
		"do you trust",
		"untrusted",
		"muse login",
		"log in to",
		"approve this",
	} {
		if strings.Contains(lower, marker) {
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
func museLaunchTUI(ctx context.Context, workdir, session, provider string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not found in PATH; muse-cli tmux mode requires tmux: %w", err)
	}
	if _, err := exec.LookPath("muse"); err != nil {
		return fmt.Errorf("muse CLI not in PATH: %w", err)
	}
	launch := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session,
		"-x", "200", "-y", "50", "-c", workdir,
		"muse", "--trust-workspace", "--provider", provider)
	if out, err := launch.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %w\n%s", err, out)
	}
	return nil
}

// museWaitSettled polls the pane until the TUI settles, a blocking gate
// appears, or the deadline passes. A gate is a hard failure with a pane
// dump (trust_auth_prompts cert territory); a timeout is a hard failure
// with the latest pane (fresh_launch fidelity).
func museWaitSettled(ctx context.Context, session string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var pane string
	for {
		if !museTmuxSessionAlive(ctx, session) {
			return "", fmt.Errorf("muse tmux session %q died while waiting to settle", session)
		}
		p, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return "", fmt.Errorf("capture pane: %w", err)
		}
		pane = p
		if museTUISufficientlySettled(pane) && musePaneStable(ctx, session, pane) {
			return pane, nil
		}
		if musePaneShowsBlockingGate(pane) {
			return "", fmt.Errorf("muse TUI blocked on trust/auth gate (trust_auth_prompts cert territory); pane:\n%s", pane)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for muse TUI to settle; latest pane:\n%s", pane)
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

// museSendPrompt types the prompt into the TUI pane and submits it.
func museSendPrompt(ctx context.Context, session, prompt string) error {
	send := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "-l", prompt)
	if out, err := send.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys prompt: %w\n%s", err, out)
	}
	enter := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "Enter")
	if out, err := enter.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %w\n%s", err, out)
	}
	return nil
}
