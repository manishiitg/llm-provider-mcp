package cursorcli

import (
	"reflect"
	"strings"
	"testing"
)

// TestCursorEventMessageTextSpacesBlockBoundaries reproduces a real Video
// Studio run: Cursor's stream sends the reply as separate text blocks per
// checkpoint, each already trimmed. A bare join glued them into a run-on
// word ("scenes.I'll") in the persisted transcript.
func TestCursorEventMessageTextSpacesBlockBoundaries(t *testing.T) {
	msg := &cursorEventMessage{Content: []cursorEventContent{
		{Type: "text", Text: "Visual system is locked."},
		{Type: "text", Text: "I'll build the eight scenes next."},
	}}
	got := cursorEventMessageText(msg)
	want := "Visual system is locked. I'll build the eight scenes next."
	if got != want {
		t.Fatalf("cursorEventMessageText = %q, want %q", got, want)
	}
}

// TestCursorEventMessageTextDoesNotDoubleSpace covers a block that already
// carries its own leading/trailing whitespace (token-level deltas), which
// must not gain an extra space on top of what the block already supplies.
func TestCursorEventMessageTextDoesNotDoubleSpace(t *testing.T) {
	msg := &cursorEventMessage{Content: []cursorEventContent{
		{Type: "text", Text: "Hello"},
		{Type: "text", Text: " world"},
		{Type: "text", Text: "!\n"},
		{Type: "text", Text: "\nNext paragraph."},
	}}
	got := cursorEventMessageText(msg)
	want := "Hello world!\n\nNext paragraph."
	if got != want {
		t.Fatalf("cursorEventMessageText = %q, want %q", got, want)
	}
}

// TestCursorEventMessageTextSkipsNonTextBlocks ignores non-text content
// (e.g. tool_use) and empty text blocks when building the joined string.
func TestCursorEventMessageTextSkipsNonTextBlocks(t *testing.T) {
	msg := &cursorEventMessage{Content: []cursorEventContent{
		{Type: "text", Text: "Before."},
		{Type: "tool_use", Text: "ignored"},
		{Type: "text", Text: ""},
		{Type: "text", Text: "After."},
	}}
	got := cursorEventMessageText(msg)
	want := "Before. After."
	if got != want {
		t.Fatalf("cursorEventMessageText = %q, want %q", got, want)
	}
}

// TestJoinTextWithSpacingReproducesCursorToolSplitBug is the exact failure
// captured live from cursor-agent's own stream-json output: a tool call
// between two text spans made Cursor's own end-of-turn "result" field read
// `"Checking the file now.Done reading it."` — no space, straight from the
// CLI itself. generateContentStructured now reconstructs the turn from its
// own per-span segments (banked at each tool_call "started" event) via this
// function instead of trusting that field.
func TestJoinTextWithSpacingReproducesCursorToolSplitBug(t *testing.T) {
	got := joinTextWithSpacing([]string{"Checking the file now.", "Done reading it."})
	want := "Checking the file now. Done reading it."
	if got != want {
		t.Fatalf("joinTextWithSpacing = %q, want %q", got, want)
	}
}

// TestJoinTextWithSpacingSkipsEmptySegments covers a tool_call "started"
// event that fires with no text span pending (e.g. two tool calls back to
// back), which must not inject a spurious empty segment.
func TestJoinTextWithSpacingSkipsEmptySegments(t *testing.T) {
	got := joinTextWithSpacing([]string{"First.", "", "Second."})
	want := "First. Second."
	if got != want {
		t.Fatalf("joinTextWithSpacing = %q, want %q", got, want)
	}
}

