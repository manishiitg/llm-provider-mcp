package tmuxinput

import (
	"bytes"
	"errors"
)

// ErrInteractiveDraft proves no programmatic input was written. The caller
// can safely retry after the human submits or clears the native composer.
var ErrInteractiveDraft = errors.New("finish or clear the terminal draft before sending another message")

type interactiveDraft struct {
	version       uint64
	submitVersion uint64
}

func (b *Broker) ClearInteractiveDraft(sessionID string) {
	b.mu.Lock()
	delete(b.interactiveDrafts, sessionID)
	delete(b.interactiveSubmissions, sessionID)
	b.mu.Unlock()
}

// NoteInteractiveInput runs inside the input transaction, before bytes reach
// tmux. Enter is only an attempt (it can select a slash-menu item). A native
// user record, rather than the key, releases the reservation.
func (b *Broker) NoteInteractiveInput(sessionID string, data []byte) {
	if len(data) == 0 {
		return
	}
	if isTerminalReport(data) {
		return
	}
	if bytes.HasPrefix(data, []byte("\x1b[200~")) && bytes.HasSuffix(data, []byte("\x1b[201~")) {
		b.NoteInteractivePaste(sessionID)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes.Equal(data, []byte{0x03}) {
		delete(b.interactiveDrafts, sessionID)
		return
	}
	draft := b.interactiveDrafts[sessionID]
	if draft == nil {
		draft = &interactiveDraft{}
		b.interactiveDrafts[sessionID] = draft
	}
	draft.version++
	if bytes.ContainsAny(data, "\r\n") {
		draft.submitVersion = draft.version
		b.interactiveSubmissions[sessionID]++
	}
}

// Emulator replies and mouse reports are not edits to the CLI composer.
func isTerminalReport(data []byte) bool {
	if bytes.Equal(data, []byte("\x1b[I")) || bytes.Equal(data, []byte("\x1b[O")) || bytes.HasPrefix(data, []byte("\x1b[M")) {
		return true
	}
	if bytes.HasPrefix(data, []byte("\x1b]")) || bytes.HasPrefix(data, []byte("\x1bP")) {
		return true
	}
	if len(data) < 3 || data[0] != 0x1b || data[1] != '[' {
		return false
	}
	last := data[len(data)-1]
	return last == 'R' || last == 'c' || last == 'n' || (data[2] == '<' && (last == 'M' || last == 'm'))
}

func (b *Broker) HasInteractiveSubmissions(sessionID string) bool {
	return b.InteractiveSubmissionCount(sessionID) > 0
}

func (b *Broker) InteractiveSubmissionCount(sessionID string) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.interactiveSubmissions[sessionID]
}

// A paste fills the composer. Its embedded newlines are not Enter presses.
func (b *Broker) NoteInteractivePaste(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	draft := b.interactiveDrafts[sessionID]
	if draft == nil {
		draft = &interactiveDraft{}
		b.interactiveDrafts[sessionID] = draft
	}
	draft.version++
}

func (b *Broker) HasInteractiveDraft(sessionID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.interactiveDrafts[sessionID] != nil
}

// ConfirmInteractiveSubmission does not clear a newer draft typed while the
// previous Enter was being accepted. Disconnects also retain drafts: the CLI
// still contains those bytes, even after the browser goes away.
func (b *Broker) ConfirmInteractiveSubmission(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if draft := b.interactiveDrafts[sessionID]; draft != nil && draft.submitVersion == draft.version {
		delete(b.interactiveDrafts, sessionID)
	}
}
