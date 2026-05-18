# Cross-Repo Integration Contract

This document defines the integration contract between the three repos that form
the production LLM pipeline:

```
mcp-agent-builder-go (HTTP API + frontend)
  → mcpagent (orchestrator + agent loop)
    → multi-llm-provider-go (adapter + real CLI)
```

The adapter-level e2e tests in `multi-llm-provider-go` prove the bottom layer.
This contract defines what must additionally hold across the full stack.

## Boundaries

### Boundary 1: mcp-agent-builder-go → mcpagent

The Go API server builds an `LLMAgentConfig` from the HTTP request and passes it
to `agent.NewLLMAgentWrapperWithTrace()`. The wrapper calls
`llm.InitializeLLM()` with a `llm.Config`.

**Config fields that must propagate:**

| Field | Source | Destination |
|---|---|---|
| Provider | `req.LLMConfig.Primary.Provider` | `llm.Config.Provider` |
| ModelID | `req.LLMConfig.Primary.ModelID` | `llm.Config.ModelID` |
| Temperature | `req.Temperature` | `llm.Config.Temperature` |
| API Keys | `config/provider-api-keys.json` | `llm.Config.APIKeys` |
| Fallbacks | `req.LLMConfig.Fallbacks[]` | `llm.Config.FallbackModels` |
| Working Dir | `agentConfig.CodingAgentWorkingDir` | agent option → adapter option |
| Session ID | HTTP `X-Session-ID` | `agentConfig.SessionID` → `WithSessionID()` |
| Transport | `agentConfig.ClaudeCodeTransport` | `llm.Config.ClaudeCodeTransport` |

### Boundary 2: mcpagent → multi-llm-provider-go

The agent's `executeLLM()` builds provider-specific options and calls
`llmInstance.GenerateContent(ctx, messages, opts...)`.

**Provider-specific option mapping:**

| Provider | mcpagent option builder | Adapter options passed |
|---|---|---|
| claude-code | lines 962-1019 | `WithMCPConfig`, `WithResumeSessionID`, `WithClaudeCodeTools`, `WithClaudeCodeSettings`, `WithMaxTurns`, `WithClaudeCodeEffort` |
| gemini-cli | lines 1027-1118 | `WithGeminiProjectSettings`, `WithGeminiAdminPolicyPath`, `WithGeminiWorkingDir`, `WithGeminiProjectDirID`, `WithGeminiResumeSessionID` |
| codex-cli | lines 1121-1179 | `WithCodexDisableShellTool`, `WithCodexApprovalPolicy`, `WithCodexConfigOverrides`, `WithCodexResumeSessionID` |
| cursor-cli | lines 1182-1194 | `WithCursorMCPConfig`, `WithCursorApproveMCPs`, `WithCursorForce` |
| opencode-cli | lines 1197-1212 | `WithOpenCodeMCPConfig`, `WithOpenCodeAgent` |

**Universal options (all providers):**
- `WithReasoningEffort()`
- `WithThinkingLevel()`
- `WithThinkingBudget()`
- `llmtypes.WithStreamingChan()`

### Boundary 3: multi-llm-provider-go → CLI

Each adapter launches the provider CLI, parses the JSON stream, and returns
`*llmtypes.ContentResponse`. This boundary is fully covered by the adapter-level
e2e tests in `docs/coding_sdk_structured_contract.md`.

## Integration Contract Areas

### IC-1: Config Propagation

**What must hold:** Every config field set in the HTTP request must arrive at
the adapter's `GenerateContent` call with the correct value. No field should be
silently dropped or transformed incorrectly.

