package musecli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// wireLines loads a captured exec --json transcript (one envelope per line).
func wireLines(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("captured wire %s not present", path)
	}
	var lines []json.RawMessage
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, json.RawMessage(line))
	}
	return lines
}

func wireLinesOfType(lines []json.RawMessage, payloadType string) []json.RawMessage {
	var out []json.RawMessage
	for _, line := range lines {
		var envelope struct {
			PayloadType string `json:"payload_type"`
		}
		if err := json.Unmarshal(line, &envelope); err == nil && envelope.PayloadType == payloadType {
			out = append(out, line)
		}
	}
	return out
}

// TestMuseToolEndChunkFromLiveWire replays the tool.result event captured
// live 2026-09-10 (meta turn running `ls` via the shell tool): the chunk
// must name the tool, keep the call id, and carry command + result.
func TestMuseToolEndChunkFromLiveWire(t *testing.T) {
	lines := wireLinesOfType(wireLines(t, "testdata/live_meta_tool_turn.jsonl"), "tool.result")
	if len(lines) != 1 {
		t.Fatalf("tool.result events = %d, want 1", len(lines))
	}
	chunk := museToolEndChunk(lines[0])
	if chunk == nil {
		t.Fatal("museToolEndChunk = nil for live tool.result")
	}
	if chunk.Type != llmtypes.StreamChunkTypeToolCallEnd {
		t.Fatalf("chunk type = %q, want tool_call_end", chunk.Type)
	}
	if chunk.ToolName != "bash" {
		t.Fatalf("tool name = %q, want bash", chunk.ToolName)
	}
	if !strings.HasPrefix(chunk.ToolCallID, "call_") {
		t.Fatalf("call id = %q, want call_ prefix", chunk.ToolCallID)
	}
	if !strings.Contains(chunk.ToolArgs, "ls -1 /tmp/musetmux-turn") {
		t.Fatalf("tool args = %q, want the ls command", chunk.ToolArgs)
	}
	if !strings.Contains(chunk.ToolResult, "List files in musetmux directory") {
		t.Fatalf("tool result = %q, want description fallback (empty output)", chunk.ToolResult)
	}
}

// TestMuseStatusChunkFromLiveWire replays lifecycle status events: each
// must surface as non-empty reasoning progress, never nil on real shapes,
// nil on garbage.
func TestMuseStatusChunkFromLiveWire(t *testing.T) {
	lines := wireLinesOfType(wireLines(t, "testdata/live_meta_tool_turn.jsonl"), "task.lifecycle.status")
	if len(lines) == 0 {
		t.Fatal("no task.lifecycle.status events in capture")
	}
	for _, line := range lines {
		chunk := museStatusChunk(line)
		if chunk == nil {
			t.Fatal("museStatusChunk = nil for live status event")
		}
		if chunk.Type != llmtypes.StreamChunkTypeReasoning || strings.TrimSpace(chunk.Content) == "" {
			t.Fatalf("status chunk = %+v, want non-empty reasoning", chunk)
		}
	}
	if museStatusChunk(json.RawMessage(`{broken`)) != nil {
		t.Fatal("museStatusChunk(garbage) must be nil, not an error")
	}
	if museToolEndChunk(json.RawMessage(`{"payload_type":"tool.result","payload":{}}`)) != nil {
		t.Fatal("museToolEndChunk(empty payload) must be nil")
	}
}
