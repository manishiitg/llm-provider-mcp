package codexcli

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// readCodexTranscriptMessages reconstructs the CLI's internal
// turn-by-turn trail (assistant text, function/custom tool calls,
// tool outputs) from the same rollout JSONL that readCodexTranscriptUsage
// reads tokens from. Returns []llmtypes.MessageContent suitable for
// splicing into a workflow conversation_history.
//
// Shape mapping (codex rollout → llmtypes):
//
//	response_item:message  role=assistant   → {Role: AI, Parts: [TextContent...]}
//	response_item:function_call             → {Role: AI, Parts: [ToolCall]}
//	response_item:function_call_output      → {Role: Tool, Parts: [ToolCallResponse]}
//	response_item:custom_tool_call          → {Role: AI, Parts: [ToolCall]}    (apply_patch etc.)
//	response_item:custom_tool_call_output   → {Role: Tool, Parts: [ToolCallResponse]}
//
// Skipped (noise / non-content):
//   - response_item:reasoning      (encrypted, no usable text)
//   - response_item:message role=developer|user (system prompts / outer user msg already in history)
//   - response_item:tool_search_*  (internal tool discovery)
//   - event_msg:*                  (UI/telemetry duplicates of response_items)
//
// File selection mirrors readCodexTranscriptUsage: freshest rollout
// whose mtime is at-or-after turnStart-30s and whose session_meta.cwd
// matches expectedWorkingDir. If expectedWorkingDir is set and no
// rollout matches, returns nil (we never want to leak cross-session
// messages).
//
// Best-effort: returns nil on any error or when no rollout is found.
func readCodexTranscriptMessages(turnStart time.Time, expectedWorkingDir string) []llmtypes.MessageContent {
	root := codexSessionsRoot()
	if root == "" {
		return nil
	}

	cutoff := turnStart.Add(-30 * time.Second)
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		cands = append(cands, cand{path: p, mod: info.ModTime()})
		return nil
	})
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })

	for _, candidate := range cands {
		msgs, cwd := readCodexTranscriptMessagesFile(candidate.path, turnStart)
		if strings.TrimSpace(expectedWorkingDir) != "" && !sameCodexWorkingDir(cwd, expectedWorkingDir) {
			continue
		}
		return msgs
	}
	return nil
}

func readCodexTranscriptMessagesFile(path string, turnStart time.Time) ([]llmtypes.MessageContent, string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ""
	}
	defer f.Close()

	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type rolloutPayload struct {
		// shared
		Type string `json:"type"`
		// session_meta
		CWD string `json:"cwd"`
		// message
		Role    string         `json:"role"`
		Content []contentBlock `json:"content"`
		// function_call / custom_tool_call
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Input     string `json:"input"`
		CallID    string `json:"call_id"`
		// function_call_output / custom_tool_call_output
		Output string `json:"output"`
	}
	type ev struct {
		Type      string         `json:"type"`
		Timestamp string         `json:"timestamp"`
		Payload   rolloutPayload `json:"payload"`
	}

	var sessionCWD string
	var out []llmtypes.MessageContent

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var e ev
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Type == "session_meta" && e.Payload.CWD != "" {
			sessionCWD = e.Payload.CWD
		}
		if !turnStart.IsZero() && e.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
		}
		if e.Type != "response_item" {
			continue
		}

		switch e.Payload.Type {
		case "message":
			if e.Payload.Role != "assistant" {
				// skip user / developer / system rows; the outer
				// conversation already carries the user prompt.
				continue
			}
			var parts []llmtypes.ContentPart
			for _, b := range e.Payload.Content {
				// codex uses "output_text" for assistant content;
				// "input_text" appears on user / developer rows.
				if b.Type != "output_text" && b.Type != "text" {
					continue
				}
				if b.Text == "" {
					continue
				}
				parts = append(parts, llmtypes.TextContent{Text: b.Text})
			}
			if len(parts) == 0 {
				continue
			}
			out = append(out, llmtypes.MessageContent{
				Role:  llmtypes.ChatMessageTypeAI,
				Parts: parts,
			})

		case "function_call":
			args := e.Payload.Arguments
			if args == "" {
				args = "{}"
			}
			out = append(out, llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeAI,
				Parts: []llmtypes.ContentPart{
					llmtypes.ToolCall{
						ID:   e.Payload.CallID,
						Type: "function",
						FunctionCall: &llmtypes.FunctionCall{
							Name:      e.Payload.Name,
							Arguments: args,
						},
					},
				},
			})

		case "custom_tool_call":
			// codex's custom tools (e.g., apply_patch) use a raw
			// `input` string instead of JSON arguments. Wrap it in a
			// single-key JSON object so downstream consumers still
			// see a parseable Arguments field.
			input := e.Payload.Input
			args := input
			if !looksLikeJSON(input) {
				wrapped, err := json.Marshal(map[string]string{"input": input})
				if err == nil {
					args = string(wrapped)
				}
			}
			if args == "" {
				args = "{}"
			}
			out = append(out, llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeAI,
				Parts: []llmtypes.ContentPart{
					llmtypes.ToolCall{
						ID:   e.Payload.CallID,
						Type: "function",
						FunctionCall: &llmtypes.FunctionCall{
							Name:      e.Payload.Name,
							Arguments: args,
						},
					},
				},
			})

		case "function_call_output", "custom_tool_call_output":
			out = append(out, llmtypes.MessageContent{
				Role: llmtypes.ChatMessageTypeTool,
				Parts: []llmtypes.ContentPart{
					llmtypes.ToolCallResponse{
						ToolCallID: e.Payload.CallID,
						Content:    e.Payload.Output,
					},
				},
			})
		}
	}
	return out, sessionCWD
}

func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	c := t[0]
	return c == '{' || c == '['
}
