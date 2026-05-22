package llmtypes

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// transportLabel returns the explicit transport ("api" / "structured_cli"
// / "tmux") when the caller set one, otherwise falls back to "non_tmux"
// for backwards compatibility with the old hard-coded value.
func (t *SyntheticTerminal) transportLabel() string {
	if t == nil || t.transport == "" {
		return "non_tmux"
	}
	return t.transport
}

// SyntheticTerminal accumulates a terminal-style log for transports
// that have no real tmux pane to snapshot (structured CLI + direct
// API providers). The log is emitted as StreamChunkTypeTerminal chunks
// with tmux-replace semantics: each chunk carries the FULL buffer so
// the frontend can render it with the existing terminal-pane code path.
//
// Why a separate type instead of doing this inline in each adapter:
// every adapter needs the same line cap, the same non-blocking emit,
// the same metadata shape. Five copies drift.
type SyntheticTerminal struct {
	mu        sync.Mutex
	lines     []string
	maxLines  int
	provider  string
	model     string
	transport string
	ch        chan<- StreamChunk
}

// NewSyntheticTerminal builds a log bound to a single StreamChan. If
// ch is nil, the returned terminal is a no-op (Append + Emit are
// safe to call but emit nothing). The caller passes provider/model
// for metadata enrichment.
func NewSyntheticTerminal(ch chan<- StreamChunk, provider, model string) *SyntheticTerminal {
	return NewSyntheticTerminalWithTransport(ch, provider, model, "")
}

// NewSyntheticTerminalWithTransport is like NewSyntheticTerminal but
// also records the actual transport class ("api" / "structured_cli" /
// "tmux"). The chip in the frontend reads this from the chunk
// metadata so a claude (tmux) call is labelled tmux·claude code
// rather than the wrong-by-default api·claude code.
func NewSyntheticTerminalWithTransport(ch chan<- StreamChunk, provider, model, transport string) *SyntheticTerminal {
	return &SyntheticTerminal{
		ch:        ch,
		provider:  provider,
		model:     model,
		transport: transport,
		maxLines:  200, // keep the pane bounded; older lines drop off the top
	}
}

// Enabled reports whether emits will reach a consumer.
func (t *SyntheticTerminal) Enabled() bool {
	return t != nil && t.ch != nil
}

// Header appends an opening banner ($ prompt + summary) and emits.
func (t *SyntheticTerminal) Header(cmdline string) {
	if !t.Enabled() {
		return
	}
	t.appendLine(fmt.Sprintf("$ %s", cmdline))
	t.emit()
}

// Line appends a single formatted line and emits a snapshot.
func (t *SyntheticTerminal) Line(format string, args ...interface{}) {
	if !t.Enabled() {
		return
	}
	t.appendLine(fmt.Sprintf(format, args...))
	t.emit()
}

// ToolStart records a tool invocation. Args are truncated for display.
func (t *SyntheticTerminal) ToolStart(name, args string) {
	if !t.Enabled() {
		return
	}
	t.appendLine(fmt.Sprintf("→ %s %s", name, truncate(args, 120)))
	t.emit()
}

// ToolEnd records a tool result with duration. Output is truncated.
func (t *SyntheticTerminal) ToolEnd(name, result string, dur time.Duration) {
	if !t.Enabled() {
		return
	}
	resultSummary := truncate(strings.ReplaceAll(result, "\n", " "), 200)
	t.appendLine(fmt.Sprintf("✓ %s (%s) %s", name, dur.Round(time.Millisecond), resultSummary))
	t.emit()
}

