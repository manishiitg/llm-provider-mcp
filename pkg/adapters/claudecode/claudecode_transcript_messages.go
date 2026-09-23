package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// readClaudeTranscriptMessages reconstructs the assistant's internal
// tool-use loop from the same sidecar JSONL that readClaudeTranscriptUsage
// reads tokens from. It returns a chronologically ordered
// []llmtypes.MessageContent representing what happened INSIDE the
// claude-code CLI during the turn — text chunks, tool_use calls, and
// the tool_result responses fed back to the model — so callers can
// splice that trail into a workflow conversation log alongside the
// outer user-prompt → final-assistant-text shape that the LLM
// abstraction already exposes.
//
// Shape mapping (claude-code JSONL → llmtypes):
//
//	{type:"assistant", message.content: [{type:"text", text}]}
//	  → {Role: AI, Parts: [TextContent]}
//	{type:"assistant", message.content: [{type:"tool_use", id, name, input}]}
//	  → {Role: AI, Parts: [ToolCall{ID, FunctionCall:{Name, Arguments=json(input)}}]}
//	{type:"user",     message.content: [{type:"tool_result", tool_use_id, content}]}
//	  → {Role: Tool,Parts: [ToolCallResponse{ToolCallID, Content}]}
//
// Claude writes one JSONL row per content BLOCK (text chunk or
// tool_use). Rows from the same LLM call share a message.id. We GROUP
// by message.id so that a single assistant LLM call producing
// "text + tool_use + tool_use" returns as ONE MessageContent with
// three Parts — matching the shape the Anthropic Messages API itself
// would have returned.
//
// Returns nil/empty on any error or if the transcript is missing.
// Best-effort by design — never surfaces IO errors to the caller.
func readClaudeTranscriptMessages(sessionID, workingDir string, turnStart time.Time, accountHome ...string) []llmtypes.MessageContent {
	if !isClaudeTranscriptSessionID(sessionID) {
		return nil
	}
	f, _, err := openClaudeTranscript(sessionID, workingDir, accountHome...)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	type ev struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	type assistantMessage struct {
		ID      string                        `json:"id"`
		Content []claudeAssistantContentBlock `json:"content"`
	}
	type userContentBlock struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	}
	type userMessage struct {
		Content json.RawMessage `json:"content"`
	}

	// Claude-code writes ONE row per content block of an LLM call.
	// All rows from the same call share message.id; each row carries
	// exactly ONE content block (text, thinking, tool_use, ...). To
	// reconstruct the call as a single AI MessageContent we group by
	// message.id, accumulate blocks across rows, and emit a combined
	// MessageContent at the first sighting (preserving chronological
	// position in `out`).
	var out []llmtypes.MessageContent
	type pendingGroup struct {
		index int // position in out where this group's MessageContent lives
	}
	groups := make(map[string]*pendingGroup)

	for scanner.Scan() {
		var e ev
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if !turnStart.IsZero() && e.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
		}

		switch e.Type {
		case "assistant":
			var am assistantMessage
			if err := json.Unmarshal(e.Message, &am); err != nil {
				continue
			}
			parts := assistantBlocksToParts(am.Content)
			if len(parts) == 0 {
				// thinking blocks, redacted_reasoning, etc. — skip
				// without registering the group; if a later row of
				// the same message.id carries usable content, it
				// still creates the group then.
				continue
			}
			if am.ID == "" {
				// No id to group by: emit standalone.
				out = append(out, llmtypes.MessageContent{
					Role:  llmtypes.ChatMessageTypeAI,
					Parts: parts,
				})
				continue
			}
			if g, ok := groups[am.ID]; ok {
				// Append additional blocks from later rows of the
				// same LLM call to the already-emitted group.
				combined := append(out[g.index].Parts, parts...) //nolint:gocritic
				out[g.index].Parts = combined
				continue
			}
			groups[am.ID] = &pendingGroup{index: len(out)}
			out = append(out, llmtypes.MessageContent{
				Role:  llmtypes.ChatMessageTypeAI,
				Parts: parts,
			})

		case "user":
			var um userMessage
			if err := json.Unmarshal(e.Message, &um); err != nil {
				continue
			}
			// content is either a string (typed user text) or an array
			// of content blocks (tool_results when the CLI feeds tool
			// outputs back into the model). We only care about
			// tool_results here — typed user text is already in the
			// outer conversation_history.
			var blocks []userContentBlock
			if err := json.Unmarshal(um.Content, &blocks); err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				out = append(out, llmtypes.MessageContent{
					Role: llmtypes.ChatMessageTypeTool,
					Parts: []llmtypes.ContentPart{
						llmtypes.ToolCallResponse{
							ToolCallID: b.ToolUseID,
							Content:    flattenToolResultContent(b.Content),
						},
					},
				})
			}
		}
	}
	return out
}

