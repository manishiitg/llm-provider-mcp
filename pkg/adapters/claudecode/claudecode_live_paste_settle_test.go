package claudecode

import "testing"

// The pane captured when TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape
// timed out: a short multi-line live message sat inline in the composer (no
// "[Pasted text" chip) while the turn's spinner kept the pane changing.
const claudeInlineMultilineDraftPane = `❯ Call the api-bridge slow_contract MCP tool with token SLOW_CLAUDE_E2E_a78e3fd9 and delay_ms 30000. Do not answer
  until the tool returns. Then reply exactly with the tool result text.

⏺ Calling api-bridge… (ctrl+o to expand)

✽ Ionizing… (7s · ↓ 138 tokens)

──────────────────────────────────────────────────────────── mcp-agent-20260923-130442 ─
❯ ## Pre-validation failed (retry attempt 3)

  Fix the specific issue above and re-produce the required outputs.
────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ don't ask on (shift+tab to cycle)`

func TestClaudeComposerTextReadsWholeMultilineDraft(t *testing.T) {
	message := "## Pre-validation failed (retry attempt 3)\n\nFix the specific issue above and re-produce the required outputs."
	composer := claudeComposerText(claudeInlineMultilineDraftPane)
	if !claudePromptDraftStillMatchesMessage(composer, message) {
		t.Fatalf("composer %q does not match the pasted message", composer)
	}
	// The conversation echo of the first prompt (also starting with ❯) must not
	// be mistaken for the input box.
	if composer == "" || composer[:2] != "##" {
		t.Fatalf("composer = %q, want the input box, not the conversation echo", composer)
	}
	// Only the first line was visible to the old draft reader.
	if draft, _, _ := latestClaudePromptDraftRaw(claudeInlineMultilineDraftPane); draft != "## Pre-validation failed (retry attempt 3)" {
		t.Fatalf("latestClaudePromptDraftRaw = %q", draft)
	}
}

// The typed authorization line must not change what a transcript row matches.
func TestClaudeNormalizeRowTextStripsPasteAuthorization(t *testing.T) {
	msg := "line one\nline two"
	row := "\n\n<pasted_content id=\"4465\">\n" + msg + "\n</pasted_content id=\"4465\">\n " + claudePastedContentAuthorization
	if !claudeRowTextMatches(row, msg) {
		t.Fatalf("row %q does not match message %q after normalization", row, msg)
	}
	if !claudeLiveInputNeedsPasteSettlement(msg) || claudeLiveInputNeedsPasteSettlement("one line") {
		t.Fatal("authorization must be typed for multi-line pastes only (and large ones)")
	}
}

func TestSplitClaudeLeadingSlashCommand(t *testing.T) {
	cmd, body, ok := splitClaudeLeadingSlashCommand("/runtime-self-check\n\nRun only the skill part.\nReply SKILL_CANARY=<value>.")
	if !ok || cmd != "/runtime-self-check" || body != "Run only the skill part.\nReply SKILL_CANARY=<value>." {
		t.Fatalf("split = %q, %q, %v", cmd, body, ok)
	}
	for _, prompt := range []string{
		"/runtime-self-check",              // lone command: paste as is
		"Please run /runtime-self-check",   // not leading
		"/path/to/file is broken\nfix it",  // a path, not a command name
		"## Pre-validation failed\n\nFix.", // ordinary multi-line prompt
	} {
		if _, _, ok := splitClaudeLeadingSlashCommand(prompt); ok {
			t.Fatalf("%q should not be split", prompt)
		}
	}
}
