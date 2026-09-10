package cursorcli

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// A retained input owns the transcript suffix after its newly submitted query.
// Reading it is repeatable: polling must not consume a tool call or final reply.
type cursorRetainedInput struct {
	storeDB  string
	query    string
	baseline map[string]struct{}
}

func (s *cursorInteractiveSession) setRetainedStore(nativeID string) {
	s.retainedMu.Lock()
	defer s.retainedMu.Unlock()
	s.retainedWorkingDir = s.workingDir
	if nativeID != "" && nativeID != s.retainedNativeID {
		s.retainedNativeID = nativeID
		s.retainedStoreDB = ""
	}
	// Initial discovery happens after launch, once Cursor has a transcript.
	if s.retainedNativeID != "" {
		s.resolveRetainedStoreLocked()
	}
}

func (s *cursorInteractiveSession) resolveRetainedStoreLocked() string {
	if s.retainedStoreDB != "" {
		return s.retainedStoreDB
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if s.retainedNativeID != "" {
		s.retainedStoreDB = cursorStoreDBForNativeSession(home, s.retainedWorkingDir, s.retainedNativeID)
	} else {
		// A cold launch may not have flushed its store when GenerateContent
		// returns. Discover once, then pin it just as the normal turn reader does.
		s.retainedStoreDB = freshestCursorStoreDBSince(s.retainedWorkingDir, s.createdAt.Add(-30*time.Second))
	}
	return s.retainedStoreDB
}

func newCursorRetainedInput(storeDB, query string) *cursorRetainedInput {
	input := &cursorRetainedInput{storeDB: storeDB, query: strings.Join(strings.Fields(query), " "), baseline: map[string]struct{}{}}
	if storeDB == "" {
		return input
	}
	db, err := sql.Open("sqlite", "file:"+storeDB+"?mode=ro")
	if err != nil {
		return input
	}
	defer db.Close()
	refs, _ := cursorStoreLatestRootRefs(context.Background(), db)
	for _, ref := range refs {
		input.baseline[ref] = struct{}{}
	}
	return input
}

// ReadRetainedTurnMessages reads the submitted query's own structured trail.
// The native store is pinned when the session starts/completes; nested image
// or helper agents in the same directory must never supply its final answer.
func ReadRetainedTurnMessages(ownerSessionID string, _ time.Time) []llmtypes.MessageContent {
	return readRetainedTurnMessages(ownerSessionID, true)
}

// ReadRetainedTurnProgressMessages consumes newly committed messages using the
// normal stream's blob cursor. Progress belongs to the native session, not just
// its latest query: a steer can be submitted before earlier narration commits.
// Callers must serialize this read with delivery and publication; a discarded
// read would otherwise consume messages without publishing them.
func ReadRetainedTurnProgressMessages(ownerSessionID string) []llmtypes.MessageContent {
	session, ok := cursorPersistentRegistry.Get(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return nil
	}
	session.retainedMu.Lock()
	path := session.resolveRetainedStoreLocked()
	session.retainedMu.Unlock()
	if path == "" {
		return nil
	}
	return readCursorStoreDBMessages(path, cursorTranscriptStreamKey(ownerSessionID))
}

// A restored runtime may accept a retained send before a normal stream has
// started in this process. Prime only once, before delivery; never advance an
// existing cursor, since it may still have unpublished previous-turn messages.
func primeCursorRetainedProgress(ownerSessionID string, input *cursorRetainedInput) {
	key := cursorTranscriptStreamKey(ownerSessionID)
	cursorReturnedBlobsMu.Lock()
	defer cursorReturnedBlobsMu.Unlock()
	if _, exists := cursorReturnedBlobs[key]; exists {
		return
	}
	seen := make(map[string]struct{}, len(input.baseline))
	for ref := range input.baseline {
		seen[ref] = struct{}{}
	}
	cursorReturnedBlobs[key] = seen
}

func readRetainedTurnMessages(ownerSessionID string, requireIdle bool) []llmtypes.MessageContent {
	session, ok := cursorPersistentRegistry.Get(strings.TrimSpace(ownerSessionID))
	if !ok || session == nil {
		return nil
	}
	session.retainedMu.Lock()
	input := session.retainedInput
	if input != nil && input.storeDB == "" {
		copy := *input
		copy.storeDB = session.resolveRetainedStoreLocked()
		input = &copy
	}
	session.retainedMu.Unlock()
	if input == nil {
		return nil
	}
	// Cursor can commit standalone commentary before a later tool-call blob.
	// Its idle composer is required in addition to the query-bound transcript.
	if requireIdle {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pane, err := captureCursorPane(ctx, session.tmuxSessionName)
		if err != nil || !PaneReadyForInput(pane) {
			return nil
		}
	}
	return readCursorRetainedInput(input)
}

func readCursorRetainedInput(input *cursorRetainedInput) []llmtypes.MessageContent {
	if input == nil || input.storeDB == "" || input.query == "" {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+input.storeDB+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	ctx := context.Background()
	refs, err := cursorStoreLatestRootRefs(ctx, db)
	if err != nil {
		return nil
	}
	var out []llmtypes.MessageContent
	matched := false
	for _, ref := range refs {
		data := readCursorBlob(ctx, db, ref)
		var msg cursorMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "user":
			query := cursorUserQueryFromContent(msg.Content)
			if query == "" {
				continue
			}
			_, old := input.baseline[ref]
			matched = !old && strings.Join(strings.Fields(query), " ") == input.query
			out = nil
			if matched {
				out = append(out, llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, query))
			}
		case "assistant":
			if matched {
				if parts := cursorAssistantPartsFromContent(msg.Content); len(parts) > 0 {
					out = append(out, llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeAI, Parts: parts})
				}
			}
		case "tool":
			if matched {
				if parts := cursorToolPartsFromContent(msg.Content); len(parts) > 0 {
					out = append(out, llmtypes.MessageContent{Role: llmtypes.ChatMessageTypeTool, Parts: parts})
				}
			}
		}
	}
	return out
}