// claudeCompletedTranscriptResponse describes whether Claude's session JSONL
// exists and whether the current turn contains a committed end_turn message.
// Found and Completed are intentionally separate: an existing but incomplete
// transcript means the turn was interrupted and must never fall back to a pane
// scrape, while a genuinely unavailable transcript retains legacy compatibility.
type claudeCompletedTranscriptResponse struct {
	// Text is the committed end_turn message: the turn's answer, and what
	// callers treat as the final result.
	Text string
	// TurnText is every assistant text block of the turn in transcript order,
	// narration before tool calls included, ending with Text. Chat surfaces
	// show this; Claude Code's own UI does the same. Empty until Completed.
	TurnText  string
	Found     bool
	Completed bool
	// LastIsEndTurn is stricter than Completed: the current turn's latest
	// assistant or user record is an end_turn message with text. Completed
	// stays true once any end_turn appears, even if Claude then continued
	// (a queued message or a background result delivered as a new user row),
	// so only LastIsEndTurn may end a turn.
	LastIsEndTurn bool
	// PendingBackgroundAgents is Claude's own count of background agents
	// still running when the latest turn ended (system turn_duration row's
	// pendingBackgroundAgentCount). While it is non-zero the answer is interim:
	// each agent's report arrives later as a new user row and Claude continues.
	PendingBackgroundAgents int
}

