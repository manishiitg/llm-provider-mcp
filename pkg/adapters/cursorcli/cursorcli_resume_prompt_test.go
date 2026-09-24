package cursorcli

import "testing"

func TestCursorResumeSystemPromptChangedResendsOnlyOnChange(t *testing.T) {
	dir := t.TempDir()
	if !cursorResumeSystemPromptChanged(dir, "chat-1", "rules v1") {
		t.Fatal("first resume without a marker must resend the prompt")
	}
	if cursorResumeSystemPromptChanged(dir, "chat-1", "rules v1") {
		t.Fatal("an unchanged prompt must not be resent")
	}
	if !cursorResumeSystemPromptChanged(dir, "chat-1", "rules v2") {
		t.Fatal("a changed prompt must be resent")
	}
	if !cursorResumeSystemPromptChanged(dir, "chat-2", "rules v2") {
		t.Fatal("markers are per native chat")
	}
	if cursorResumeSystemPromptChanged(dir, "chat-1", "  ") || cursorResumeSystemPromptChanged(dir, "", "rules") {
		t.Fatal("no prompt or no resume id means nothing to resend")
	}
}
