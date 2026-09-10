package musecli

import (
	"encoding/json"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Stream mapping for immediate assessment (thinking/tool visibility),
// decoded from live wire payloads observed 2026-09-10 against the real
// CLI: `tool.result` carries kind=tool_result with call_id, text (a JSON
// document with command/description/exit_code/output), and
// correlation_facts{tool_name, outcome}. `task.lifecycle.status` carries
// event.message progress text ("opening meta model stream attempt 1/10").

// museToolResultPayload decodes a tool.result wire payload.
type museToolResultPayload struct {
	CallID string `json:"call_id"`
	Text   string `json:"text"`
	Facts  struct {
		ToolName string `json:"tool_name"`
		Outcome  string `json:"outcome"`
	} `json:"correlation_facts"`
}

// museToolChunkText decodes the inner tool document: command run,
// description, exit code, and captured output.
type museToolChunkText struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	ExitCode    int    `json:"exit_code"`
	Output      string `json:"output"`
}

// museToolEndChunk maps one tool.result event to a tool_call_end chunk.
// Nil when the event carries nothing mappable (never fail a turn on
// telemetry shape drift).
func museToolEndChunk(line json.RawMessage) *llmtypes.StreamChunk {
	var envelope struct {
		Payload museToolResultPayload `json:"payload"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil
	}
	payload := envelope.Payload
	if strings.TrimSpace(payload.Text) == "" && strings.TrimSpace(payload.Facts.ToolName) == "" {
		return nil
	}
	var doc museToolChunkText
	args, result := "", strings.TrimSpace(payload.Text)
	if err := json.Unmarshal([]byte(payload.Text), &doc); err == nil {
		if strings.TrimSpace(doc.Command) != "" {
			args = doc.Command
		}
		if strings.TrimSpace(doc.Output) != "" {
			result = strings.TrimSpace(doc.Output)
		}
		if result == "" && strings.TrimSpace(doc.Description) != "" {
			result = strings.TrimSpace(doc.Description)
		}
	}
	return &llmtypes.StreamChunk{
		Type:       llmtypes.StreamChunkTypeToolCallEnd,
		ToolName:   payload.Facts.ToolName,
		ToolCallID: payload.CallID,
		ToolArgs:   args,
		ToolResult: result,
		Metadata:   map[string]interface{}{"outcome": payload.Facts.Outcome},
	}
}

// museTaskStatusPayload decodes a task.lifecycle.status wire payload.
type museTaskStatusPayload struct {
	Event struct {
		Message string `json:"message"`
	} `json:"event"`
}

// museStatusChunk maps one lifecycle status event to a reasoning chunk so
// product UIs can render it as a progress card (immediate assessment).
// Transport chatter is kept: attempt counts are exactly what an operator
// watches during a slow turn. Nil on shape drift.
func museStatusChunk(line json.RawMessage) *llmtypes.StreamChunk {
	var envelope struct {
		Payload museTaskStatusPayload `json:"payload"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil
	}
	payload := envelope.Payload
	if strings.TrimSpace(payload.Event.Message) == "" {
		return nil
	}
	return &llmtypes.StreamChunk{
		Type:    llmtypes.StreamChunkTypeReasoning,
		Content: strings.TrimSpace(payload.Event.Message),
	}
}
