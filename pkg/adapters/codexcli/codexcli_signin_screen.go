package codexcli

import "strings"

// CodexLoginRequiredError means Codex's terminal is waiting on its own sign-in screen, so a turn can never
// start in it. The text is what the person sees.
type CodexLoginRequiredError struct{}

func (*CodexLoginRequiredError) Error() string {
	return "Codex is not signed in for this account. Reconnect your Codex account in your account settings, or pick another agent. Your message is saved."
}

// codexPaneAsksForSignIn reports Codex's first-run sign-in screen: all three of its exact option lines in the visible
// tail of the pane. The older scrollback is ignored, so a screen the person already answered cannot trigger it.
func codexPaneAsksForSignIn(captured string) bool {
	lower := strings.ToLower(lastNCodexLines(stripCodexANSI(captured), 25))
	return strings.Contains(lower, "sign in with chatgpt") &&
		strings.Contains(lower, "provide your own api key") &&
		strings.Contains(lower, "press enter to continue")
}
