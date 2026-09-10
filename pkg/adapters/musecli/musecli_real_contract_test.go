package musecli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireRealMuseCLIE2E(t *testing.T) {
	t.Helper()
	if !*codingCLIP0Live {
		t.Skip("run through the live coding CLI P0 runner: go test ./pkg/adapters/musecli/ -run TestMuseCLIReal -args -coding-cli-p0-live")
	}
	for _, bin := range []string{"muse", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("real Muse CLI tests require %s in PATH: %v", bin, err)
		}
	}
}

func museRandomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// musePaneShowsBlockingGate reports whether the pane is stuck on a trust,
// auth, or login gate instead of the interactive TUI. Unit-covered by
// TestMusePaneGateDetection; the live test fails through this with a pane
// dump pointing at the trust_auth_prompts cert.
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

// museTUISufficientlySettled is the v1 readiness signal: the TUI rendered
// real content and is not sitting on a gate. Follow-up after the first
// live run: capture the exact ready prompt from the settled-pane log below
// and pin it here (see CertReplyFormattingFidelity-style fidelity: assert
// on the real marker, not line counts).
func museTUISufficientlySettled(pane string) bool {
	if musePaneShowsBlockingGate(pane) {
		return false
	}
	lines := 0
	for _, line := range strings.Split(pane, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	return lines >= 5
}

func museTmuxCapturePane(ctx context.Context, session string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-t", session).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func TestMuseCLIRealInteractiveTmuxFullContract(t *testing.T) {
	requireRealMuseCLIE2E(t)

	workdir := t.TempDir()

	// runtime_context (projection half): plant a canary skill before boot.
	// The model-reads-it half needs a live turn and belongs to the
	// runtime_context cert, not fresh_launch.
	canarySkill := "muse-fresh-canary-" + museRandomHex(t, 3)
	skillDir := filepath.Join(workdir, ".agents", "skills", canarySkill)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillDoc := "---\nname: " + canarySkill + "\ndescription: fresh_launch canary\n---\n# Canary\nReply pineapple when asked.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	session := "mlp-muse-cli-int-t-" + museRandomHex(t, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", session).Run()
	})

	// --trust-workspace keeps this cert about launch, not gate UX: trust
	// gates are CertTrustAuthPrompts territory. The gate assertion below
	// fails loudly if the flag ever stops working.
	launch := exec.CommandContext(ctx, "tmux", "new-session", "-d", "-s", session,
		"-x", "200", "-y", "50", "-c", workdir, "muse", "--trust-workspace")
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}

	deadline := time.Now().Add(90 * time.Second)
	var pane string
	for {
		p, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			t.Fatalf("capture pane: %v", err)
		}
		pane = p
		if museTUISufficientlySettled(pane) {
			break
		}
		if musePaneShowsBlockingGate(pane) {
			t.Fatalf("muse TUI blocked on trust/auth gate (trust_auth_prompts cert territory); pane:\n%s", pane)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for muse TUI to settle; latest pane:\n%s", pane)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("settled muse pane:\n%s", pane)

	// Projection half of runtime_context: the canary skill must be
	// discoverable in this trusted workspace.
	skillsOut, err := exec.CommandContext(ctx, "muse", "skills", "list", "--source", "project", "--workspace", workdir, "--trust-workspace").CombinedOutput()
	if err != nil {
		t.Fatalf("muse skills list: %v\n%s", err, skillsOut)
	}
	if !strings.Contains(string(skillsOut), canarySkill) {
		t.Fatalf("canary skill %q not discovered; skills list:\n%s", canarySkill, skillsOut)
	}

	// Still alive 5s later: a booted TUI, not a crash-looping process.
	time.Sleep(5 * time.Second)
	if err := exec.CommandContext(ctx, "tmux", "has-session", "-t", session).Run(); err != nil {
		t.Fatalf("muse tmux session died after settling: %v", err)
	}

	if err := exec.CommandContext(ctx, "tmux", "kill-session", "-t", session).Run(); err != nil {
		t.Fatalf("kill test session: %v", err)
	}
	if err := exec.CommandContext(ctx, "tmux", "has-session", "-t", session).Run(); err == nil {
		t.Fatal("test tmux session survived kill-session")
	}
}

func TestMusePaneGateDetection(t *testing.T) {
	blocked := []string{
		"Do you trust the files in this folder? [y/n]",
		"workspace is untrusted",
		"Run `muse login` to continue",
		"Please log in to Meta to proceed",
	}
	for _, pane := range blocked {
		if !musePaneShowsBlockingGate(pane) {
			t.Errorf("gate not detected in %q", pane)
		}
	}
	for _, pane := range []string{"", "thinking...", "Welcome to Muse\n\n> "} {
		if musePaneShowsBlockingGate(pane) {
			t.Errorf("false gate positive in %q", pane)
		}
	}
	if museTUISufficientlySettled("alpha\nbeta") {
		t.Error("2-line pane must not count as settled")
	}
}
