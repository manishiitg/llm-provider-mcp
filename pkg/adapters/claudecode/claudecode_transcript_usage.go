package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// readClaudeTranscriptUsage looks up the Claude Code session transcript
// JSONL written by the local claude CLI and aggregates token usage for
// the current turn.
//
// The CLI writes a JSONL file at
//
//	~/.claude/projects/<encoded-cwd>/<session-id>.jsonl
//
// where <encoded-cwd> is the cwd with `/`, `_`, and `.` replaced by `-`.
// Rather than mirror that encoding (which has drifted over claude
// versions), we glob `~/.claude/projects/*/<session-id>.jsonl`: session
// IDs are UUIDs so the match is unambiguous and the glob is cheap.
//
// Returns nil/empty on any error or if no usage data is available.
// Best-effort by design — never surface IO errors to the caller. The
// model string is the latest model seen on an in-turn assistant event
// (claude-code can swap models mid-session via /model).
func readClaudeTranscriptUsage(sessionID string, turnStart time.Time) (*llmtypes.GenerationInfo, string) {
	if !isClaudeTranscriptSessionID(sessionID) {
		return nil, ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, ""
	}
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return nil, ""
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return nil, ""
	}
	defer f.Close()

	var input, output, cacheCreate, cacheRead int
	var latestModel string
	// Claude CLI writes one JSONL row per assistant content block (text
	// chunks + each tool_use), but every row from a single LLM call shares
	// the same message.id and carries the call's total usage. Sum per
	// unique message.id, not per row, or we multi-count by ~5-10x.
	seenMessageID := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	// Assistant events can carry long tool-use payloads; bump line limit.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			RequestID string `json:"requestId"`
			Message   struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil || ev.Type != "assistant" {
			continue
		}
		if !turnStart.IsZero() && ev.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
		}
		dedupKey := ev.Message.ID
		if dedupKey == "" {
			dedupKey = ev.RequestID
		}
		if dedupKey != "" {
			if _, ok := seenMessageID[dedupKey]; ok {
				continue
			}
			seenMessageID[dedupKey] = struct{}{}
		}
		input += ev.Message.Usage.InputTokens
		output += ev.Message.Usage.OutputTokens
		cacheCreate += ev.Message.Usage.CacheCreationInputTokens
		cacheRead += ev.Message.Usage.CacheReadInputTokens
		if m := ev.Message.Model; m != "" && m != "<synthetic>" {
			latestModel = m
		}
	}
	if input+output+cacheCreate+cacheRead == 0 {
		return nil, latestModel
	}

	prompt := input + cacheCreate
	total := prompt + output + cacheRead
	gi := &llmtypes.GenerationInfo{
		PromptTokens:     intRef(prompt),
		CompletionTokens: intRef(output),
		TotalTokens:      intRef(total),
	}
	if cacheRead > 0 {
		gi.CachedContentTokens = intRef(cacheRead)
	}
	// Mirror cache numbers into Additional under the raw Anthropic
	// key names so the downstream cost ledger pipeline (which keys
	// off `cache_read_input_tokens` / `cache_creation_input_tokens`
	// rather than the typed CachedContentTokens field) can populate
	// CacheReadTokens / CacheWriteTokens per turn. Without this,
	// claude-code chat ledger entries showed cache_read/write=0
	// even though the turn was largely cache-served.
	if cacheRead > 0 || cacheCreate > 0 {
		gi.Additional = map[string]interface{}{}
		if cacheRead > 0 {
			gi.Additional["cache_read_input_tokens"] = cacheRead
		}
		if cacheCreate > 0 {
			gi.Additional["cache_creation_input_tokens"] = cacheCreate
		}
	}
	return gi, latestModel
}

func intRef(v int) *int { return &v }
