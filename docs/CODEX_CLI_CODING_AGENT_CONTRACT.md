# Codex CLI Coding Agent Contract

This document defines the behavior we rely on when `codex-cli` is used as a
coding-agent backend. Codex has two transports:

- `exec-json`: structured `codex exec --json`, used for workflow and
  deterministic agent runs.
- `persistent-interactive`: tmux-backed Codex TUI, used for interactive chat
  when users need to send messages while Codex is already working.

The shared terminal-agent patterns are documented in
`docs/CODING_AGENT_TRANSPORT_PATTERNS.md`.

## Launch

By default, Codex CLI uses the structured non-interactive transport:

- Run `codex exec --json`.
- Use `gpt-5.5` as the default model.
- Pass the model with `--model gpt-5.5` unless a caller explicitly
  overrides it.
- Pass system/developer instructions with `-c developer_instructions=...`.
- Disable the built-in shell tool with `--disable shell_tool` when the runtime
  wants Codex to use only the MCP bridge.
- Use `approval_policy="never"` for non-interactive MCP calls.

Codex CLI should not use tmux for workflow execution. The JSON stream gives us
structured assistant text, tool starts, tool results, usage, and thread ids.
Moving workflow execution to terminal scraping would be less reliable.

Interactive chat may enable tmux with
`WithCodexPersistentInteractiveSession(true)` and
`WithCodexInteractiveSessionID(<app-session-id>)`.

## Tools

Codex receives MCP bridge servers through config overrides:

- `mcp_servers.<name>.command=...`
- `mcp_servers.<name>.args=[...]`
- `mcp_servers.<name>.env.<KEY>=...`
- `mcp_servers.<name>.tool_timeout_sec=...`

When shell is disabled, the MCP bridge is the intended tool surface.

## Streaming

The adapter must parse `codex exec --json` events and emit:

- assistant content chunks from `agent_message`
- tool-call start chunks from `command_execution`, `mcp_call`,
  `mcp_tool_call`, `web_search`, and `file_change`
- tool-call end chunks with tool results
- token usage from `turn.completed`
- `codex_thread_id` from `thread.started`

## Multi-Turn Chat

Workflow multi-turn state is based on native thread ids, not a persistent tmux
session.

Normal flow:

1. Start with `codex exec --json --model gpt-5.5 <prompt>`.
2. Capture `codex_thread_id`.
3. Resume with `codex exec resume --json --model gpt-5.5
   <thread_id> <latest prompt>`.
4. On resume, send only the latest user message. Older conversation context
   stays in the Codex thread.

Interactive chat flow:

1. Start `codex --no-alt-screen --model gpt-5.5 ...` inside tmux.
2. Register `app_session_id -> tmux_session_name`.
3. Paste the user message with `tmux load-buffer`, `tmux paste-buffer -p -r`,
   and `tmux send-keys C-m`.
4. While Codex is still working, route `/steer` messages to the same tmux
   session and paste them directly into the TUI.
5. After Codex returns to idle, keep the tmux session alive for follow-up chat.
6. Close the tmux session after the idle timeout.

The interactive transport trades structured JSON events for live user input. It
must only be used for chat surfaces that require mid-turn steering.

## Cancel

The adapter starts Codex in its own process group. Context cancellation kills
that process group, so a canceled backend request should not leave Codex running
in the background.

## Tests

Default tests are deterministic and credit-free. They cover pure parser,
normalization, and stream-delta logic using static pane/event fixtures captured
from real CLI behavior. Transport contracts must be validated with real Codex
CLI E2E tests, not replacement binaries.

Important tests:

- `TestExtractCodexVisibleAssistantTextFiltersTUIProgress`
- `TestStripCodexHistoricalAssistantTextRemovesPaneReplay`
- `TestStreamCodexAssistantDeltaDedupesCumulativeRedrawAfterContinuationChunks`
- `TestCodexIdleDetectionIgnoresAssistantProseAboutRunning`
- `TestCodexCLIInteractiveIntegrationSpark` (opt-in real Codex CLI)
- `TestCodexCLIRealInteractiveTmuxFullContract` (opt-in real Codex CLI)
- `TestCodexCLIRealInteractiveMCPBridgeContract` (opt-in real Codex CLI)
- `TestCodexCLIRealInteractiveLiveInputAndEscapeContract` (opt-in real Codex CLI)
- `TestCodexCLIRealExecJSONContract` (opt-in real Codex CLI structured
  transport plus native resume)
- `TestCodexCLIRealExecJSONMCPBridgeContract` (opt-in real Codex CLI
  structured MCP bridge)
- `TestCodexCLIRealSearchWeb` (opt-in real Codex CLI native web search; asserts
  a streamed web-search tool event)
- `TestGetDefaultModelCodexCLIUsesGPT55`

Run the real persistent interactive contract tests with:

```sh
RUN_CODEX_CLI_REAL_E2E=1 RUN_CODEX_CLI_INTERACTIVE_E2E=1 go test ./pkg/adapters/codexcli -run 'TestCodexCLIRealInteractive|TestCodexCLIInteractiveIntegrationSpark' -v -timeout 6m
```

Run the real structured transport contract tests with:

```sh
RUN_CODEX_CLI_STREAM_JSON_E2E=1 go test ./pkg/adapters/codexcli -run 'TestCodexCLIRealExecJSON' -v -timeout 6m
```

Run the real native web-search contract test with:

```sh
RUN_CODEX_CLI_SEARCH_WEB_E2E=1 go test ./pkg/adapters/codexcli -run 'TestCodexCLIRealSearchWeb' -v -timeout 4m
```

Real Codex checks are in `mcp-agent-builder-go/agent_go/cmd/testing`:

- `codex-mcp-tool-call`
- `codex-resume-after-cancel`

Use `--model gpt-5.3-codex-spark` for real smoke tests unless testing another
Codex model explicitly. The normal product default can remain tiered
separately; release tests should use the cheaper contract model.
