package cursorcli

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cursorStoresHoldingUserQuery lists the chat stores of workingDir, written at
// or after since, that hold a user row matching message.
func cursorStoresHoldingUserQuery(workingDir, accountHome, message string, since time.Time) []string {
	home := accountHome
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return nil
		}
	}
	hash := workingDirHashForCursor(workingDir)
	if hash == "" || strings.TrimSpace(message) == "" {
		return nil
	}
	var matches []string
	for _, root := range cursorChatsRoots(home) {
		_ = filepath.WalkDir(filepath.Join(root, hash), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Base(p) != "store.db" {
				return nil
			}
			if cursorStoreDBEffectiveModTime(p, d).Before(since) {
				return nil
			}
			if cursorStoreUserQuerySince(p, message, nil) {
				matches = append(matches, p)
			}
			return nil
		})
	}
	return matches
}

// repinStoreForMessage makes sure the session reads the chat that actually
// received message. Cursor can have more than one chat in the same folder:
// after a relaunch into a fresh native session (e.g. a tool-mode change) the
// first turn's chat was pinned while the pane kept typing into another one, so
// every later message failed its durable ack and its reply never reached the
// user (RTS 2026-09-25). When the pinned chat lacks the message and exactly one
// chat written since the send holds it, that chat is pinned instead. It never
// guesses: zero or several candidates keep the current pin.
func (s *cursorInteractiveSession) repinStoreForMessage(message string, since time.Time) string {
	s.retainedMu.Lock()
	pinned := s.resolveRetainedStoreLocked()
	workingDir := s.retainedWorkingDir
	if workingDir == "" {
		workingDir = s.workingDir
	}
	s.retainedMu.Unlock()
	if pinned != "" && cursorStoreUserQuerySince(pinned, message, nil) {
		return pinned
	}
	candidates := cursorStoresHoldingUserQuery(workingDir, s.accountHome, message, since)
	if len(candidates) != 1 || candidates[0] == pinned {
		return pinned
	}
	found := candidates[0]
	s.retainedMu.Lock()
	defer s.retainedMu.Unlock()
	if s.retainedStoreDB != pinned {
		// Another caller re-pinned meanwhile; keep theirs.
		return s.retainedStoreDB
	}
	s.retainedNativeID = cursorNativeSessionIDFromStoreDBPath(found)
	s.retainedStoreDB = found
	if s.retainedInput != nil && s.retainedInput.storeDB == pinned {
		input := *s.retainedInput
		input.storeDB = found
		// The baseline refs belonged to the old chat; the new chat's rows are
		// all new to this input.
		input.baseline = map[string]struct{}{}
		s.retainedInput = &input
	}
	log.Printf("[cursor-store] owner=%s re-pinned chat %s -> %s: it holds the sent message", s.ownerSessionID, cursorNativeSessionIDFromStoreDBPath(pinned), s.retainedNativeID)
	return found
}
