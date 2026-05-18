# Coding SDK Structured (JSON Streaming) Contract

This document defines the structured JSON streaming transport contract for
coding SDK providers in `multi-llm-provider-go`.

Covered providers:

- `claude-code` — `claude -p --output-format stream-json`
- `codex-cli` — `codex exec --json`
- `cursor-cli` — `cursor-agent --print --output-format stream-json`
- `gemini-cli` — `gemini --output-format stream-json`
- `opencode-cli` — `opencode run --format json` (structured-only, no tmux)

Every coding agent provider supports both the tmux interactive transport and the
structured JSON transport. OpenCode CLI is the exception: it uses structured JSON
only. The two transports are complementary:

- Tmux: persistent chat, live input, terminal streaming, interrupt, multi-turn.
- Structured: per-turn, native token/cost, clean tool events, no tmux dependency.

Both transports must have full e2e test coverage. Neither is legacy.

## Provider CLI Commands

| Provider | Structured CLI | Key Flags |
|---|---|---|
| Claude Code | `claude -p --output-format stream-json` | `--system-prompt-file`, `--mcp-config`, `--tools ""` |
| Codex CLI | `codex exec --json` | `--model`, `--config` |
| Cursor CLI | `cursor-agent --print --output-format stream-json --stream-partial-output` | `--trust`, `--force`, `--workspace`, `--model`, `--mode` |
| Gemini CLI | `gemini --output-format stream-json` | `--prompt`, `--model` |
| OpenCode CLI | `opencode run --format json --dangerously-skip-permissions` | `--dir`, `--model`, `--session`, `--continue` |

## Event Formats by Provider

### Claude Code (stream-json)

```jsonl
{"type":"message_start","message":{"id":"msg_...","model":"claude-haiku-4-5-20241022",...}}
{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}
{"type":"content_block_stop","index":0}
{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}
{"type":"message_stop"}
{"type":"result","subtype":"success","result":"...","session_id":"...","usage":{"input_tokens":1234,"output_tokens":56}}
```

### Codex CLI (exec --json)

```jsonl
{"type":"message","message":{"role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}
{"type":"completed","response":{"output":[...],"usage":{"input_tokens":100,"output_tokens":50}}}
```

### Cursor CLI (stream-json)

```jsonl
{"type":"system","subtype":"init","session_id":"...","model":"...","permissionMode":"..."}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
{"type":"thinking","subtype":"delta","text":"...","session_id":"..."}
{"type":"thinking","subtype":"completed","session_id":"..."}
{"type":"tool_call","subtype":"started","call_id":"...","tool_call":{...}}
{"type":"tool_call","subtype":"completed","call_id":"...","tool_call":{...}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"delta"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"full final text"}]}}
{"type":"result","subtype":"success","result":"...","session_id":"...","usage":{"inputTokens":3705,"outputTokens":45,"cacheReadTokens":7040}}
```

### Gemini CLI (stream-json)

```jsonl
{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}
{"type":"result","subtype":"success","result":"...","session_id":"...","usage":{...}}
```

### OpenCode CLI (run --format json)

```jsonl
{"type":"step_start","timestamp":...,"sessionID":"ses_...","part":{...}}
{"type":"text","timestamp":...,"sessionID":"ses_...","part":{"type":"text","text":"Hello"}}
{"type":"tool_use","timestamp":...,"sessionID":"ses_...","part":{"type":"tool","tool":"bash","callID":"...","state":{"status":"completed","input":{...},"output":"..."}}}
{"type":"step_finish","timestamp":...,"sessionID":"ses_...","part":{"reason":"stop","tokens":{"total":8134,"input":8113,"output":2,"reasoning":19,"cache":{"write":0,"read":0}},"cost":0}}
```

## Normative Structured Agent Contract

### 1. Launch

The adapter must:

- find the provider CLI binary via PATH or configured location
- pass the structured output flag (`--print --output-format stream-json`,
  `exec --json`, `run --format json`, etc.)
- pass tool approval flags (`--force`, `--trust`,
  `--dangerously-skip-permissions`) since there is no interactive user