// completedAssistantResponseFromTranscript returns the final assistant message
// that Claude Code explicitly committed with stop_reason=end_turn during the
// current turn. It deliberately skips narration attached to tool_use calls:
// those blocks are progress within the turn, not the final response callers
// should persist.
//
// Claude writes one JSONL row per content block and may repeat a message ID, so
// text is accumulated by message ID and the last completed message wins.
func completedAssistantResponseFromTranscript(sessionID, workingDir string, turnStart time.Time, accountHome ...string) claudeCompletedTranscriptResponse {
	if !isClaudeTranscriptSessionID(sessionID) {
		return claudeCompletedTranscriptResponse{}
	}
	f, _, err := openClaudeTranscript(sessionID, workingDir, accountHome...)
	if err != nil {
		return claudeCompletedTranscriptResponse{}
	}
	defer f.Close()
	result := claudeCompletedTranscriptResponse{Found: true}

	type assistantMessage struct {
		ID         string                        `json:"id"`
		StopReason string                        `json:"stop_reason"`
		Content    []claudeAssistantContentBlock `json:"content"`
	}
	type event struct {
		Type                        string          `json:"type"`
		Subtype                     string          `json:"subtype"`
		Timestamp                   string          `json:"timestamp"`
		Message                     json.RawMessage `json:"message"`
		PendingBackgroundAgentCount int             `json:"pendingBackgroundAgentCount"`
	}
	type group struct {
		text      []string
		completed bool
	}

	groups := make(map[string]*group)
	order := make([]string, 0)
	standalone := 0
	lastAssistantID := ""
	userAfterLastAssistant := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var e event
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		if e.Type == "system" && e.Subtype == "turn_duration" {
			result.PendingBackgroundAgents = e.PendingBackgroundAgentCount
			continue
		}
		if e.Type != "assistant" && e.Type != "user" {
			continue
		}
		if !turnStart.IsZero() && e.Timestamp != "" {
			if ts, parseErr := time.Parse(time.RFC3339Nano, e.Timestamp); parseErr == nil && ts.Before(turnStart) {
				continue
			}
		}
		if e.Type == "user" {
			// A tool result or a newly delivered message: Claude is working again.
			if lastAssistantID != "" {
				userAfterLastAssistant = true
			}
			continue
		}
		var am assistantMessage
		if json.Unmarshal(e.Message, &am) != nil {
			continue
		}
		id := am.ID
		if id == "" {
			standalone++
			id = "__standalone_" + strconv.Itoa(standalone)
		}
		g, ok := groups[id]
		if !ok {
			g = &group{}
			groups[id] = g
			order = append(order, id)
		}
		g.text = append(g.text, textFromClaudeBlocks(am.Content)...)
		if am.StopReason == "end_turn" {
			g.completed = true
			result.Completed = true
		}
		lastAssistantID = id
		userAfterLastAssistant = false
		// A new assistant record belongs to a turn whose turn_duration row
		// (if any) has not been written yet.
		result.PendingBackgroundAgents = 0
	}
	if last := groups[lastAssistantID]; last != nil && last.completed && !userAfterLastAssistant && len(last.text) > 0 && result.PendingBackgroundAgents == 0 {
		result.LastIsEndTurn = true
	}

	for i := len(order) - 1; i >= 0; i-- {
		g := groups[order[i]]
		if g.completed && len(g.text) > 0 {
			result.Text = strings.TrimSpace(strings.Join(g.text, "\n\n"))
			parts := make([]string, 0, len(order))
			for _, id := range order[:i+1] {
				if t := strings.TrimSpace(strings.Join(groups[id].text, "\n\n")); t != "" {
					parts = append(parts, t)
				}
			}
			result.TurnText = strings.Join(parts, "\n\n")
			return result
		}
	}
	return result
}

// claudeTranscriptTurnEndQuiet is how long the transcript must stay unchanged
// after the current turn's final end_turn before that turn counts as finished.
// It absorbs a continuation Claude starts right after end_turn (a queued
// message or background result arrives as a new record and resets the wait).
const claudeTranscriptTurnEndQuiet = 1500 * time.Millisecond

// waitForClaudeTurnEndInTranscript is the structured done_detection gate
// (PLAT-354): it returns once the current turn's latest record is a committed
// end_turn answer and the transcript has been quiet for quiet. The pane plays
// no part. ok is false when ctx ends first or no transcript exists for the
// session; callers then keep the pane-based completion as a fallback.
func waitForClaudeTurnEndInTranscript(ctx context.Context, sessionID, workingDir string, turnStart time.Time, quiet time.Duration, accountHome ...string) (claudeCompletedTranscriptResponse, bool) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastSize int64 = -1
	var lastModTime time.Time
	var endSeenAt time.Time
	for {
		select {
		case <-ctx.Done():
			return claudeCompletedTranscriptResponse{}, false
		case <-ticker.C:
		}
		transcriptPath, _ := resolveClaudeTranscriptPath(sessionID, workingDir, true, accountHome...)
		if transcriptPath == "" {
			continue
		}
		info, err := os.Stat(transcriptPath)
		if err != nil {
			continue
		}
		if info.Size() != lastSize || !info.ModTime().Equal(lastModTime) {
			lastSize, lastModTime = info.Size(), info.ModTime()
			response := completedAssistantResponseFromTranscript(sessionID, workingDir, turnStart, accountHome...)
			if response.LastIsEndTurn && strings.TrimSpace(response.Text) != "" {
				endSeenAt = time.Now()
			} else {
				endSeenAt = time.Time{}
			}
			continue
		}
		if !endSeenAt.IsZero() && time.Since(endSeenAt) >= quiet {
			response := completedAssistantResponseFromTranscript(sessionID, workingDir, turnStart, accountHome...)
			if response.LastIsEndTurn && strings.TrimSpace(response.Text) != "" {
				return response, true
			}
			endSeenAt = time.Time{}
		}
	}
}

