// Package paneview holds helpers for normalising tmux pane snapshots
// before they reach the UI. Adapters that stream live capture-pane output
// (claude-code, codex, gemini, cursor, agy, opencode) all share the same
// hygiene needs, so the logic lives once here.
package paneview

import "strings"

// maxConsecutiveBlankLines caps how many blank rows survive a collapse. CLI
// agents redraw their loading spinners ("Generating...") via cursor
// positioning, and `capture-pane -e` materialises every frame's current pane
// state into scrollback — typically the spinner row followed by ~25 empty rows
// of pane area — so the runs must be capped or the snapshot becomes mostly
// whitespace. But the TUIs deliberately separate sections with 2–3 blank rows;
// capping at 1 stacked those sections directly on top of each other and made
// the pane hard to read once it flipped to a re-captured snapshot. Keeping up
// to 2 preserves that visual separation while still squashing the spinner
// gaps.
const maxConsecutiveBlankLines = 2

// CollapseBlankRuns squeezes any run of blank/whitespace-only lines down to at
// most maxConsecutiveBlankLines and trims trailing whitespace from each line.
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
			if blankRun <= maxConsecutiveBlankLines {
				out = append(out, "")
			}
			continue
		}
		blankRun = 0
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}
