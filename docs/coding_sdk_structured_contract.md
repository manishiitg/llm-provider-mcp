# Coding SDK Structured (JSON Streaming) Contract

This document defines the structured JSON streaming transport contract for
coding SDK providers in `multi-llm-provider-go`.

Covered providers:

- `claude-code` — `claude -p --output-format stream-json`
- `codex-cli` — `codex exec --json`
- `cursor-cli` — `cursor-agent --print --output-format stream-json`
- `gemini-cli` — `gemini --output-format stream-json`
- `opencode-cli` — `opencode run --format json` (structured-only, no tmux)

The provider-level coding-agent contract in `coding_agent_contract.go` decides
which transport host applications should use by default. Claude Code, Codex CLI,
and Cursor CLI use the tmux interactive transport. Gemini CLI and OpenCode CLI
use structured JSON only. The two transports are complementary:

- Tmux: persistent chat, live input, terminal streaming, interrupt, multi-turn.
- Structured: per-turn, native token/cost, clean tool events, no tmux dependency.

Declared provider capabilities must have matching test coverage. Neither
transport is legacy, but not every CLI provider exposes both transports through
the provider-level contract.

## Provider CLI Commands

| Provider | Structured CLI | Key Flags |
|---|---|---|
| Claude Code | `claude -p --output-format stream-json` | `--system-prompt-file`, `--mcp-config`, `--tools ""` |
| Codex CLI | `codex exec --json` | `--model`, `--config` |
| Cursor CLI | `cursor-agent --print --output-format stream-json --stream-partial-output` | `--trust`, `--force`, `--workspace`, `--model`, `--mode` |
| Gemini CLI | `gemini --output-format stream-json` | `--prompt`, `--model` |
| OpenCode CLI | `opencode run --format json` | `--dangerously-skip-permissions` (default on), `--dir`, `--model`, `--session`, `--continue` |

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
- **also surface cache totals in `gi.Additional` under the raw
  Anthropic-style keys `cache_read_input_tokens` and (Anthropic
  only) `cache_creation_input_tokens`** — the cost-ledger
  pipeline reads these from Additional, not from the typed
  `CachedContentTokens` field. See
  [`docs/COSTS_AND_CONVERSATION_HISTORY.md`](COSTS_AND_CONVERSATION_HISTORY.md)
  → "Cache token surfacing contract".

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

### 9. Process Shutdown Contract

Every structured CLI subprocess MUST be torn down by the adapter using
this exact sequence once a terminal event (`result` / `task_complete` /
`done` / equivalent) is observed on stdout:

```
SIGTERM  →  10s  →  SIGTERM  →  10s  →  SIGTERM  →  5s  →  SIGKILL
```

Up to **25s** total grace, three SIGTERMs, then unconditional SIGKILL.

Adapters MUST use the shared helper
`pkg/adapters/internal/procshutdown.Graceful(cmd, terminated, logger)`
rather than open-coding the sequence. The helper owns the timing policy
— call sites pass no duration. The helper:

1. Sends `SIGTERM #1` to the process group (`-pid`, requires `Setpgid: true`).
2. Waits up to `FirstGrace` (10s) for `terminated` to close.
3. If still alive: sends `SIGTERM #2`, waits up to `SecondGrace` (10s).
4. If still alive: sends `SIGTERM #3`, waits up to `ThirdGrace` (5s).
5. If still alive: sends `SIGKILL` to the process group.
6. Returns once the kill has been issued. The adapter's main goroutine
   remains responsible for `cmd.Wait()` and reaping.

The helper is launched as a goroutine from the decode loop so the scanner
keeps draining stdout while shutdown is in progress.

**Why three SIGTERMs.** Most well-behaved CLIs exit on the first SIGTERM
and the second / third are never sent. The repeats exist for CLIs whose
event loop is briefly starved (e.g. Node.js CLIs blocked in a synchronous
HTTP call to their MCP bridge) — the second / third SIGTERM lands on a
warmer loop and tends to be serviced. Each signal also writes a distinct
log line, so operators can read the logs and see which CLI needed how
many nudges. After three attempts we stop being polite and SIGKILL.

**What the graces are for.** The total 25s window lets the CLI flush
state the *next* call depends on:

- session files used by `--resume` (`~/.gemini`, `~/.claude`, `~/.codex`,
  `~/.cursor`, …)
