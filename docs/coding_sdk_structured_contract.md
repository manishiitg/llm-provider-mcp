# Coding SDK Structured (JSON) Contract

This document defines the structured JSON transport contract for coding SDK
providers in `multi-llm-provider-go`.

Covered providers:

- `opencode-cli` (default transport)
- `kimi` / `kimi-code` (structured-only)

The structured transport uses `<cli> run --format json` or equivalent to get
NDJSON event streams instead of parsing a TUI through tmux. This is simpler,
more reliable, and provides native token/cost tracking, but trades off
persistent chat, live input, terminal streaming, and interrupt for clean
per-turn semantics.

The mechanical source of truth is `coding_agent_contract.go`. Providers using
`CodingAgentTransportStructured` must satisfy this contract.

## Comparison with Tmux Transport

| Capability | Tmux | Structured |
|---|:---:|:---:|
| Persistent multi-turn chat | Yes | No (per-turn) |
| Live follow-up input | Yes | No |
| Terminal snapshot streaming | Yes | No |
| Interrupt/cancel mid-turn | Kill process group | Kill process group |
| Token/cost tracking | Estimated | Native from events |
| Tool call visibility | Inferred from TUI | Native events |
| Done detection | TUI state machine | Process exit |
| Final text extraction | TUI parsing | Text events |
| MCP bridge support | Yes | Yes |
| System prompt | Native files | Prepended to prompt |
| Working directory | `--dir` or cwd | `--dir` or cwd |
| Requires tmux | Yes | No |

## Transport Mechanics

### Event Stream

The structured transport reads NDJSON (one JSON object per line) from stdout.
Each event has:

```json
{
  "type": "step_start|text|tool_use|step_finish",
  "timestamp": 1779079046322,
  "sessionID": "ses_...",
  "part": { ... }
}
```

Event types:

- `step_start`: A new inference step begins. Contains session ID and snapshot.
- `text`: Assistant text output. Accumulated into the final response and
  streamed as content chunks.
- `tool_use`: A tool call with status, input, and output. Streamed as
  tool-call-start and tool-call-end chunks.
- `step_finish`: Step completed. Contains reason (`stop` or `tool-calls`),
  token counts, and cost. Multiple step_finish events may occur when the
  agent loops through tool calls.

### Process Lifecycle

1. Spawn the CLI subprocess with `--format json` and `--dangerously-skip-permissions`.
2. Read NDJSON events line by line from stdout.
3. Accumulate text events into the final response.
4. Stream text and tool events through the stream channel.
5. Sum token usage across all `step_finish` events.
6. Wait for process exit.
7. Return accumulated text, usage, session ID, and metadata.

### Cancellation

Context cancellation kills the process group (`syscall.Kill(-pgid, SIGKILL)`).
There is no graceful interrupt — the process is terminated immediately.

## Normative Structured Agent Contract

### 1. Launch

The adapter must:

- find the provider CLI binary via PATH or configured location
- always pass `--dangerously-skip-permissions` since there is no interactive
  user to approve tool calls
- pass `--format json` to get NDJSON events
- pass `--dir <workspace>` when a working directory is configured
- pass `--model <provider/model>` when a model override is set
- pass `--session <id>` or `--continue` for session resume
- set the process group for clean cancellation
- build the environment with provider-specific API keys

### 2. Instructions and Prompting

The adapter must:

- extract system messages from the message list
- prepend system instructions as `[System Instructions]\n...\n\n[User Message]\n...`
- build user conversation from all non-system messages
- reject empty prompts

### 3. Tool Surface

The adapter must:

- pass `--dangerously-skip-permissions` to allow all tool calls without
  interactive approval
- parse `tool_use` events and stream them as tool-call chunks
- support MCP bridge configuration when the provider supports it

### 4. Response Extraction

The adapter must:

- accumulate all `text` events into the final content
- trim whitespace from the accumulated content
- return an error if no text events were received
- map `step_finish` reason `tool-calls` to `tool_calls` stop reason
- default stop reason to `stop`

### 5. Token Usage

