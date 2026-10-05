package picli

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// piCompactionTracker turns Pi's own compaction records into
// llmtypes.ContextCompaction chunks, for both transports:
//
//   - structured (`--mode json`): compaction_start {reason} and
//     compaction_end {reason, result{tokensBefore, estimatedTokensAfter}, aborted, willRetry}
//     (docs/json.md, AgentSessionEvent in dist/core/agent-session.d.ts).
//   - interactive (tmux): the marker extension's compaction_start /
//     compaction_end / compaction_failed records, written from Pi's
//     session_before_compact / session_compact / session_compact_failed
//     extension hooks (dist/core/extensions/types.d.ts).
//
// Trigger is Pi's reason verbatim ("manual", "threshold", "overflow"): the
// contract keeps the CLI's own reason when it is more specific than auto/manual.
// ID is the start time in Unix ms, so a start and its end share it; an end
// without a seen start (Pi fails a manual /compact before
// session_before_compact) carries no ID and no duration.
//
// One tracker per turn loop; not safe for concurrent use.
type piCompactionTracker struct {
	startedAt time.Time
}

func (t *piCompactionTracker) start(reason string, tokensBefore int, at time.Time) llmtypes.StreamChunk {
	t.startedAt = at
	return llmtypes.ContextCompactionChunk(llmtypes.ContextCompaction{
		Provider:     "pi-cli",
		Phase:        llmtypes.ContextCompactionPhaseStart,
		ID:           piCompactionID(at),
		Trigger:      reason,
		TokensBefore: tokensBefore,
		StartedAt:    at,
	})
}

func (t *piCompactionTracker) end(reason, outcome string, tokensBefore, tokensAfter int, at time.Time) llmtypes.StreamChunk {
	c := llmtypes.ContextCompaction{
		Provider:     "pi-cli",
		Phase:        llmtypes.ContextCompactionPhaseEnd,
		Trigger:      reason,
		Outcome:      outcome,
		TokensBefore: tokensBefore,
		TokensAfter:  tokensAfter,
		EndedAt:      at,
	}
	if !t.startedAt.IsZero() {
		c.ID = piCompactionID(t.startedAt)
		c.StartedAt = t.startedAt
		if d := at.Sub(t.startedAt); d >= 0 {
			c.DurationMs = d.Milliseconds()
		}
	}
	t.startedAt = time.Time{}
	return llmtypes.ContextCompactionChunk(c)
}

func piCompactionID(at time.Time) string {
	return "pi-compaction-" + strconv.FormatInt(at.UnixMilli(), 10)
}

// piCompactionOutcome maps Pi's end fields: a result means success, aborted
// means aborted (user abort or an extension's cancel), anything else failed.
func piCompactionOutcome(hasResult, aborted bool) string {
	switch {
	case hasResult:
		return llmtypes.ContextCompactionOutcomeSuccess
	case aborted:
		return llmtypes.ContextCompactionOutcomeAborted
	default:
		return llmtypes.ContextCompactionOutcomeFailed
	}
}

// fromStructuredEvent maps a `pi --mode json` compaction event. The JSON
// stream carries no timestamps, so now is the receive time.
func (t *piCompactionTracker) fromStructuredEvent(event piJSONEvent, now time.Time) (llmtypes.StreamChunk, bool) {
	switch event.Type {
	case "compaction_start":
		return t.start(event.Reason, 0, now), true
	case "compaction_end":
		var result struct {
			TokensBefore         int `json:"tokensBefore"`
			EstimatedTokensAfter int `json:"estimatedTokensAfter"`
		}
		hasResult := false
		if raw := bytes.TrimSpace(event.Result); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			hasResult = json.Unmarshal(raw, &result) == nil
		}
		return t.end(event.Reason, piCompactionOutcome(hasResult, event.Aborted),
			result.TokensBefore, result.EstimatedTokensAfter, now), true
	}
	return llmtypes.StreamChunk{}, false
}

// fromMarker maps the interactive marker extension's compaction records.
// Times come from the marker's own ts (ms), written inside Pi.
func (t *piCompactionTracker) fromMarker(marker piMarker) (llmtypes.StreamChunk, bool) {
	at := time.UnixMilli(marker.TS)
	if marker.TS <= 0 {
		at = time.Now()
	}
	switch marker.Type {
	case "compaction_start":
		return t.start(marker.Reason, marker.TokensBefore, at), true
	case "compaction_end":
		// session_compact carries the saved entry's tokensBefore; Pi does not
		// hand extensions the after-estimate, so TokensAfter stays unknown.
		return t.end(marker.Reason, llmtypes.ContextCompactionOutcomeSuccess, marker.TokensBefore, 0, at), true
	case "compaction_failed":
		aborted := marker.Aborted != nil && *marker.Aborted
		return t.end(marker.Reason, piCompactionOutcome(false, aborted), 0, 0, at), true
	}
	return llmtypes.StreamChunk{}, false
}
