package llmtypes

import "time"

// TmuxKillDelay is the window between a bounded coding-agent turn completing
// and its tmux session being torn down. Keep this aligned with the terminal
// rail's default retention so the UI does not advertise a live/debuggable
// terminal after the provider has already killed the real tmux process.
//
// Shared across all interactive adapters (claudecode_experimental,
// codex_interactive, gemini_interactive, cursor_interactive) so the
// behavior is uniform; per-adapter overrides via env vars still apply
// to the rail-display retention, not this kill delay.
const TmuxKillDelay = 30 * time.Minute
