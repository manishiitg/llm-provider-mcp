package picli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A provider that rejects a request (a token rate limit, a bad request, an outage) shows Pi the HTTP status and a JSON body, and
// Pi prints it in its pane as "Error: 429: {...}" -- but Pi's own hooks do not always report it, so a turn that fails this way
// used to end empty ("No content generated") and the person saw nothing. When a turn produced no text and no tool call, the pane
// is the only place the reason is, so read it from there.
var (
	piPaneStatusPattern  = regexp.MustCompile(`\b([45]\d\d):\s*\{`)
	piPaneMessagePattern = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// piPaneProviderError returns "HTTP <status>: <message>" for the last provider error in a pane capture, or "" when there is none.
// Pi wraps long lines at the terminal width, so the text is joined across line breaks before it is read.
func piPaneProviderError(pane string) string {
	lines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
	if len(lines) > 60 {
		lines = lines[len(lines)-60:]
	}
	text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	matches := piPaneStatusPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	status := text[last[2]:last[3]]
	rest := text[last[1]:]
	message := ""
	if m := piPaneMessagePattern.FindStringSubmatch(rest); m != nil {
		message = strings.ReplaceAll(strings.ReplaceAll(m[1], `\"`, `"`), `\n`, " ")
		// The pane wraps inside words as well as between them; a space inserted by the join is not part of the message.
		message = strings.TrimSpace(message)
	}
	if message == "" {
		return "HTTP " + status
	}
	return "HTTP " + status + ": " + message
}

// piTurnProviderError reads the pane of an empty turn and returns its provider error as a Go error, or nil.
func piTurnProviderError(sessionName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pane, err := capturePiPane(ctx, sessionName)
	if err != nil {
		return nil
	}
	if reason := piPaneProviderError(pane); reason != "" {
		return fmt.Errorf("pi-cli provider request failed: %s", reason)
	}
	return nil
}
