package llmtypes

import (
	"context"
	"time"
)

// TrailingCaptureScraper takes a single snapshot of a terminal pane.
// Each interactive adapter passes its own implementation (each CLI
// uses a slightly different tmux capture-pane invocation and ANSI
// stripper), so we accept a callback rather than baking pane mechanics
// into llmtypes.
type TrailingCaptureScraper func(ctx context.Context) (string, error)

// RunTrailingPaneCapture polls the supplied scraper for TmuxKillDelay
// after the agent's main response has been parsed. Any new pane
// snapshots are emitted as terminal-typed StreamChunks so trailing CLI
// output (post-processing logs, "press any key" lines, etc.) lands in
// the rail before the tmux session is torn down.
//
// Without this, the response-wait loop inside each adapter exits as
// soon as completion is detected, and the StreamChan would close
// before any late output could be captured.
//
// Caller supplies the metadata that should ride on each chunk so
// downstream consumers (terminals store, frontend) can attribute the
// snapshot to the right pane.
func RunTrailingPaneCapture(
	ctx context.Context,
	streamChan chan<- StreamChunk,
	scrape TrailingCaptureScraper,
	metadata map[string]interface{},
) {
	if streamChan == nil || scrape == nil {
		return
	}
	deadline := time.Now().Add(TmuxKillDelay)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var lastSnapshot string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		snapshot, err := scrape(ctx)
		if err != nil {
			continue
		}
		if snapshot == "" || snapshot == lastSnapshot {
			continue
		}
		lastSnapshot = snapshot
		select {
		case streamChan <- StreamChunk{
			Type:     StreamChunkTypeTerminal,
			Content:  snapshot,
			Metadata: metadata,
		}:
		case <-ctx.Done():
			return
		default:
		}
	}
}