// waitForCompletedAssistantResponseFromTranscript waits for Claude Code's
// authoritative end_turn record after its TUI appears to have returned to the
// prompt. Pane readiness is only a display/transport hint: Claude may render
// assistant narration and briefly expose a prompt while it is still processing
// a tool result and starting the next model round. A fixed post-pane grace
// period can therefore terminate a healthy tool loop before its final response
// is committed. The owning turn context is the only timeout/cancellation
// authority here.
func waitForCompletedAssistantResponseFromTranscript(ctx context.Context, sessionID, workingDir string, turnStart time.Time, accountHome ...string) claudeCompletedTranscriptResponse {
	response := completedAssistantResponseFromTranscript(sessionID, workingDir, turnStart, accountHome...)
	if !response.Found || (response.Completed && strings.TrimSpace(response.Text) != "") {
		return response
	}

	// Transcripts can grow to several megabytes. Once one has been found, avoid
	// rescanning the entire JSONL on every poll while Claude is still thinking.
	// An append changes size and mtime; transient stat/read failures retain the
	// already-observed Found state rather than falling back to pane text.
	transcriptPath, _ := resolveClaudeTranscriptPath(sessionID, workingDir, true, accountHome...)
	var lastSize int64 = -1
	var lastModTime time.Time
	if info, err := os.Stat(transcriptPath); err == nil {
		lastSize = info.Size()
		lastModTime = info.ModTime()
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return response
		case <-ticker.C:
			if transcriptPath != "" {
				info, err := os.Stat(transcriptPath)
				if err != nil || (info.Size() == lastSize && info.ModTime().Equal(lastModTime)) {
					continue
				}
				lastSize = info.Size()
				lastModTime = info.ModTime()
			}
			candidate := completedAssistantResponseFromTranscript(sessionID, workingDir, turnStart, accountHome...)
			if !candidate.Found {
				continue
			}
			response = candidate
			if response.Completed && strings.TrimSpace(response.Text) != "" {
				return response
			}
		}
	}
}

func textFromClaudeBlocks(blocks []claudeAssistantContentBlock) []string {
	var text []string
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			text = append(text, strings.TrimSpace(block.Text))
		}
	}
	return text
}

// claudeAssistantContentBlock is one block inside an assistant
// message in claude-code's JSONL transcript. Hoisted to package scope
// so the helper below can take it as a typed parameter.
type claudeAssistantContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

// assistantBlocksToParts maps claude content blocks into llmtypes parts.
func assistantBlocksToParts(blocks []claudeAssistantContentBlock) []llmtypes.ContentPart {
	var parts []llmtypes.ContentPart
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" {
				continue
			}
			parts = append(parts, llmtypes.TextContent{Text: b.Text})
		case "tool_use":
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			parts = append(parts, llmtypes.ToolCall{
				ID:   b.ID,
				Type: "function",
				FunctionCall: &llmtypes.FunctionCall{
					Name:      b.Name,
					Arguments: args,
				},
			})
		}
	}
	return parts
}

// flattenToolResultContent collapses a tool_result's `content` field
// (either a raw string or an array of {type:"text", text}) into a
// single string suitable for llmtypes.ToolCallResponse.Content.
func flattenToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Then array of {type, text}.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		joined := ""
		for i, b := range blocks {
			if b.Type != "text" {
				continue
			}
			if i > 0 && joined != "" {
				joined += "\n"
			}
			joined += b.Text
		}
		return joined
	}
	// Last resort: return raw JSON so nothing is silently dropped.
	return string(raw)
}
