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

// TestMuseDiscoverSessionSinceMultilineSnippet pins the intake false
// negative: session.jsonl stores newlines JSON-escaped, so a snippet holding
// a literal newline must still match the log's backslash-n bytes. Every
// multi-line builder prompt failed intake while single-line test prompts
// always matched.
func TestMuseDiscoverSessionSinceMultilineSnippet(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "muse", "sessions", "2026", "09", "10")
	dir := filepath.Join(day, "sess-multi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Raw log bytes as the CLI writes them: JSON-escaped newlines.
	content := `{"payload":{"record":{"text":"# Workflow Builder Agent\n\nYou design, run, monitor."}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Minute)
	id, _, err := museDiscoverSessionSince(home, since, "# Workflow Builder Agent\n\nYou design")
	if err != nil {
		t.Fatalf("discover multiline snippet: %v", err)
	}
	if id != "sess-multi" {
		t.Fatalf("session id = %q, want sess-multi", id)
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

// TestMuseSplitPrompt pins the file-only contract kernel for both lanes:
// system-role texts separate from the human turn (tmux lane projects them
// to AGENTS.md; the exec lane concatenates via museInlinePrompt). A missing
// human turn is an error in both lanes.
func TestMuseSplitPrompt(t *testing.T) {
	msgs := func() []llmtypes.MessageContent {
		return []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "system-one"}}},
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "system-two"}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "hi"}}},
		}
	}
	system, human, err := museSplitPrompt(msgs())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(system) != 2 || system[0] != "system-one" || system[1] != "system-two" {
		t.Fatalf("system = %q, want both system texts in order", system)
	}
	if human != "hi" {
		t.Fatalf("human = %q, want hi", human)
	}
	// Exec-lane concatenation and the file-only fallback share museInlinePrompt.
	if got := museInlinePrompt(system, human); got != "system-one\n\nsystem-two\n\nhi" {
		t.Fatalf("inline = %q, want concatenated preamble + human", got)
	}
	if got := museInlinePrompt(nil, human); got != "hi" {
		t.Fatalf("inline without system = %q, want bare human", got)
	}
	if _, _, err := museSplitPrompt([]llmtypes.MessageContent{
		{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "only system"}}},
	}); err == nil {
		t.Fatal("expected error when no human turn exists")
	}
}

// TestWriteMuseProjectAgentsFile pins the codex-mirrored byte-restore
// contract: a pre-existing operator AGENTS.md comes back byte-for-byte when
// restorePrior is set, and a projected file is removed otherwise.
func TestWriteMuseProjectAgentsFile(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "AGENTS.md")
	operator := "# operator rules\n"
	if err := os.WriteFile(path, []byte(operator), 0o600); err != nil {
		t.Fatal(err)
	}
	restore, err := writeMuseProjectAgentsFile(workdir, "session instructions", true)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read projected: %v", err)
	}
	if !strings.Contains(string(raw), "session instructions") || !strings.Contains(string(raw), "mlp-session-instructions") {
		t.Fatalf("projected AGENTS.md missing marker or content:\n%s", raw)
	}
	restore()
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(raw) != operator {
		t.Fatalf("restored = %q, want operator content byte-for-byte", raw)
	}

	plain := t.TempDir()
	restore, err = writeMuseProjectAgentsFile(plain, "session instructions", false)
	if err != nil {
		t.Fatalf("write without restore: %v", err)
	}
	restore()
	if _, err := os.Stat(filepath.Join(plain, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("projected AGENTS.md not removed on cleanup without restorePrior")
	}
}

// TestProjectMuseAgentsForTurnFallback pins the never-drop contract: when
// the projection write fails, the caller gets (nil, false) and types the
// preamble inline instead of losing it.
func TestProjectMuseAgentsForTurnFallback(t *testing.T) {
	if restore, projected := projectMuseAgentsForTurn("", []string{"sys"}, true, false); restore != nil || projected {
		t.Fatal("empty workdir must not project")
	}
	// A file where the workdir should be makes MkdirAll/Write fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if restore, projected := projectMuseAgentsForTurn(filepath.Join(blocker, "sub"), []string{"sys"}, true, false); restore != nil || projected {
		t.Fatal("unwritable workdir must fall back to inline, not claim projection")
	}
	if restore, projected := projectMuseAgentsForTurn(t.TempDir(), []string{"sys"}, true, true); restore == nil || !projected {
		t.Fatal("writable workdir must project")
	} else {
		restore()
	}
}