- transcript JSONL used for cost reconciliation
- any provider-specific rollout state

Token usage and pricing data MUST already be captured from the terminal
event itself — not from on-disk transcripts — so escalating to `SIGKILL`
after the 25s grace does NOT cost the current turn's billing accuracy.
It only forfeits `--resume` for that turn.

**What the contract is NOT.**

- It is NOT acceptable to send `SIGTERM` and rely on the CLI to exit
  "eventually." Adapters MUST escalate to `SIGKILL` after the third
  SIGTERM's grace expires.
- The escalation MUST NOT be gated on pending in-flight tool calls. By
  the time the terminal event has arrived, any tool calls the CLI is
  still firing are side-effect leakage and MUST be cut off — this is
  the orphan-subprocess hazard.
- It is NOT acceptable to extend the total grace past 25s. A CLI that
  cannot flush state in 25s either has an upstream bug (synchronous
  I/O blocking signal delivery long enough to outlast three SIGTERMs)
  or is doing work it shouldn't be doing after terminal.

**Upstream obligation on each CLI.** Each CLI provider promises: "Given
SIGTERM, I will flush resume state within 25s and exit, OR I accept that
this turn's `--resume` is forfeit." If a CLI cannot honor this (e.g., a
synchronous HTTP call to its MCP bridge with a 120s timeout), the fix
is in that CLI — shorter per-tool timeouts, signal-aware I/O — not in
the adapter's grace budget.

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
| 20 | Graceful cancel | Context cancellation mid-run preserves all streamed chunks; stream channel is closed; partial content is returned if available. |
| 21 | Sandboxed MCP | Built-in tools disabled while MCP bridge tools remain callable. Proves the production pattern: agent can only use tools you explicitly provide. |
| 22 | End-of-turn shutdown | On terminal event (result/done/task_complete), adapter calls `procshutdown.Graceful` → 3 SIGTERMs (10s/10s/5s graces) → SIGKILL. Asserts no post-result `tool_use` events leak through after `cmd.Wait()` returns. See §9 Process Shutdown Contract. |

## Current Test Coverage

### Claude Code (`claude -p --output-format stream-json`)

```sh
RUN_CLAUDE_CODE_PRINT_INTEGRATION=1 go test ./pkg/adapters/claudecode \
  -run 'TestClaudeCodeStructured|TestClaudeCodeStreaming|TestRawClaude' -v -timeout 4m
```

| Area | Test |
|---|---|
| Fresh launch | `TestClaudeCodeStructuredBasicRun` |
| Working directory | `TestClaudeCodeStructuredWorkingDir` |
| System prompt | `TestClaudeCodeStructuredSystemPrompt` |
| Token usage | `TestClaudeCodeStructuredTokenUsage` |
| Streaming text | `TestClaudeCodeStructuredStreaming` |
| Streaming tool calls | `TestClaudeCodeStructuredToolUse` |
| Session metadata | `TestClaudeCodeStructuredSessionMetadata` |
| Multi-step tool use | `TestClaudeCodeStructuredMultiStepToolUse` |
| Model override | `TestClaudeCodeStructuredModelOverride` |
| Image input | `TestClaudeCodePrintRealImageInput` |
| Web search | `TestClaudeCodeRealSearchWeb` |
| Error handling | `TestClaudeCodeStructuredErrorHandling` |
| MCP bridge | `TestClaudeCodeStructuredMCPBridge` |
| Tool disable | `TestClaudeCodeStructuredToolDisable` |
| Multi-turn resume | `TestClaudeCodeStructuredMultiTurnResume` |
| No injected strings | `TestClaudeCodeStructuredNoInjectedStrings` |
| No internal memory | `TestClaudeCodeStructuredNoInternalMemory` |
| Live web search | `TestClaudeCodeStructuredSearchWebLiveData` |
| Graceful cancel | `TestClaudeCodeStructuredGracefulCancel` |
| Sandboxed MCP | `TestClaudeCodeStructuredMCPBridge` (uses `--tools ""` + MCP) |

**Gaps:** none.

### Codex CLI (`codex exec --json`)

```sh
RUN_CODEX_CLI_STREAM_JSON_E2E=1 go test ./pkg/adapters/codexcli \
  -run 'TestCodexCLIRealExecJSON' -v -timeout 6m
```