- pass `--workspace <dir>` or `--dir <dir>` when a working directory is set
- pass `--model <model>` when a model override is set
- set the process group for clean cancellation
- build the environment with provider-specific API keys
- report clear errors for missing binary, auth, or version

### 2. Instructions and Prompting

The adapter must:

- extract system messages from the message list
- route system instructions through the provider-native mechanism when
  available (Claude Code `--system-prompt-file`, Cursor `.cursor/rules`,
  Gemini `GEMINI_SYSTEM_MD`)
- fall back to prepending system instructions to the prompt when no native
  mechanism exists (OpenCode `[System Instructions]\n...\n\n[User Message]\n...`)
- build user conversation from all non-system messages
- reject empty prompts with a clear error

### 3. Tool Surface

The adapter must:

- pass tool approval flags so all tool calls execute without interactive
  approval
- parse tool-call events from the JSON stream and stream them as
  `StreamChunkTypeToolCallStart` and `StreamChunkTypeToolCallEnd` chunks
- support MCP bridge configuration when the provider supports it
- support bridge-only mode (disable built-in tools) via provider-specific
  flags (`--tools ""`, `--mode ask`, policy files)

### 4. Response Extraction

The adapter must:

- accumulate text/assistant content events into the final response
- for providers with streaming deltas plus a final full-text event (Cursor),
  prefer the final full-text event
- trim whitespace from the accumulated content
- return an error if no text was received and the process exited with error
- map provider-specific stop reasons to `stop` or `tool_calls`

### 5. Token Usage

The adapter must:

- extract token usage from the provider's result/finish events
- normalize to `InputTokens`, `OutputTokens`, `TotalTokens`
- propagate `CacheTokens` when the provider reports cache read/write
- sum across multiple step/result events for multi-step agent runs

### 6. Session Metadata

The adapter must:

- capture the session/thread/request ID from the provider events
- expose it in `GenerationInfo.Additional` with a provider-specific key
- expose the transport mode (`structured`, `stream-json`, `exec-json`, etc.)

### 7. Streaming

The adapter must:

- stream assistant text deltas as `StreamChunkTypeContent` chunks
- stream thinking/reasoning deltas as `StreamChunkTypeReasoning` chunks when
  available
- stream tool calls as `StreamChunkTypeToolCallStart` and
  `StreamChunkTypeToolCallEnd` chunks
- close the stream channel after process exit or error
- use non-blocking sends to avoid deadlock

### 8. Error Handling

The adapter must:

- return clear errors for missing binary
- include stderr output in error messages when the process fails
- return an error if the process exits with non-zero and no text was captured
- handle malformed JSON lines gracefully (skip and log)
- close the stream channel on all error paths

## Required Real E2E Certification Matrix

Every structured/JSON coding provider must have opt-in real E2E tests for each
area below. The test proves the adapter correctly handles the provider's actual
event format end-to-end.

| # | Area | Required proof |
|---|---|---|
| 1 | Fresh launch | Starts the real CLI in structured mode, gets a text response. |
| 2 | Working directory | Provider operates in the exact caller workspace. |
| 3 | System prompt | System instructions reach the provider and a canary appears in the response. |
| 4 | Token usage | Result/finish events produce non-zero input and output token counts. |
| 5 | Streaming text | Text/assistant delta events stream to the channel as content chunks. |
| 6 | Streaming tool calls | Tool call events stream as tool-call-start and tool-call-end chunks. |
| 7 | Session metadata | Session/thread ID captured from events, transport mode in generation info. |
| 8 | Multi-step tool use | Agent uses a tool, gets output, then produces final text. |
| 9 | Model override | Non-default model selector produces a response (adapter passes `--model`). |
| 10 | Image path analysis | Provider reads a local image file path and answers content questions. |
| 11 | Web search | Provider performs web search and returns real internet data. |
| 12 | Live web search | Web search returns data that could not come from model training data. |
| 13 | Cancellation | Context cancellation kills the process group; adapter returns error cleanly. |
| 14 | Error on empty response | Adapter returns a clear error when no text events are received. |
| 15 | MCP bridge tool call | A real MCP bridge tool is callable from the structured transport. |
| 16 | Internal tool disable | Built-in tools disabled via provider flag; agent cannot use shell/file tools. |
| 17 | Multi-turn resume | Second call with resume session ID continues the conversation; agent recalls prior context. |
| 18 | No injected strings | Adapter does not leak internal project names, library names, or custom strings into the prompt. |
| 19 | No internal memory | Fresh session (no resume) cannot recall data from a previous session; agent memory is isolated. |

