package llmtypes

import (
	"errors"
	"fmt"
	"strings"
)

// CodingAgentTmuxSessionLostError marks failures caused by an already-launched
// tmux-backed coding-agent pane/session disappearing. Callers use this typed
// error to decide whether a provider-native continuation can retry once with a
// fresh tmux transport and the same native resume id.
type CodingAgentTmuxSessionLostError struct {
	Provider    string
	SessionName string
	Reason      string
	Err         error
}

func (e *CodingAgentTmuxSessionLostError) Error() string {
	if e == nil {
		return ""
	}
	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "coding agent"
	}
	session := strings.TrimSpace(e.SessionName)
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "tmux session lost"
	}
	base := fmt.Sprintf("%s %s", provider, reason)
	if session != "" {
		base = fmt.Sprintf("%s %q %s", provider, session, reason)
	}
	if e.Err == nil {
		return base
	}
	return fmt.Sprintf("%s: %v", base, e.Err)
}

func (e *CodingAgentTmuxSessionLostError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func WrapCodingAgentTmuxSessionLostError(err error, provider, sessionName, reason string) error {
	if err == nil {
		return nil
	}
	var existing *CodingAgentTmuxSessionLostError
	if errors.As(err, &existing) {
		return err
	}
	return &CodingAgentTmuxSessionLostError{
		Provider:    provider,
		SessionName: sessionName,
		Reason:      reason,
		Err:         err,
	}
}

func IsCodingAgentTmuxSessionLostError(err error) bool {
	var lostErr *CodingAgentTmuxSessionLostError
	return errors.As(err, &lostErr)
}