The adapter must:

- sum `InputTokens`, `OutputTokens`, and `TotalTokens` across all
  `step_finish` events (multi-step tool-calling agents emit multiple)
- propagate `CacheTokens` when the provider reports cache hits
- return the accumulated usage in the response

### 6. Session Metadata

The adapter must:

- capture the `sessionID` from the first event that carries one
- expose it in `GenerationInfo.Additional["opencode_session_id"]`
- expose `opencode_mode: "structured"` to distinguish from tmux

### 7. Streaming

The adapter must:

- stream `text` events as `StreamChunkTypeContent` chunks
- stream `tool_use` events as `StreamChunkTypeToolCallStart` and
  `StreamChunkTypeToolCallEnd` chunks
- close the stream channel after process exit
- use non-blocking sends to avoid deadlock if the consumer is slow

### 8. Error Handling

The adapter must:

- return clear errors for missing binary
- include stderr output in error messages when the process fails
- return an error if the process exits with non-zero and no text was captured
- handle malformed JSON lines gracefully (skip and log)
- close the stream channel on all error paths

## Provider Transports

OpenCode CLI:

- Default: `opencode run --format json --dangerously-skip-permissions`
- Session resume: `--session <id>` or `--continue`
- Model override: `--model <provider/model>`
- Working directory: `--dir <workspace>`
- Requires git-initialized workspace
- Legacy tmux: `opencode <project>` inside tmux, available as fallback for
  persistent interactive sessions

Kimi Code:

- Structured-only transport (no tmux support)
- Uses Kimi API with structured output format

## Testing Contract

### Required Real E2E Certification Matrix

Every structured coding provider must have opt-in real E2E tests for:

| Area | Required proof |
| --- | --- |
| Fresh launch | Starts the real CLI, gets a response, reports clear errors for missing binary. |
| Working directory | Provider runs in the exact caller workspace directory. |
| System prompt | System instructions reach the provider and influence the response. |
| Token usage | `step_finish` events produce non-zero input and output token counts. |
| Streaming | Text events stream as content chunks to the stream channel. |
| Tool use events | Tool calls produce tool-call-start and tool-call-end stream chunks. |
| Session metadata | `sessionID` is captured from events and exposed in generation metadata. |
| Multi-step tool use | Agent that uses tools produces text after tool completion. |
| Image path analysis | Provider reads a local image file path and answers content questions. |
| Web search | Provider performs web search and returns real internet data. |

Current real contract commands:

```sh
RUN_OPENCODE_CLI_REAL_E2E=1 go test ./pkg/adapters/opencodecli \
  -run 'TestOpenCodeCLIStructured' -v -timeout 10m

RUN_OPENCODE_CLI_REAL_E2E=1 go test ./pkg/adapters/opencodecli \
  -run 'TestOpenCodeCLIRealImagePathAnalysis|TestOpenCodeCLIRealSearchWeb' -v -timeout 6m
```

### Test Coverage Status

| Area | Status |
| --- | --- |
| Fresh launch | `TestOpenCodeCLIStructuredBasicRun` |
| Working directory | Covered by all tests via `WithWorkingDir()` |
| System prompt | `TestOpenCodeCLIStructuredSystemPrompt` |
| Token usage | `TestOpenCodeCLIStructuredTokenUsage` |
| Streaming | `TestOpenCodeCLIStructuredStreaming` |
| Tool use events | `TestOpenCodeCLIStructuredToolUseProducesToolChunks` |
| Session metadata | `TestOpenCodeCLIStructuredSessionIDInMetadata` |
| Multi-step tool use | `TestOpenCodeCLIStructuredToolUseProducesToolChunks` |
| Image path analysis | `TestOpenCodeCLIRealImagePathAnalysis` |
| Web search | `TestOpenCodeCLIRealSearchWeb`, `TestOpenCodeCLIRealSearchWebLiveData` |

## Related Docs

- `docs/coding_sdk_tmux_contract.md`
- `docs/CODING_AGENT_TRANSPORT_PATTERNS.md`