## Current Test Coverage

### Claude Code (`claude -p --output-format stream-json`)

```sh
RUN_CLAUDE_CODE_PRINT_INTEGRATION=1 go test ./pkg/adapters/claudecode \
  -run 'TestClaudeCodeStructured|TestClaudeCodeStreaming|TestRawClaude' -v -timeout 4m
```

| Area | Test |
|---|---|
| Fresh launch | `TestClaudeCodeStructuredBasicRun` |
| System prompt | `TestClaudeCodeStructuredSystemPrompt` |
| Token usage | `TestClaudeCodeStructuredTokenUsage` |
| Streaming text | `TestClaudeCodeStructuredStreaming` |
| Streaming tool calls | `TestClaudeCodeStructuredToolUse` |
| Session metadata | `TestClaudeCodeStructuredSessionMetadata` |
| Image input | `TestClaudeCodePrintRealImageInput` |
| Web search | `TestClaudeCodeRealSearchWeb` |
| Tool disable | `TestClaudeCodeStructuredToolDisable` |
| Multi-turn resume | `TestClaudeCodeStructuredMultiTurnResume` |
| No injected strings | `TestClaudeCodeStructuredNoInjectedStrings` |
| No internal memory | `TestClaudeCodeStructuredNoInternalMemory` |

**Gaps:** working directory, multi-step tool use, model override, cancellation, MCP bridge.

### Codex CLI (`codex exec --json`)

```sh
RUN_CODEX_CLI_STREAM_JSON_E2E=1 go test ./pkg/adapters/codexcli \
  -run 'TestCodexCLIRealExecJSON' -v -timeout 6m
```

| Area | Test |
|---|---|
| Fresh launch | `TestCodexCLIRealExecJSONContract` |
| Session resume | `TestCodexCLIRealExecJSONContract` (multi-turn with resume) |
| MCP bridge | `TestCodexCLIRealExecJSONMCPBridgeContract` |

**Gaps:** system prompt, token usage, streaming text, tool call events, session
metadata, model override, image path, web search, cancellation.

### Cursor CLI (`cursor-agent --print --output-format stream-json`)

```sh
RUN_CURSOR_CLI_STREAM_JSON_E2E=1 go test ./pkg/adapters/cursorcli \
  -run 'TestCursorCLIStructured' -v -timeout 10m
```

| Area | Test |
|---|---|
| Fresh launch | `TestCursorCLIStructuredBasicRun` |
| System prompt | `TestCursorCLIStructuredSystemPrompt` |
| Token usage | `TestCursorCLIStructuredTokenUsage` |
| Streaming text | `TestCursorCLIStructuredStreaming` |
| Streaming tool calls | `TestCursorCLIStructuredToolUse` |
| Session metadata | `TestCursorCLIStructuredSessionMetadata` |
| Multi-step tool use | `TestCursorCLIStructuredToolUse` |
| Image path | `TestCursorCLIStructuredImagePath` |
| Web search | `TestCursorCLIStructuredSearchWeb` |
| Live web search | `TestCursorCLIStructuredSearchWebLiveData` |
| Tool disable | `TestCursorCLIStructuredToolDisable` |
| Multi-turn resume | `TestCursorCLIStructuredMultiTurnResume` |
| No injected strings | `TestCursorCLIStructuredNoInjectedStrings` |
| No internal memory | `TestCursorCLIStructuredNoInternalMemory` |

**Gaps:** working directory, model override, cancellation, error handling, MCP bridge.

### Gemini CLI (`gemini --output-format stream-json`)

```sh
RUN_GEMINI_CLI_STREAM_JSON_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli \
  -run 'TestGeminiCLIRealStreamJSON|TestGeminiCLIStructured' -v -timeout 10m
```

