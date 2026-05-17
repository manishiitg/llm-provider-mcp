# Gemini CLI Coding Agent Contract

This document defines the behavior we rely on when `gemini-cli` is used as a
coding-agent backend. Gemini CLI has two transports:

- `stream-json`: structured `gemini --output-format stream-json --prompt ...`,
  used for workflow and deterministic agent runs.
- `persistent-interactive`: tmux-backed Gemini TUI, used for interactive chat
  when users need to send messages while Gemini is already working.

The shared terminal-agent patterns are documented in
`docs/CODING_AGENT_TRANSPORT_PATTERNS.md`.

## Launch

By default, Gemini CLI uses the structured non-interactive transport:

- Run `gemini --output-format stream-json`.
- Pass the user prompt with `--prompt`.
- Pass system instructions with `GEMINI_SYSTEM_MD`.
- Pass project settings through a scoped `.gemini/settings.json`.
- Capture `gemini_session_id` and `gemini_project_dir_id` from stream-json
  responses for native resume.

Interactive chat may enable tmux with
`WithGeminiPersistentInteractiveSession(true)` and
`WithGeminiInteractiveSessionID(<app-session-id>)`.

## Tools

Gemini receives MCP bridge servers through project settings:

- `.gemini/settings.json`
- `.gemini/policies/*.toml`
- optional hooks under `.gemini/hooks`

When running as a policy-controlled workflow/chat agent, built-in filesystem and
shell tools should stay denied by policy and the MCP bridge should be the
intended tool surface.

## Multi-Turn Chat

Workflow multi-turn state is based on Gemini's native session id and project
directory id, not a persistent tmux session.

Normal flow:

1. Start with `gemini --output-format stream-json --prompt <latest prompt>`.
2. Capture `gemini_session_id` and `gemini_project_dir_id`.
3. Resume with `gemini --output-format stream-json --resume <session_id>
   --prompt <latest prompt>` from the same project directory.
4. Send only the latest user message on resume.

Interactive chat flow:

1. Start `gemini --model gemini-3.1-flash-lite` inside tmux from a project
   directory scoped to the app session id, unless the caller explicitly
   selects another Gemini model.
2. Register `app_session_id -> tmux_session_name`.
3. Paste the user message with `tmux load-buffer`, `tmux paste-buffer -p -r`,
   and `tmux send-keys C-m`.
4. While Gemini is still working, route `/steer` messages to the same tmux
   session and paste them directly into the TUI.
5. After Gemini returns to idle, keep the tmux session alive for follow-up chat.
6. Close the tmux session after the idle timeout.

## Done Detection

The interactive adapter considers a turn done only after Gemini returns to an
idle prompt containing `Type your message`.

The parser ignores Gemini TUI chrome such as:

- logo and auth text
- update notices
- keyboard shortcut/footer text
- workspace/model footer
- box drawing borders
- the echoed user prompt

## Tests

Default tests are deterministic and credit-free. They cover pure parser,
normalization, and stream-delta logic using static pane/event fixtures captured
from real CLI behavior.

Provider-contract validation must run the real Gemini CLI E2E. The real E2E is
environment-gated so regular CI does not spend credits accidentally.

Important tests:

- `TestGeminiCLIInteractiveIntegrationFlashLite` (real Gemini CLI)
- `TestGeminiCLIRealInteractiveTmuxFullContract` (real Gemini CLI full
  tmux chat contract)
- `TestGeminiCLIRealInteractiveMCPBridgeContract` (real Gemini CLI with MCP
  bridge and policy)
- `TestGeminiCLIRealInteractiveLiveInputAndEscapeContract` (real Gemini CLI
  live input plus Escape/cancel path)
- `TestGeminiCLIRealStreamJSONContract` (real Gemini CLI structured transport
  plus native resume)
- `TestGeminiCLIRealStreamJSONMCPBridgeContract` (real Gemini CLI structured
  MCP bridge and policy)
- `TestGeminiCLISearchWebSmoke` (real Gemini CLI native Google web search;
  asserts a streamed web-search tool event)

Run the real persistent interactive contract tests with:

```sh
RUN_GEMINI_CLI_REAL_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli -run 'TestGeminiCLIRealInteractive|TestGeminiCLIInteractiveIntegrationFlashLite' -v -timeout 6m
```

Run the real structured transport contract tests with:

```sh
RUN_GEMINI_CLI_STREAM_JSON_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli -run 'TestGeminiCLIRealStreamJSON' -v -timeout 6m
```

Run the real native web-search contract test with:

```sh
RUN_GEMINI_CLI_SEARCH_WEB_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli -run 'TestGeminiCLISearchWebSmoke' -v -timeout 4m
```

These tests must be run before releasing Gemini CLI transport changes. Static
parser fixtures remain useful for UI quality regressions, but transport
behavior must be proven against the real CLI.
