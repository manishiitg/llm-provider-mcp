package agycli

import (
	"context"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// streamAgyTerminal keeps the retained tmux process visible to the host UI.
// Pane frames are display only; intake, final text, and tool receipts still
// come from AGY's structured conversation records.
func streamAgyTerminal(ctx context.Context, tmuxName string, out chan<- llmtypes.StreamChunk) func() {
	if out == nil {
		return func() {}
	}
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		last := ""
		for {
			pane, err := captureAgyPane(ctx, tmuxName)
			pane = strings.TrimRight(pane, "\n")
			if err == nil && strings.TrimSpace(pane) != "" && pane != last {
				last = pane
				chunk := llmtypes.StreamChunk{
					Type:    llmtypes.StreamChunkTypeTerminal,
					Content: pane,
					Metadata: map[string]interface{}{
						"tmux_session":      tmuxName,
						"agy_stream_source": "tmux-screen",
					},
				}
				select {
				case out <- chunk:
				case <-done:
					return
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ticker.C:
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}
