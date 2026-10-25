package llmtypes

import "time"

// NativeRecord is a committed observation from one explicitly selected coding
// runtime. It is not a command, a permission grant, or platform-run completion.
// Text is present only for assistant replies and provider-authored questions;
// analytics consumers must not copy it into operational reports.
type NativeRecord struct {
	ID         string       `json:"id"`
	Provider   string       `json:"provider"`
	SessionID  string       `json:"native_session_id"`
	TurnID     string       `json:"native_turn_id,omitempty"`
	ParentID   string       `json:"parent_id,omitempty"`
	Kind       string       `json:"kind"`
	State      string       `json:"state,omitempty"`
	Origin     string       `json:"origin,omitempty"`
	Timestamp  time.Time    `json:"timestamp,omitempty"`
	Text       string       `json:"text,omitempty"`
	Name       string       `json:"name,omitempty"`
	ToolID     string       `json:"tool_id,omitempty"`
	TaskID     string       `json:"task_id,omitempty"`
	FollowupID string       `json:"followup_turn_id,omitempty"`
	Model      string       `json:"model,omitempty"`
	DurationMS int64        `json:"duration_ms,omitempty"`
	Usage      *NativeUsage `json:"usage,omitempty"`
}

// NativeUsage keeps the producer's scope explicit. Cumulative observations
// replace a previous observation of that scope; they must never be summed.
// A list-price estimate is not an invoice. Missing prices remain absent.
type NativeUsage struct {
	Scope       string   `json:"scope"`
	Cumulative  bool     `json:"cumulative"`
	Input       int64    `json:"input_tokens,omitempty"`
	Output      int64    `json:"output_tokens,omitempty"`
	CacheRead   int64    `json:"cache_read_tokens,omitempty"`
	CacheWrite  int64    `json:"cache_write_tokens,omitempty"`
	EstimateUSD *float64 `json:"estimated_usd,omitempty"`
}