| Area | Test |
|---|---|
| Fresh launch | `TestCodexCLIStructuredBasicRun` |
| Working directory | `TestCodexCLIStructuredWorkingDir` |
| System prompt | `TestCodexCLIStructuredSystemPrompt` |
| Token usage | `TestCodexCLIStructuredTokenUsage` |
| Streaming text | `TestCodexCLIStructuredStreaming` |
| Streaming tool calls | `TestCodexCLIStructuredToolUse` |
| Session metadata | `TestCodexCLIStructuredSessionMetadata` |
| Multi-step tool use | `TestCodexCLIStructuredMultiStepToolUse` |
| Model override | `TestCodexCLIStructuredModelOverride` |
| Session resume | `TestCodexCLIRealExecJSONContract` (multi-turn with resume) |
| MCP bridge | `TestCodexCLIRealExecJSONMCPBridgeContract` |
| Image input | `TestCodexCLIStructuredImageInput` |
| Web search | `TestCodexCLIStructuredSearchWeb` |
| Live web search | `TestCodexCLIStructuredSearchWebLiveData` |
| Error handling | `TestCodexCLIStructuredErrorHandling` |
| Tool disable | `TestCodexCLIStructuredToolDisable` |
| Multi-turn resume | `TestCodexCLIStructuredMultiTurnResume` |
| No injected strings | `TestCodexCLIStructuredNoInjectedStrings` |
| No internal memory | `TestCodexCLIStructuredNoInternalMemory` |
| Graceful cancel | `TestCodexCLIStructuredGracefulCancel` |
| Sandboxed MCP | `TestCodexCLIRealExecJSONMCPBridgeContract` (uses `--disable shell_tool` + MCP) |

**Gaps:** none.

### Cursor CLI (`cursor-agent --print --output-format stream-json`)

```sh
RUN_CURSOR_CLI_STREAM_JSON_E2E=1 go test ./pkg/adapters/cursorcli \
  -run 'TestCursorCLIStructured' -v -timeout 10m
```

| Area | Test |
|---|---|
| Fresh launch | `TestCursorCLIStructuredBasicRun` |
| Working directory | `TestCursorCLIStructuredWorkingDir` |
| System prompt | `TestCursorCLIStructuredSystemPrompt` |
| Token usage | `TestCursorCLIStructuredTokenUsage` |
| Streaming text | `TestCursorCLIStructuredStreaming` |
| Streaming tool calls | `TestCursorCLIStructuredToolUse` |
| Session metadata | `TestCursorCLIStructuredSessionMetadata` |
| Multi-step tool use | `TestCursorCLIStructuredToolUse` |
| Model override | `TestCursorCLIStructuredModelOverride` |
| Image path | `TestCursorCLIStructuredImagePath` |
| Web search | `TestCursorCLIStructuredSearchWeb` |
| Live web search | `TestCursorCLIStructuredSearchWebLiveData` |
| Error handling | `TestCursorCLIStructuredErrorHandling` |
| MCP bridge | `TestCursorCLIStructuredMCPBridge` |
| Tool disable | `TestCursorCLIStructuredToolDisable` |
| Multi-turn resume | `TestCursorCLIStructuredMultiTurnResume` |
| No injected strings | `TestCursorCLIStructuredNoInjectedStrings` |
| No internal memory | `TestCursorCLIStructuredNoInternalMemory` |
| Sandboxed MCP | `TestCursorCLIStructuredSandboxedMCP` (uses `--mode ask` + `--approve-mcps`) |
| Graceful cancel | `TestCursorCLIStructuredGracefulCancel` |

**Gaps:** none.

### Gemini CLI (`gemini --output-format stream-json`)

```sh
RUN_GEMINI_CLI_STREAM_JSON_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli \
  -run 'TestGeminiCLIRealStreamJSON|TestGeminiCLIStructured' -v -timeout 10m
```

