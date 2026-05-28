// Package paneview holds helpers for normalising tmux pane snapshots
// before they reach the UI. Adapters that stream live capture-pane output
// (claude-code, codex, gemini, cursor, agy, opencode) all share the same
// hygiene needs, so the logic lives once here.
package paneview

import "strings"

// CollapseBlankRuns squeezes any run of blank/whitespace-only lines down to a
// single blank line and trims trailing whitespace from each line. CLI agents
// redraw their loading spinners ("Generating...") via cursor positioning, and
// `capture-pane -e` materialises every frame's current pane state into
// scrollback — typically the spinner row followed by ~25 empty rows of pane
// area. Without collapsing, the live-stream snapshot is mostly empty
// whitespace with rare lines of real content scattered between huge gaps.
// Paragraph breaks need only one blank line, so this is lossless for real
// content.
//
// Use this as the final step after stripping ANSI cursor escapes — color SGR
// is preserved upstream, blank-row noise is squashed here.
func CollapseBlankRuns(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blankRun := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" {
			blankRun++
			if blankRun <= 1 {
				out = append(out, "")
			}
			continue
		}
		blankRun = 0
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}
