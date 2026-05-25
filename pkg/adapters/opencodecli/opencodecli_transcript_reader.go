package opencodecli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// opencodeExport mirrors the JSON shape emitted by `opencode export
// <sessionID>`. It is the canonical post-hoc source of truth for a
// session: conversation messages, aggregate token usage, the model
// that actually served the call, and cache-read counts. We use it as
// a backstop when the in-flight stream-json events don't carry those
// fields (the free-tier endpoint, in particular, often emits only
// step_start + text events with no step_finish for short responses).
//
// The JSON has more keys than we model here. We intentionally only
// surface the fields the host app consumes through GenerationInfo —
// extending this struct as new use cases land keeps the cost of
// future drift low.
type opencodeExport struct {
	Info     opencodeExportInfo      `json:"info"`
	Messages []opencodeExportMessage `json:"messages"`
}

type opencodeExportInfo struct {
	ID    string `json:"id"`
	Model struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
	Tokens struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Cost float64 `json:"cost"`
}

type opencodeExportMessage struct {
	Info  opencodeExportMessageInfo `json:"info"`
	Parts []opencodeExportPart      `json:"parts"`
}

type opencodeExportMessageInfo struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

type opencodeExportPart struct {
	Type string `json:"type"`
	// text parts
	Text string `json:"text,omitempty"`
	// tool parts — opencode uses both "tool" and "tool_use" depending
	// on version + agent path.
	Tool     string                 `json:"tool,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Input    map[string]interface{} `json:"input,omitempty"`
	CallID   string                 `json:"callID,omitempty"`
	// State carries the tool's output once execution finishes.
	State struct {
		Status string `json:"status,omitempty"`
		Output string `json:"output,omitempty"`
	} `json:"state,omitempty"`
}

// runOpencodeExport invokes `opencode export <sessionID>` and parses
// the JSON output. opencode export runs in well under a second on the
// local SQLite store; the call cost is acceptable for adding it on the
// hot path of every turn.
//
// Returns an error if the binary isn't found, the export subprocess
// fails, or the output isn't valid JSON. Callers should treat any
// error as non-fatal — the rest of the turn already succeeded and we
// only call this for sidecar enrichment.
func runOpencodeExport(ctx context.Context, binPath, sessionID string) (*opencodeExport, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("runOpencodeExport: empty sessionID")
	}
	cmd := exec.CommandContext(ctx, binPath, "export", sessionID)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode export %s: %w", sessionID, err)
	}
	// `opencode export` prefixes the JSON with a one-line "Exporting
	// session: <id>" status message. Skip everything before the first
	// '{' to be safe.
	if idx := strings.IndexByte(string(out), '{'); idx > 0 {
		out = out[idx:]
	}
	var exp opencodeExport
	if err := json.Unmarshal(out, &exp); err != nil {
		return nil, fmt.Errorf("decode opencode export: %w", err)
	}
	return &exp, nil
}

// lastTurnMessages returns the last user/assistant pair from the
// export — i.e. the messages produced by THIS turn. opencode appends
// the new (user, assistant) pair to the end of the messages array on
// every `opencode run` invocation, so the tail two messages are
// always the current turn's contribution.
//
// We do NOT return the full history each turn: callers splice these
// messages into their own conversation_history and would otherwise
// see exponential duplication across turns.
func (e *opencodeExport) lastTurnMessages() []llmtypes.MessageContent {
	if e == nil || len(e.Messages) == 0 {
		return nil
	}
	// Find the last user→assistant transition. Usually that's just the
	// final two entries, but if opencode someday emits trailing
	// system/tool-only messages we walk back to the last user marker.
	lastUser := -1
	for i := len(e.Messages) - 1; i >= 0; i-- {
		if e.Messages[i].Info.Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		return nil
	}
	out := make([]llmtypes.MessageContent, 0, len(e.Messages)-lastUser)
	for _, m := range e.Messages[lastUser:] {
		mc := opencodeExportMessageToContent(m)
		if mc.Role != "" {
			out = append(out, mc)
		}
	}
	return out
}

// opencodeExportMessageToContent maps one exported message into the
// llmtypes shape. opencode parts include reasoning, text, step-start,
// step-finish, and tool calls; we surface text + tool calls (the
// information the host app's conversation log cares about) and drop
// reasoning + step markers (internal-only).
func opencodeExportMessageToContent(m opencodeExportMessage) llmtypes.MessageContent {
	role := llmtypes.ChatMessageType("")
	switch strings.ToLower(strings.TrimSpace(m.Info.Role)) {
	case "user":
		role = llmtypes.ChatMessageTypeHuman
	case "assistant":
		role = llmtypes.ChatMessageTypeAI
	case "tool":
		role = llmtypes.ChatMessageTypeTool
	default:
		return llmtypes.MessageContent{}
	}
	parts := make([]llmtypes.ContentPart, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch p.Type {
		case "text":
			if strings.TrimSpace(p.Text) != "" {
				parts = append(parts, llmtypes.TextContent{Text: p.Text})
			}
		case "tool", "tool_use":
			// Surface as a typed ToolCall. The opencode export gives us
			// the structured input (a map) and the state output text;
			// we marshal input back to JSON for the llmtypes shape.
			name := p.Tool
			if name == "" {
				name = p.Name
			}
			if name == "" {
				continue
			}
			argsJSON := "{}"
			if len(p.Input) > 0 {
				if b, err := json.Marshal(p.Input); err == nil {
					argsJSON = string(b)
				}
			}
			parts = append(parts, llmtypes.ToolCall{
				ID:   p.CallID,
				Type: "function",
				FunctionCall: &llmtypes.FunctionCall{
					Name:      name,
					Arguments: argsJSON,
				},
			})
			// If the tool finished and produced output, append a
			// matching ToolCallResponse so the splice carries both
			// sides of the call.
			if strings.TrimSpace(p.State.Output) != "" {
				parts = append(parts, llmtypes.ToolCallResponse{
					ToolCallID: p.CallID,
					Name:       name,
					Content:    p.State.Output,
				})
			}
		}
	}
	if len(parts) == 0 {
		return llmtypes.MessageContent{}
	}
	return llmtypes.MessageContent{Role: role, Parts: parts}
}