// AssistantText appends streamed assistant text. Token-level deltas
// concatenate onto the trailing assistant pane line so the pane reads
// like a streaming reply. When the delta carries newlines, each
// newline-separated segment becomes its own pane line with the "  "
// continuation prefix so the frontend parser keeps classifying the
// whole block as the same assistant row — the row coalesces back
// together with newlines preserved, which is what ReactMarkdown needs
// to render structure (lists, headings, paragraph breaks, fenced code)
// instead of one collapsed run-on string.
func (t *SyntheticTerminal) AssistantText(delta string) {
	if !t.Enabled() || delta == "" {
		return
	}
	t.mu.Lock()
	segments := strings.Split(delta, "\n")
	for i, seg := range segments {
		if i == 0 {
			// First segment continues the existing trailing assistant
			// line (or starts a new one if there isn't one yet).
			if len(t.lines) > 0 && strings.HasPrefix(t.lines[len(t.lines)-1], "  ") {
				t.lines[len(t.lines)-1] += seg
			} else {
				t.lines = append(t.lines, "  "+seg)
			}
			continue
		}
		// Subsequent segments are separate pane lines, still tagged as
		// assistant continuation via the leading two spaces so the
		// frontend parser keeps coalescing them into the same row.
		t.lines = append(t.lines, "  "+seg)
	}
	t.trim()
	t.mu.Unlock()
	t.emit()
}

// Done records a final summary line.
func (t *SyntheticTerminal) Done(durationMs int64, summary string) {
	if !t.Enabled() {
		return
	}
	dur := humanizeDuration(durationMs)
	if summary != "" {
		t.appendLine(fmt.Sprintf("[done · %s · %s]", dur, summary))
	} else {
		t.appendLine(fmt.Sprintf("[done · %s]", dur))
	}
	t.emit()
}

// humanizeDuration converts a millisecond count into a compact
// human-readable form so terminal users don't have to mentally
// convert "30691ms" into "30.7s". Scales:
//   - under 1s   → "234ms"
//   - under 60s  → "30.7s"
//   - under 1h   → "2m 14s"
//   - 1h+        → "1h 5m"
func humanizeDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	sec := float64(ms) / 1000
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	secs := ms / 1000
	mins := secs / 60
	rem := secs % 60
	if mins < 60 {
		return fmt.Sprintf("%dm %ds", mins, rem)
	}
	hours := mins / 60
	remMin := mins % 60
	return fmt.Sprintf("%dh %dm", hours, remMin)
}

// Error records a terminal failure line.
func (t *SyntheticTerminal) Error(err error) {
	if !t.Enabled() {
		return
	}
	t.appendLine(fmt.Sprintf("[error] %s", err))
	t.emit()
}

func (t *SyntheticTerminal) appendLine(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	t.trim()
}

// trim drops the oldest lines if the buffer exceeds maxLines.
// Caller holds t.mu.
func (t *SyntheticTerminal) trim() {
	if len(t.lines) > t.maxLines {
		t.lines = t.lines[len(t.lines)-t.maxLines:]
	}
}

func (t *SyntheticTerminal) snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// emit sends the current buffer as a Terminal chunk. Non-blocking:
// if the consumer is slow we drop this snapshot — the next emit
// will carry the freshest state anyway.
//
// Adapters own the close of opts.StreamChan and some close it before
// WithObservability's terminal Done fires (e.g. vertex defers close
// inside generateContentStreaming). A send to a closed channel
// panics, so we recover here — losing the final Done snapshot is
// preferable to crashing every vertex/structured-CLI call.
func (t *SyntheticTerminal) emit() {
	defer func() { _ = recover() }()
	snap := t.snapshot()
	select {
	case t.ch <- StreamChunk{
		Type:    StreamChunkTypeTerminal,
		Content: snap,
		Metadata: map[string]interface{}{
			// kind=terminal is the gate the orchestrator's
			// terminals store checks; without it our synthetic
			// snapshots are silently dropped before reaching the
			// /api/terminals endpoint the frontend pane reads.
			"kind":      "terminal",
			"source":    "synthetic",
			"provider":  t.provider,
			"model":     t.model,
			"transport": t.transportLabel(),
		},
	}:
	default:
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
