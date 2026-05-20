package llmtypes

import "time"

// TmuxKillDelay is the grace window between a bounded coding-agent turn
// completing and its tmux session being torn down. The periodic pane
// scraper continues running during this window so any trailing CLI
// output is captured in the final snapshot before kill.
//
// After kill, the snapshot persists in the terminals store as a
// read-only record (controlled separately by terminal_retention_seconds
// in chunk metadata) — but the tmux process itself is gone, freeing
// the shell PID and pane resources.
//
// Shared across all interactive adapters (claudecode_experimental,
// codex_interactive, gemini_interactive, cursor_interactive) so the
// behavior is uniform; per-adapter overrides via env vars still apply
// to the rail-display retention, not this kill delay.
const TmuxKillDelay = 30 * time.Second
