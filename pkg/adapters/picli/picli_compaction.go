package picli

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// piCompactionTracker maps native RPC compaction events to the shared stream.
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