| Area | Test |
|---|---|
| Fresh launch | `TestGeminiCLIRealStreamJSONContract` |
| Working directory | `TestGeminiCLIStructuredWorkingDir` |
| System prompt | `TestGeminiCLIStructuredSystemPrompt` |
| Token usage | `TestGeminiCLIUsageAndCost` |
| Streaming text | `TestGeminiCLIStructuredStreaming` |
| Streaming tool calls | `TestGeminiCLIStructuredToolUse` |
| Multi-step tool use | `TestGeminiCLIStructuredMultiStepToolUse` |
| Model override | `TestGeminiCLIStructuredModelOverride` |
| Session resume | `TestGeminiCLIRealStreamJSONContract` (multi-turn with resume) |
| Session metadata | `TestGeminiCLIRealStreamJSONContract` (session_id + project_dir_id) |
| MCP bridge | `TestGeminiCLIRealStreamJSONMCPBridgeContract` |
| Image path | `TestGeminiCLIStructuredImagePath` |
| Web search | `TestGeminiCLIStructuredSearchWeb` |
| Live web search | `TestGeminiCLIStructuredSearchWebLiveData` |
| Error handling | `TestGeminiCLIStructuredErrorHandling` |
| Tool disable | `TestGeminiCLIStructuredToolDisable` |
| Multi-turn resume | `TestGeminiCLIStructuredMultiTurnResume` |
| No injected strings | `TestGeminiCLIStructuredNoInjectedStrings` |
| No internal memory | `TestGeminiCLIStructuredNoInternalMemory` |
| Graceful cancel | `TestGeminiCLIStructuredGracefulCancel` |
| Sandboxed MCP | `TestGeminiCLIRealStreamJSONMCPBridgeContract` (uses admin policy deny + MCP settings) |

**Gaps:** none.

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
| Model override | `TestOpenCodeCLIStructuredModelOverride` |
| Image path | `TestOpenCodeCLIRealImagePathAnalysis` |
| Web search | `TestOpenCodeCLIRealSearchWeb` |
| Live web search | `TestOpenCodeCLIRealSearchWebLiveData` |
| Error handling | `TestOpenCodeCLIStructuredErrorHandling` |
| MCP bridge | `TestOpenCodeCLIStructuredMCPBridge` |
| Multi-turn resume | `TestOpenCodeCLIStructuredMultiTurnResume` |
| No injected strings | `TestOpenCodeCLIStructuredNoInjectedStrings` |
| No internal memory | `TestOpenCodeCLIStructuredNoInternalMemory` |
| Tool disable | `TestOpenCodeCLIStructuredToolDisable` |
| Sandboxed MCP | `TestOpenCodeCLIStructuredSandboxedMCP` (limitation: `*:deny` blocks all including MCP) |
| Graceful cancel | `TestOpenCodeCLIStructuredGracefulCancel` |

**Gaps:** none.

## Full Provider Coverage Matrix

| Area | Claude | Codex | Cursor | Gemini | OpenCode |
|---|:---:|:---:|:---:|:---:|:---:|
| 1. Fresh launch | yes | yes | yes | yes | yes |
| 2. Working directory | yes | yes | yes | yes | yes |
| 3. System prompt | yes | yes | yes | yes | yes |
| 4. Token usage | yes | yes | yes | yes | yes |
| 5. Streaming text | yes | yes | yes | yes | yes |
| 6. Streaming tool calls | yes | yes | yes | yes | yes |
| 7. Session metadata | yes | yes | yes | yes | yes |
| 8. Multi-step tool use | yes | yes | yes | yes | yes |
| 9. Model override | yes | yes | yes | yes | yes |
| 10. Image path | yes | yes | yes | yes | yes |
| 11. Web search | yes | yes | yes | yes | yes |
| 12. Live web search | yes | yes | yes | yes | yes |
| 13. Cancellation | yes | yes | yes | yes | yes |
| 14. Error handling | yes | yes | yes | yes | yes |
| 15. MCP bridge | yes | yes | yes | yes | yes |
| 16. Tool disable | yes | yes | yes | yes | yes |
| 17. Multi-turn resume | yes | yes | yes | yes | yes |
| 18. No injected strings | yes | yes | yes | yes | yes |
| 19. No internal memory | yes | yes | yes | yes | yes |
| 20. Graceful cancel | yes | yes | yes | yes | yes |
| 21. Sandboxed MCP | yes | yes | yes | yes | n/a† |

† OpenCode's permission system is all-or-nothing (`"*":"deny"` blocks all tools including MCP).
Sandboxed MCP for OpenCode must be implemented at the orchestration layer (mcpagent).

## Related Docs

- `docs/coding_sdk_tmux_contract.md`
- `docs/CODING_AGENT_TRANSPORT_PATTERNS.md`
