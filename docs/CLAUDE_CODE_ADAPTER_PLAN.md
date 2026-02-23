# Claude Code CLI Adapter

## Overview
LLM provider adapter for [Claude Code CLI](https://github.com/anthropics/claude-code) (`claude`). Uses the local `claude` CLI as an LLM provider, leveraging its agentic capabilities and efficient local context handling.

## Architecture

### Multi-Turn Conversation (Stream-JSON)
The `llmtypes.Model` interface defines `GenerateContent` as a stateless operation. We map this to the CLI using **stateless conversation playback piped via stdin**:

1.  **Extract System Prompt:** Identify system messages and pass via `--system-prompt`.
2.  **Construct JSON History:** Convert conversation history (User/Assistant turns) into stream-json objects.
3.  **Execute via Stdin:** Pipe to `claude -p --input-format stream-json --output-format stream-json --verbose --include-partial-messages`.
4.  **Parse Streamed Output:** Real-time JSON stream parsing with tool event extraction.

### CLI Flags
```
claude -p \
  --output-format stream-json \
  --input-format stream-json \
  --verbose \
  --include-partial-messages \
  --system-prompt "..." \
  --mcp-config '{"mcpServers":{...}}' \
  --dangerously-skip-permissions
```

### Tool Management
Claude Code has a built-in toolset (Bash, Read, Write, Edit, Glob, Grep, WebSearch, etc.). The adapter supports:
*   `WithMCPConfig(json)` — Ephemeral MCP server configuration for a session.
*   `WithDangerouslySkipPermissions()` — Bypass permission checks for autonomous execution.
*   `WithClaudeCodeTools(tools)` — Whitelist specific tools via `--tools` flag.

## Stream Event Parsing

The adapter parses the CLI's real-time JSON stream to extract tool call events for observability.

### Event Flow
```
CLI Output                              → Adapter Action
─────────────────────────────────────────────────────────
"type": "system"                        → Skip (metadata)
"type": "stream_event"
  event.type: "content_block_start"
    content_block.type: "tool_use"      → Emit StreamChunkTypeToolCallStart
                                          Buffer pendingToolCall with startTime
  event.type: "content_block_delta"
    delta.type: "text_delta"            → Emit StreamChunkTypeContent
    delta.type: "input_json_delta"      → Accumulate tool args
  event.type: "content_block_stop"      → Save args to pending buffer (wait for result)
"type": "user"
  content[].type: "tool_result"         → Match by tool_use_id, emit StreamChunkTypeToolCallEnd
                                          with args + result + duration
"type": "assistant"                     → Skip if stream_events present (dedup)
"type": "result"                        → Flush pending tools, parse final response
```

### Pending Tool Buffering
Tool calls are buffered between `content_block_stop` and the `tool_result` message to capture:
*   **ToolArgs** — Complete JSON arguments accumulated from `input_json_delta` chunks
*   **ToolResult** — Execution result from the CLI's internal tool loop
*   **ToolDuration** — Wall-clock time from `content_block_start` to `tool_result`

If the CLI exits before returning a result, pending tools are flushed at `"type": "result"` without result content.

### Playback Deduplication
*   Counts historical AI messages in conversation to skip them during playback.
*   Skips consolidated `assistant` messages when `stream_event`s provide real-time deltas.

## StreamChunk Types

```go
const (
    StreamChunkTypeContent       StreamChunkType = "content"
    StreamChunkTypeToolCall      StreamChunkType = "tool_call"
    StreamChunkTypeToolCallStart StreamChunkType = "tool_call_start"
    StreamChunkTypeToolCallEnd   StreamChunkType = "tool_call_end"
)

type StreamChunk struct {
    Type         StreamChunkType
    Content      string          // Text content
    ToolCall     *ToolCall       // Complete tool call
    ToolName     string          // Tool name (start/end)
    ToolCallID   string          // Tool call ID (start/end)
    ToolArgs     string          // JSON arguments (end)
    ToolResult   string          // Execution result (end)
    ToolDuration time.Duration   // Duration from start to result (end)
}
```

## Data Mapping

| CLI JSON Field | Go Struct Field | Description |
| :--- | :--- | :--- |
| `result` | `Choices[0].Content` | Generated text content |
| `usage.input_tokens` | `Usage.InputTokens` | Prompt tokens |
| `usage.output_tokens` | `Usage.OutputTokens` | Response tokens |
| `usage.cache_read_input_tokens` | `Usage.CacheTokens` | Cache read tokens |
| `total_cost_usd` | `GenerationInfo.Additional["cost_usd"]` | Estimated cost |
| `permission_denials` | `GenerationInfo.Additional["permission_denials"]` | Denied tool calls |

## Known Limitations

### HTTP/SSE MCP Servers
The CLI cannot dynamically bootstrap remote HTTP/SSE MCP servers via `--mcp-config` in headless mode (`-p`). Use `stdio` based local servers only.

### Nested Sessions
CLI cannot be launched inside another Claude Code session. Unset the `CLAUDECODE` environment variable for testing.

### Tool Result Availability
Tool results are captured from the CLI's internal `tool_result` messages. If interrupted before returning a result, the `ToolCallEnd` chunk will have empty `ToolResult`.

## Testing

```bash
# Integration test (requires claude CLI installed)
go test -v ./pkg/adapters/claudecode -run TestClaudeCodeStreaming -timeout 60s
```

## Files
*   `pkg/adapters/claudecode/claudecode_adapter.go` — Main adapter implementation
*   `pkg/adapters/claudecode/claudecode_stream_integration_test.go` — Integration test
*   `llmtypes/types.go` — StreamChunk types
