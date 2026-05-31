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
	lines = pruneInputBoxTrailer(lines)
	lines = pruneSpinnerLines(lines)
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

	// Trim leading empty lines
	start := 0
	for start < len(out) && out[start] == "" {
		start++
	}
	out = out[start:]

	return strings.Join(out, "\n")
}

// pruneSpinnerLines identifies lines containing Braille spinner characters.
// Any historical spinner frame that has been scrolled up (meaning there are
// non-blank lines following it) is pruned. Only the active spinner frame
// at the very end (followed only by blank lines) is preserved.
func pruneSpinnerLines(lines []string) []string {
	lastBrailleIdx := -1
	for i, line := range lines {
		if hasBraille(line) {
			lastBrailleIdx = i
		}
	}
	if lastBrailleIdx == -1 {
		return lines
	}

	isActive := true
	for j := lastBrailleIdx + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) != "" {
			isActive = false
			break
		}
	}

	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if hasBraille(line) {
			if i == lastBrailleIdx && isActive {
				out = append(out, line)
			}
			// Otherwise, prune it (skip)
		} else {
			out = append(out, line)
		}
	}
	return out
}

func hasBraille(s string) bool {
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

// pruneInputBoxTrailer removes the agy input-box region and everything below
// it. The input box appears as a run of ─ box-drawing characters (the top
// border), followed by the › prompt line, followed by another ─ run (bottom
// border). Below the bottom border the pane contains empty space and animation
// cursor-positioning artifacts ("oa", "ad", "di" — fragments of a word being
// overwritten mid-frame) that must not be shown to the user.
//
// We find the last run of ≥20 consecutive ─ characters (the bottom border of
// the input box, which is always the wider of the two) and strip from the top
// border (the ─ run just above the › line) onward. Using the last long ─ run
// as the anchor avoids false-positives on ─ dividers inside tool output.
func pruneInputBoxTrailer(lines []string) []string {
	// Find the index of the last box-border line (≥20 ─ chars).
	lastBorderIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 20 && isAllDashes(trimmed) {
			lastBorderIdx = i
		}
	}
	if lastBorderIdx < 0 {
		return lines
	}
	// Walk back from lastBorderIdx to find the matching top border
	// (the ─ line just before the › prompt). The top border is the
	// nearest ─ line above lastBorderIdx.
	topBorderIdx := -1
	for i := lastBorderIdx - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if len(trimmed) >= 20 && isAllDashes(trimmed) {
			topBorderIdx = i
			break
		}
		// Allow only blank lines and a single › prompt line between the two borders.
		if strings.TrimSpace(trimmed) != "" && !strings.HasPrefix(strings.TrimSpace(trimmed), ">") {
			break
		}
	}
	cutAt := lastBorderIdx + 1 // strip from bottom border onward
	if topBorderIdx >= 0 {
		cutAt = topBorderIdx // strip from top border onward (cleaner)
	}
	return lines[:cutAt]
}

func isAllDashes(s string) bool {
	for _, r := range s {
		if r != '─' && r != '-' && r != '━' {
			return false
		}
	}
	return true
}