func has(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestBuildCursorStructuredArgs pins the structured argv shape, including the
// native --resume flag and the mutual exclusion of --force and hooks.
func TestBuildCursorStructuredArgs(t *testing.T) {
	t.Run("full turn with resume", func(t *testing.T) {
		got := buildCursorStructuredArgs("/work", "gpt-5", "ask", "danger", true, false, true, false, "cur-sess-1", "hello")
		want := []string{
			"--print",
			"--output-format", "stream-json",
			"--stream-partial-output",
			"--trust",
			"--force",
			"--workspace", "/work",
			"--model", "gpt-5",
			"--mode", "ask",
			"--sandbox", "danger",
			"--approve-mcps",
			"--resume", "cur-sess-1",
			"hello",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("argv mismatch:\n got=%v\nwant=%v", got, want)
		}
	})

	t.Run("fresh turn: no --resume", func(t *testing.T) {
		got := buildCursorStructuredArgs("/work", "gpt-5", "", "", false, false, true, false, "", "hi")
		if has(got, "--resume") {
			t.Errorf("fresh turn must not carry --resume, got %v", got)
		}
		if has(got, "--mode") {
			t.Errorf("no mode => no --mode flag, got %v", got)
		}
		// The prompt is always the final positional arg.
		if got[len(got)-1] != "hi" {
			t.Errorf("prompt must be the last arg, got %v", got)
		}
	})
}

// --force bypasses cursor hooks, so shipping both would leave the deny-builtin
// denylist installed but inert — the agent would keep full built-in access while
// the caller believed it was contained.
func TestStructuredArgsWithholdForceWhenHooksInstalled(t *testing.T) {
	got := buildCursorStructuredArgs("/work", "gpt-5", "", "", true, true, true, false, "", "hi")
	if has(got, "--force") {
		t.Fatalf("--force must be withheld when hooks are installed, got %v", got)
	}
	if has(got, "--mode") {
		t.Fatalf("hooks provide containment, so no read-only --mode is needed, got %v", got)
	}
}

// Without hooks there is nothing to bypass, and --force keeps the one-shot run
// from stalling on a permission prompt no one can answer.
func TestStructuredArgsKeepForceWithoutHooks(t *testing.T) {
	got := buildCursorStructuredArgs("/work", "gpt-5", "", "", true, false, true, false, "", "hi")
	if !has(got, "--force") {
		t.Fatalf("--force expected when no hooks are installed, got %v", got)
	}
}

func TestStructuredArgsAutoReviewKeepsNativeToolsWithoutForce(t *testing.T) {
	got := buildCursorStructuredArgs("/work", "auto", "", "enabled", true, false, false, true, "", "hi")
	if !has(got, "--auto-review") {
		t.Fatalf("--auto-review missing: %v", got)
	}
	if has(got, "--force") {
		t.Fatalf("provider auto must not pass --force: %v", got)
	}
}

func TestCursorStructuredCLIConfigPreapprovesInjectedMCPServers(t *testing.T) {
	mcpJSON := `{"mcpServers":{"api-bridge":{"command":"mlp-bridge"},"github":{"url":"https://example.invalid/mcp"}}}`

	out, ok, err := cursorStructuredCLIConfig(mcpJSON, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected an allowlist config for an mcp.json with servers")
	}
	for _, want := range []string{`"Mcp(api-bridge:*)"`, `"Mcp(github:*)"`, `"deny":[]`} {
		if !strings.Contains(out, want) {
			t.Fatalf("allowlist config missing %s: %s", want, out)
		}
	}

	// A caller-supplied project config wins verbatim, as in the tmux path.
	custom := `{"permissions":{"allow":["Mcp(api-bridge:execute_shell_command)"]}}`
	out, ok, err = cursorStructuredCLIConfig(mcpJSON, custom)
	if err != nil || !ok || out != custom {
		t.Fatalf("caller config not honoured: ok=%v err=%v out=%s", ok, err, out)
	}

	// Nothing to write for an mcp.json without servers.
	if _, ok, err := cursorStructuredCLIConfig(`{"mcpServers":{}}`, ""); err != nil || ok {
		t.Fatalf("expected no config for an empty server list: ok=%v err=%v", ok, err)
	}
}
