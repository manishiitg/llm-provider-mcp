package musecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestMuseTmuxSessionNameIsNamespacedAndClean(t *testing.T) {
	seen := map[string]bool{}
	for _, owner := range []string{"", "Workflow/Testing Iteration 0", "abc"} {
		name := museTmuxSessionName(owner)
		if !strings.HasPrefix(name, museTmuxSessionPrefix) {
			t.Fatalf("session %q missing prefix %q", name, museTmuxSessionPrefix)
		}
		if strings.ContainsAny(name, "/ .") {
			t.Fatalf("session %q contains tmux-unsafe characters", name)
		}
		if seen[name] {
			t.Fatalf("duplicate session name %q", name)
		}
		seen[name] = true
	}
}

func TestMuseTmuxTransportRequestedDefaultsOff(t *testing.T) {
	if museTmuxTransportRequested(nil) {
		t.Fatal("nil options must not request tmux")
	}
	opts := &llmtypes.CallOptions{}
	if museTmuxTransportRequested(opts) {
		t.Fatal("empty options must not request tmux")
	}
	WithTmuxTransport(true)(opts)
	if !museTmuxTransportRequested(opts) {
		t.Fatal("WithTmuxTransport(true) not honored")
	}
	WithTmuxTransport(false)(opts)
	if museTmuxTransportRequested(opts) {
		t.Fatal("WithTmuxTransport(false) not honored")
	}
}

func TestMuseStructuredTransportRequestedDefaultsOff(t *testing.T) {
	if museStructuredTransportRequested(nil) {
		t.Fatal("nil options must not request structured")
	}
	opts := &llmtypes.CallOptions{}
	if museStructuredTransportRequested(opts) {
		t.Fatal("empty options must not request structured (tmux is the default)")
	}
	WithMuseStructuredTransport(true)(opts)
	if !museStructuredTransportRequested(opts) {
		t.Fatal("WithMuseStructuredTransport(true) not honored")
	}
	WithMuseStructuredTransport(false)(opts)
	if museStructuredTransportRequested(opts) {
		t.Fatal("WithMuseStructuredTransport(false) not honored")
	}
}

// TestMusePersistentKeyAndName: pooling without an owner must fail fast
// (two conversations must never share a TUI), and tmux names must be
// stable, safe, and bounded.
func TestMusePersistentKeyAndName(t *testing.T) {
	if _, err := musePersistentKey(""); err == nil {
		t.Fatal("empty owner must fail, not pool anonymously")
	}
	if _, err := musePersistentKey("  "); err == nil {
		t.Fatal("blank owner must fail")
	}
	key, err := musePersistentKey("conv-123")
	if err != nil || key != "conv-123" {
		t.Fatalf("key = %q, %v", key, err)
	}
	a, b := musePersistentTmuxName("conv-123"), musePersistentTmuxName("conv-123")
	if a != b || !strings.HasPrefix(a, "mlp-muse-") {
		t.Fatalf("names not stable/prefixed: %q %q", a, b)
	}
	weird := musePersistentTmuxName("Conv 123/ABC!@#xyz")
	for _, r := range weird {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			t.Fatalf("unsafe tmux name %q", weird)
		}
	}
	if got := musePersistentTmuxName(strings.Repeat("x", 200)); len(got) > 48 {
		t.Fatalf("name not bounded: %d chars", len(got))
	}
	KillMusePersistentSession("test-owner-that-never-existed")
}

func TestMuseLastAssistantTextPicksLatest(t *testing.T) {
	text := func(s string) llmtypes.ContentPart { return llmtypes.TextContent{Text: s} }
	messages := []llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{text("hi")}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{text("first")}},
		{Role: llmtypes.ChatMessageTypeAI, Parts: []llmtypes.ContentPart{text("second")}},
	}
	if got := museLastAssistantText(messages); got != "second" {
		t.Fatalf("last assistant text = %q, want second", got)
	}
	if got := museLastAssistantText(nil); got != "" {
		t.Fatalf("empty transcript = %q, want empty", got)
	}
}

func TestPromptSnippetTruncates(t *testing.T) {
	if got := promptSnippet("  hi  "); got != "hi" {
		t.Fatalf("snippet = %q, want trimmed hi", got)
	}
	long := strings.Repeat("x", 200)
	if got := promptSnippet(long); len(got) != 120 {
		t.Fatalf("snippet len = %d, want 120", len(got))
	}
}

// TestMuseDiscoverSessionSince finds the newest matching session log in a
// fixture tree: the newest turn's log wins over older runs and unrelated logs.
func TestMuseDiscoverSessionSince(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "muse", "sessions", "2026", "09", "10")
	oldDir := filepath.Join(day, "sess-old")
	newDir := filepath.Join(day, "sess-new")
	otherDir := filepath.Join(day, "sess-other")
	for _, dir := range []string{oldDir, newDir, otherDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.WriteFile(filepath.Join(oldDir, "session.jsonl"), []byte("pineapple old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(oldDir, "session.jsonl"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "session.jsonl"), []byte("unrelated chatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "session.jsonl"), []byte("pineapple new turn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	since := time.Now().Add(-time.Minute)
	id, path, err := museDiscoverSessionSince(home, since, "pineapple")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if id != "sess-new" {
		t.Fatalf("session id = %q, want sess-new", id)
	}
	if path != filepath.Join(newDir, "session.jsonl") {
		t.Fatalf("log path = %q", path)
	}
	if _, _, err := museDiscoverSessionSince(home, since, "mango"); err == nil {
		t.Fatal("expected error when no log mentions the prompt")
	}
}

// TestMusePromptNeedsAtomicPaste pins the prompt-size routing that avoids
// tmux's "command too long" rejection: ordinary prompts type literally in
// bounded chunks, while prompts at/above the cursor-mirrored thresholds go
// through one atomic buffer paste.
func TestMusePromptNeedsAtomicPaste(t *testing.T) {
	largeRunes := strings.Repeat("x", museAtomicPasteMinRunes)
	manyLines := strings.Repeat("line\n", museAtomicPasteMinLines)
	fewLines := strings.Repeat("line\n", museAtomicPasteMinLines-1)
	for _, tc := range []struct {
		name   string
		prompt string
		want   bool
	}{
		{"short prompt types literally", "hello", false},
		{"multiline under threshold types literally", "a\nb\nc", false},
		{"rune threshold pastes atomically", largeRunes, true},
		{"line threshold pastes atomically", manyLines, true},
		{"just under line threshold types literally", strings.TrimSuffix(fewLines, "\n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := musePromptNeedsAtomicPaste(tc.prompt); got != tc.want {
				t.Fatalf("musePromptNeedsAtomicPaste(%q...) = %v, want %v", tc.prompt[:min(20, len(tc.prompt))], got, tc.want)
			}
		})
	}
}
