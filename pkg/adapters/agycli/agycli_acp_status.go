package agycli

import (
	"context"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// GetStatusLine returns only this owner's active native session context record.
func (a *AgyCLIAdapter) GetStatusLine(_ context.Context, owner string) (*llmtypes.StatusLine, error) {
	agyACPSessions.Lock()
	s := agyACPSessions.m[owner]
	agyACPSessions.Unlock()
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil {
		return nil, nil
	}
	result := *s.status
	result.Metadata = make(map[string]interface{}, len(s.status.Metadata))
	for k, v := range s.status.Metadata {
		result.Metadata[k] = v
	}
	return &result, nil
}