**Proof required:**
- Provider string → correct adapter instantiated (not a different one)
- ModelID → adapter receives it and passes `--model` to CLI
- Temperature → adapter receives it (API providers use it; CLI providers may ignore)
- API key → adapter gets the correct provider key (not a different provider's key)
- Working directory → adapter sets `cmd.Dir` / `--workspace` / `--cd`
- Fallback models → agent iterates them on failure

**Risk areas:**
- Provider string → `llm.Provider` enum conversion can silently map to wrong adapter
- Cross-provider fallback format `"provider/model"` parsing
- `CodingAgentWorkingDir` not wired through for all providers

### IC-2: Streaming Chunk Flow

**What must hold:** Every `StreamChunk` emitted by the adapter must arrive at
the SSE event stream sent to the frontend. No chunk type should be dropped.

**Proof required:**
- `StreamChunkTypeContent` → `StreamingChunkEvent` → SSE `event: chunk`
- `StreamChunkTypeToolCallStart` → `ToolCallStartEvent` → SSE `event: tool_start`
- `StreamChunkTypeToolCallEnd` → `ToolCallEndEvent` → SSE `event: tool_end`
- `StreamChunkTypeReasoning` → forwarded (if applicable)
- Stream channel closed → `StreamingEndEvent` → SSE `event: stream_end`

**Risk areas:**
- `startStreaming()` goroutine reads from channel; main goroutine calls
  `GenerateContent`. If `GenerateContent` returns before all chunks are read,
  the goroutine must drain the channel (verified at lines 772-775).
- Non-blocking sends in adapter: if channel buffer (256) fills, chunks are
  silently dropped.
- Tool call chunks from CLI providers accumulate in `sm.CLIToolCalls` for
  history — if this fails, multi-turn loses tool context.

### IC-3: Token Usage & Cost

**What must hold:** Token counts from the adapter response must reach the cost
ledger and be available via `GET /api/cost/summary`.

**Proof required:**
- `resp.Usage.InputTokens` → `TokenUsageEvent.PromptTokens` → cost ledger
- `resp.Usage.OutputTokens` → `TokenUsageEvent.CompletionTokens` → cost ledger
- `resp.Usage.CacheTokens` → `TokenUsageEvent.GenerationInfo.cache_read_input_tokens`
- Cost calculation uses correct per-model pricing
- Multi-step runs (agent uses tool, gets result, responds) sum all steps

**Risk areas:**
- CLI providers report usage differently (Codex: `turn.completed`, Gemini:
  `result`, OpenCode: `step_finish`). If adapter doesn't sum across steps,
  multi-tool runs undercount.
- Cache token field names vary by provider (`CacheTokens` vs
  `cached_input_tokens` vs `cache.read`).

### IC-4: Session ID & Resume

**What must hold:** Session IDs from the adapter response must be stored and
passed back on subsequent turns to resume the conversation.

**Proof required:**
- Turn 1: adapter returns `session_id` in `GenerationInfo.Additional`
- mcpagent stores it in agent struct (`ClaudeCodeSessionID`, `GeminiSessionID`, etc.)
- Turn 2: agent passes it back via `WithResumeSessionID()`
- CLI resumes the conversation (context preserved)

**Risk areas:**
- Session ID key names differ per provider (`claude_code_session_id`,
  `gemini_session_id`, `codex_thread_id`, `cursor_session_id`,
  `opencode_session_id`). If mcpagent reads the wrong key, resume silently
  starts a fresh session.
- Codex resume is conditional on `!codingAgentPersistentInteractiveEnabled()`
  (line 1126). If persistent interactive is on, structured resume is skipped.

### IC-5: Model Metadata

**What must hold:** The actual model used (as reported by the CLI) must be
available in the API response or events.

**Current state:**
- Claude Code: `claude_code_model` = actual model (e.g., `claude-opus-4-6`)
- Cursor CLI: `cursor_model` = display name (e.g., `Composer 2 Fast`), not model ID
- Gemini CLI: `gemini_model` = resolved model (e.g., `gemini-3.1-flash-lite`)
- Codex CLI: **not available** — CLI doesn't report model in JSON events
- OpenCode CLI: **not available** — CLI doesn't report model in JSON events

**Proof required:**
- Model name from adapter reaches `TokenUsageEvent` or SSE event
- Frontend can display which model actually ran (not just what was requested)

**Risk areas:**
- Codex and OpenCode don't report model. The wrapper must either:
  - Pass through the requested model ID as a best-effort fallback, or
  - Accept that model is unknown and not display misleading info.
- Cursor returns display name, not model ID. Cost calculation needs model ID.

### IC-6: Fallback Chain

**What must hold:** When the primary model fails, the agent must try fallback
models in order and succeed on the first available one.

**Proof required:**
- Primary model quota exhausted → fallback model tried
- Fallback success → `FallbackModelUsedEvent` emitted with new model
- Agent permanently updates to fallback model for the session
- Cross-provider fallback (e.g., `openai/gpt-5.5` → `anthropic/claude-opus-4-7`)
  instantiates a different adapter

**Risk areas:**
- Cross-provider fallback requires `llm.InitializeLLM()` with a different
  provider. If fallback logic only changes model ID (not provider), wrong
  adapter used.
- Fallback models parsed from `"provider/model"` format. If format changes or
  provider name doesn't match enum, silent failure.

### IC-7: Error Propagation

**What must hold:** Errors from the adapter must reach the frontend as
meaningful error events, not silently swallowed.

**Proof required:**
- CLI binary missing → clear error event to frontend
- CLI exits non-zero → error message includes stderr
- Context cancellation → clean shutdown, partial content preserved
- Auth failure (bad API key) → specific error type (not generic)

**Risk areas:**
- `GenerateContentWithRetry` retries up to 5 times with backoff. If the error
  is permanent (bad API key), this wastes 5 minutes before surfacing.
- Cancellation in streaming: adapter must close channel, goroutine must exit,
  `StreamingEndEvent` must fire.

### IC-8: Cancellation Propagation

**What must hold:** When the user cancels (SSE disconnect, explicit cancel API
call), cancellation must propagate through all layers and kill the CLI process.

**Proof required:**
- Frontend disconnect → context cancelled → adapter kills process group
- All streamed chunks before cancellation are preserved in events
- `StreamingEndEvent` fires with partial content
- No goroutine leaks (channel drained, process waited)

**Risk areas:**
- `streamCtx` has 3-hour timeout. If SSE disconnects but context isn't
  cancelled, agent runs for 3 hours in background.
- Process group kill (`syscall.Kill(-pid, SIGTERM)`) must work on the OS.
  macOS vs Linux differences in process group handling.

### IC-9: Multi-Turn Tool Context

**What must hold:** Tool calls from previous turns must be included in the
conversation history for subsequent turns.

**Proof required:**
- Turn 1: agent uses tool, result captured in `sm.CLIToolCalls`
- Tool calls appended to `updatedMessages` history
- Turn 2: history sent to adapter includes tool call summaries
- CLI providers receive tool context as text (converted by
  `convertToolCallsToTextForCLI()`)

**Risk areas:**
- CLI providers need plain-text history. If `convertToolCallsToTextForCLI()`
  truncates or loses information, agent loses context.
- Tool call chunks (`StreamChunkTypeToolCallEnd`) have `ToolResult` — if this
  isn't captured, history is incomplete.

### IC-10: MCP Bridge Propagation

**What must hold:** MCP server configs set at the API level must reach the
CLI as bridge tool configurations.

**Proof required:**
- `WithMCPConfig(json)` → Claude Code `--mcp-config <file>`
- `WithCursorMCPConfig(json)` → Cursor `.cursor/mcp.json`
- `WithOpenCodeMCPConfig(json)` → OpenCode `opencode.jsonc`
- MCP tool calls work through the bridge end-to-end

**Risk areas:**
- Config file format differs per CLI. If JSON schema changes between CLI
  versions, bridge breaks silently.
- MCP server startup timeout. If server takes too long, first tool call fails.

## Test Matrix

| # | Area | Boundary | Test Location |
|---|---|---|---|
| IC-1 | Config propagation | B1 + B2 | mcpagent |
| IC-2 | Streaming chunk flow | B2 + B1 | mcpagent + mcp-agent-builder-go |
| IC-3 | Token usage & cost | B3 → B1 | mcp-agent-builder-go |
| IC-4 | Session ID & resume | B3 → B2 → B3 | mcpagent |
| IC-5 | Model metadata | B3 → B1 | mcpagent |
| IC-6 | Fallback chain | B2 | mcpagent |
| IC-7 | Error propagation | B3 → B1 | mcpagent + mcp-agent-builder-go |
| IC-8 | Cancellation propagation | B1 → B3 | mcp-agent-builder-go |
| IC-9 | Multi-turn tool context | B2 → B3 | mcpagent |
| IC-10 | MCP bridge propagation | B1 → B3 | mcpagent |

## Provider Coverage

Each IC area should be verified for all 5 coding agent providers plus all API
providers that the system supports.

**Coding agents (structured JSON transport):**
- claude-code
- codex-cli
- cursor-cli
- gemini-cli
- opencode-cli

**API providers:**
- openai
- anthropic
- vertex
- bedrock
- azure

## Priority

**P0 (blocks production):**
- IC-2: Streaming chunk flow (if chunks drop, frontend shows blank)
- IC-4: Session ID & resume (if resume fails, multi-turn breaks)
- IC-8: Cancellation propagation (if cancel leaks, background processes pile up)

**P1 (degrades quality):**
- IC-1: Config propagation (wrong model = wrong results)
- IC-3: Token usage & cost (billing accuracy)
- IC-7: Error propagation (user sees generic errors)

**P2 (important but not urgent):**
- IC-5: Model metadata (cosmetic in UI)
- IC-6: Fallback chain (rare path, only on failures)
- IC-9: Multi-turn tool context (affects long conversations)
- IC-10: MCP bridge propagation (MCP is additive feature)

## Existing Coverage

**Adapter-level (multi-llm-provider-go):**
20-area certification matrix. See `docs/coding_sdk_structured_contract.md`.
- Claude Code: 17 structured tests (all 20 areas covered)
- Codex CLI: 18 structured tests (all 20 areas covered)
- Cursor CLI: 18 structured tests (all 20 areas covered)
- Gemini CLI: 15 structured tests (all 20 areas covered)
- OpenCode CLI: 14 structured tests (all 20 areas covered)
- All gaps closed: full 20/20 coverage for all 5 providers

**Agent-level (mcpagent):**
- IC-1: `agent/coding_agent_options_test.go` — interactive options, working dir, fallback provider (6 tests)
- IC-2: `agent/llm_generation_streaming_test.go` — all chunk types, accumulation, drain, finish (13 tests)
- IC-4: `agent/session_resume_integration_test.go` — extraction, resume injection, round-trip (18 subtests)
- IC-5: `agent/llm_generation_streaming_test.go` — Gemini/Claude metadata in StreamingEndEvent (2 tests)
- IC-6: `agent/fallback_parsing_test.go` — parseFallbackModelRef, dedupe, config propagation (17 subtests)
- IC-7: `agent/error_classification_test.go` — classifyLLMError, context cancel guard, nil safety (44 subtests)
- IC-9: `agent/cli_tool_history_test.go` — CLIToolCalls round-trip, skip incomplete, attach to resp (3 tests)
- IC-10: `agent/coding_agents_bridge_test.go` — session URL, bridge URL override, missing config (7 tests)

**API-level (mcp-agent-builder-go):**
- IC-3: `cmd/server/cost_routes_test.go` — cache token extraction, cost observer event flow, multi-turn accumulation (20 subtests)
- IC-7: `cmd/server/sse_test.go` — SSE format, id handling, panic recovery, error event serialization (4 tests)
- IC-8: `internal/events/event_store_test.go` — subscribe, filter hidden, streaming bypass, unsubscribe (19 subtests)
- IC-8: `cmd/server/shutdown_cleanup_test.go` — cancel active work, cleanup providers (2 tests)

## Implementation Order

1. ✅ IC-4 Session ID & resume (mcpagent) — 18 subtests
2. ✅ IC-2 Streaming chunk flow (mcpagent) — 13 tests
3. ✅ IC-8 Cancellation propagation (mcp-agent-builder-go) — 19 subtests (subscriber layer)
4. ✅ IC-1 Config propagation (mcpagent) — 6 tests (interactive options + working dir)
5. ✅ IC-3 Token usage & cost (mcp-agent-builder-go) — 20 subtests
6. ✅ IC-7 Error propagation (mcpagent + mcp-agent-builder-go) — 44 + 4 tests
7. ✅ IC-6 Fallback chain (mcpagent) — 17 subtests
8. ✅ IC-5 Model metadata (mcpagent) — 2 tests (Gemini + Claude)
9. ✅ IC-9 Multi-turn tool context (mcpagent) — 3 tests
10. ✅ IC-10 MCP bridge propagation (mcpagent) — 7 tests