| Area | Test |
|---|---|
| Fresh launch | `TestGeminiCLIRealStreamJSONContract` |
| System prompt | `TestGeminiCLIStructuredSystemPrompt` |
| Token usage | `TestGeminiCLIUsageAndCost` |
| Streaming text | `TestGeminiCLIStructuredStreaming` |
| Session resume | `TestGeminiCLIRealStreamJSONContract` (multi-turn with resume) |
| Session metadata | `TestGeminiCLIRealStreamJSONContract` (session_id + project_dir_id) |
| MCP bridge | `TestGeminiCLIRealStreamJSONMCPBridgeContract` |
| Tool disable | `TestGeminiCLIStructuredToolDisable` |
| Multi-turn resume | `TestGeminiCLIStructuredMultiTurnResume` |
| No injected strings | `TestGeminiCLIStructuredNoInjectedStrings` |
| No internal memory | `TestGeminiCLIStructuredNoInternalMemory` |

**Gaps:** tool call events, model override, image path, web search, cancellation.

### OpenCode CLI (`opencode run --format json`)

```sh
RUN_OPENCODE_CLI_REAL_E2E=1 go test ./pkg/adapters/opencodecli \
  -run 'TestOpenCodeCLIStructured|TestOpenCodeCLIRealImagePath|TestOpenCodeCLIRealSearchWeb' \
  -v -timeout 10m
```

| Area | Test |
|---|---|
| Fresh launch | `TestOpenCodeCLIStructuredBasicRun` |
| System prompt | `TestOpenCodeCLIStructuredSystemPrompt` |
| Token usage | `TestOpenCodeCLIStructuredTokenUsage` |
| Streaming text | `TestOpenCodeCLIStructuredStreaming` |
| Tool call events | `TestOpenCodeCLIStructuredToolUseProducesToolChunks` |
| Session metadata | `TestOpenCodeCLIStructuredSessionIDInMetadata` |
| Multi-step tool use | `TestOpenCodeCLIStructuredToolUseProducesToolChunks` |
| Image path | `TestOpenCodeCLIRealImagePathAnalysis` |
| Web search | `TestOpenCodeCLIRealSearchWeb` |
| Live web search | `TestOpenCodeCLIRealSearchWebLiveData` |
| Multi-turn resume | `TestOpenCodeCLIStructuredMultiTurnResume` |
| No injected strings | `TestOpenCodeCLIStructuredNoInjectedStrings` |
| No internal memory | `TestOpenCodeCLIStructuredNoInternalMemory` |

**Gaps:** model override, cancellation, MCP bridge, error handling, tool disable (no CLI flag available).

## Full Provider Coverage Matrix

| Area | Claude | Codex | Cursor | Gemini | OpenCode |
|---|:---:|:---:|:---:|:---:|:---:|
| 1. Fresh launch | yes | yes | yes | yes | yes |
| 2. Working directory | - | - | **no** | - | yes |
| 3. System prompt | yes | **no** | yes | yes | yes |
| 4. Token usage | yes | **no** | yes | yes | yes |
| 5. Streaming text | yes | **no** | yes | yes | yes |
| 6. Streaming tool calls | yes | **no** | yes | **no** | yes |
| 7. Session metadata | yes | **no** | yes | yes | yes |
| 8. Multi-step tool use | **no** | **no** | yes | **no** | yes |
| 9. Model override | **no** | **no** | **no** | **no** | **no** |
| 10. Image path | yes | **no** | yes | **no** | yes |
| 11. Web search | yes | **no** | yes | **no** | yes |
| 12. Live web search | **no** | **no** | yes | **no** | yes |
| 13. Cancellation | **no** | **no** | **no** | **no** | **no** |
| 14. Error handling | **no** | **no** | **no** | **no** | **no** |
| 15. MCP bridge | **no** | yes | **no** | yes | **no** |
| 16. Tool disable | yes | **no** | yes | yes | n/a |
| 17. Multi-turn resume | yes | yes | yes | yes | yes |
| 18. No injected strings | yes | **no** | yes | yes | yes |
| 19. No internal memory | yes | **no** | yes | yes | yes |

## Related Docs

- `docs/coding_sdk_tmux_contract.md`
- `docs/CODING_AGENT_TRANSPORT_PATTERNS.md`
