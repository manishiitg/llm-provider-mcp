package llmtypes

import "time"

// NativeSubagentTranscript is a provider-recorded child conversation, bound to
// the native parent session. It is evidence for inspection, never model input.
type NativeSubagentTranscript struct {
	ID        string           `json:"id"`
	ParentID  string           `json:"parent_id"`
	Name      string           `json:"name"`
	Provider  string           `json:"provider"`
	Status    string           `json:"status"`
	StartedAt time.Time        `json:"started_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Messages  []MessageContent `json:"messages"`
}

const StreamChunkTypeNativeSubagentTranscript StreamChunkType = "native_subagent_transcript"
