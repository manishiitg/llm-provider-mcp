# Coding SDK Tmux Contract

This document defines the tmux-backed interactive transport contract for coding
SDK providers in `multi-llm-provider-go`.

Covered providers:

- `claude-code`
- `codex-cli`
- `cursor-cli`
- `gemini-cli`

The goal is to expose terminal-native coding tools through the normal provider
interface for both chat and workflow execution: terminal snapshot progress, MCP
bridge tool calls, same-session follow-up input, cancellation, final response
extraction, and native resume.

The mechanical source of truth is `coding_agent_contract.go`. Every provider
that should behave like a coding agent must be declared there with its transport
and capabilities. Host applications should use that contract instead of
copying provider string lists. Adding a future provider means adding a contract
entry and tests for:

- explicit working directory
- native system/developer instruction transport
- MCP bridge launch and bridge-only tool policy
- live input behavior
- interrupt/cancel behavior
- terminal snapshot streaming behavior
- final assistant text extraction
- resume or explicit non-resume semantics
- process-scoped cleanup
- tmux session-loss handling for `no server running`, `can't find pane`,
  `can't find session`, and `no current target`

This contract is the normal product path. Structured/JSON CLI transports are
legacy fallback or provider-regression test paths unless a capability has not
yet been implemented in tmux.

## Product Surfaces

There are two product surfaces that share the same coding-CLI transport:

- Interactive chat: a user is chatting with a coding agent in a workspace. This
  includes workflow builder chat, optimizer chat, run chat, and normal coding
  agent chat.
- Workflow execution: a workflow step, route, sub-agent, background execution,
  scheduled job, or deterministic test run.

Both surfaces should run the provider TUI in tmux when the provider supports it.
The difference is orchestration: workflow execution sends intermediate messages
programmatically and waits for idle; chat accepts live user steer input.

## Provider Transports

Claude Code:

- Default interactive transport: `claude` inside tmux
  (`CLAUDE_CODE_TRANSPORT=experimental`).
- Legacy structured print transport: `claude -p --output-format stream-json`
  (`CLAUDE_CODE_TRANSPORT=print`). This path is disabled unless
  `CLAUDE_CODE_ALLOW_LEGACY_PRINT=1` is also set, and should only be used for
  targeted legacy tests.
- Do not use `claude -p` or `claude --print` inside the tmux transport.
- Workflow and chat both use the tmux transport.

Codex CLI:

- Legacy structured transport: `codex exec --json`.
- Interactive transport: `codex` TUI inside tmux.
- Default coding model: `gpt-5.5`.
- Workflow and chat both use the tmux transport when an owner session id is
  available.

Cursor CLI:

- Interactive transport: `cursor-agent` TUI inside tmux.
- The adapter must not use `cursor-agent -p`, `--print`, or
  `--output-format stream-json` for the tmux path.
- Default model selector: `cursor-cli`, which means "do not pass --model; let
  Cursor use its configured/account default".
- Bounded per-turn calls should still launch `cursor-agent` in tmux, paste one
  turn, parse the final TUI output, and close the tmux session.
- Persistent chat should keep the same tmux session alive when
  `cursor_interactive_session_id` plus `cursor_persistent_interactive=true` are
  provided.

Gemini CLI:

- Legacy structured transport: `gemini --output-format stream-json --prompt ...`.
- Interactive transport: `gemini` TUI inside tmux.
- Workflow and chat both use the tmux transport when an owner session id is
  available.

## Image Input Contract

`llmtypes.ImageContent` must never be silently dropped.

- Claude Code `print`: supports base64 and URL image parts through stream-json
  content blocks.
- Claude Code `experimental` tmux: rejects image input until the TUI transport
  has a real attachment path.
- Codex CLI `exec`: supports base64 image parts by writing temporary image files
  and passing them through native `--image` flags. Image URLs are rejected
  because Codex CLI expects local files.
- Codex CLI persistent tmux: rejects image input until live image attachment is
  implemented for the TUI session.
- Cursor CLI tmux: rejects image input until live image attachment is
  implemented for the TUI session.
- Gemini CLI: rejects image input in the current adapter because the supported
  headless/tmux transport has no image attachment flag.

## Launch Contract

All tmux-backed providers must:

- Create one adapter-owned tmux session per application session id.
- Use an internal tmux session name that does not leak into the UI.
- Register `app_session_id -> tmux_session_name` before accepting live input.
- Run the provider CLI from the same effective working directory used by the
  MCP bridge shell tools. This is a hard invariant:
  - chat resolves to the logged-in user's chat workspace
  - workflow chat resolves to the workflow workspace
  - workflow step agents resolve to the run/execution workspace selected for
    that step session
  - adding a new coding CLI provider requires adding an explicit working-dir
    option and contract coverage before it can be treated as a coding CLI
- Keep launch settings stable for the lifetime of the persistent tmux session.
- Close and recreate the session if model, MCP config, approval mode, tool
  policy, or workspace root changes. Provider-specific system prompts are
  pinned at session start for persistent chat; normal per-turn app prompt
  variation must not silently restart the TUI and lose chat context.

Provider-specific launch requirements:

- Claude Code:
  - pass system instructions with `--system-prompt-file`
  - pass MCP config with `--mcp-config <file> --strict-mcp-config`
  - disable internal tools with `--tools ""` unless explicitly overridden
  - use `--permission-mode dontAsk`
  - start with `--session-id <uuid> --name <display-name>` when creating a new
    native session
  - resume with `--resume <uuid>`
- Codex CLI:
  - pass model with `--model`, defaulting to `gpt-5.5`
  - pass developer/system instructions through native config overrides
  - pass MCP bridge servers through config overrides
  - disable `shell_tool` when bridge-only tool policy is required
  - use a no-prompt approval policy for MCP-controlled runs
- Cursor CLI:
  - launch `cursor-agent` in tmux from the caller-provided workspace directory
  - pass model with `--model` only when the model selector is not `cursor-cli`
    or `auto`
  - pass system/developer instructions through a temporary/restored
    `.cursor/rules/*.mdc` rule with `alwaysApply: true`
  - pass MCP bridge servers through a temporary/restored `.cursor/mcp.json`
  - pass project permissions through a temporary/restored `.cursor/cli.json`
  - pass `--workspace <dir>` and keep process cwd aligned with the MCP bridge
    shell cwd
  - never concatenate system/developer instructions into the pasted user
    message
- Gemini CLI:
  - pass system instructions with `GEMINI_SYSTEM_MD`
  - pass MCP bridge and policy through scoped `.gemini/settings.json` and
    `.gemini/policies`
  - keep the project dir id stable for a resumed Gemini session
  - deny built-in filesystem/shell tools by policy when bridge-only behavior is
    required
  - keep the TUI session alive when app-level system prompt text varies between
    turns; Gemini receives a bounded prior-turn transcript with the current
    message so the final answer remains correct even when native TUI context is
    not sufficient

## Input Contract

User input must be pasted into the TUI. It must not be typed key-by-key.

Required tmux sequence:

```text
tmux load-buffer <payload>
tmux paste-buffer -p -r
tmux send-keys C-m
```

The paste path must preserve:

- multiline text
- blank lines
- quotes and markdown
- shell-looking commands
- JSON
- Unicode
- pasted blocks

System/developer instructions must not be concatenated into the pasted user
message. They must use the provider's native instruction mechanism.

## MCP Bridge Contract

The MCP bridge is the intended tool surface for policy-controlled runs.

The provider TUI may show provider-native progress, but actual app/workflow
tools must route through the bridge when bridge-only policy is active.

Required behavior:

- Pass MCP bridge configuration at launch.
- Disable or deny internal filesystem/shell/browser tools unless explicitly
  allowed by the caller.
- Surface bridge tool calls as stream chunks or tool-call events when possible.
- Do not parse pane redraws into live assistant-content chunks during
  generation. Terminal snapshots are the generation stream.

## Streaming Contract

The adapter may stream the provider TUI as a live terminal snapshot while a turn
is generating. This must use a terminal/screen-specific stream chunk, not normal
assistant content. The host UI can replace the previous terminal snapshot with
the latest one and keep it visible after the final response for debugging.

The final response must still come from parsed assistant output, not from the raw
terminal snapshot.

Interactive tmux adapters should not convert live pane snapshots into normal
assistant-content chunks during generation. The final response is parsed only
after the provider turn is complete, and that final parse must not include raw
terminal noise:

- spinner frames
- footer text
- shortcut hints
- repeated "calling tool" status lines
- box borders
- echoed user prompt
- compact/history notices
- tmux focus-events warnings

If no assistant text is available yet, the host application may show either the
terminal snapshot or a compact status such as "Agent is still working", but that
status is not a substitute for final assistant parsing.

The adapter's hard contract is:

- detect when the provider turn is complete
- extract the final assistant text for the unified completion
- if the tmux pane/server disappears, parse any last captured pane before
  failing; then unregister the owned session so later live input/resume attempts
  do not target stale tmux state

Cursor, OpenCode, or any future tmux-backed provider must set
`HandlesTmuxSessionLoss=true` in `coding_agent_contract.go` and add an
adapter-level test that simulates the tmux server/pane disappearing during a
turn. A contract flag without that adapter test is not enough for review.

The UI may show the raw terminal snapshot for progress/debugging; adapters must
avoid over-parsing terminal progress into assistant content.

## Terminal Size

Interactive tmux sessions should use an explicit pane size instead of tmux's
detached default. The default is `160x48`, which is closer to the desktop chat
terminal panel and avoids excessive wrapping in streamed snapshots.

Operators may tune this without code changes:

- `CODING_AGENT_TMUX_COLUMNS` controls pane columns. Default `160`.
- `CODING_AGENT_TMUX_ROWS` controls pane rows. Default `48`.

Adapters clamp values to a practical range so broken environment values do not
produce unreadable sessions.

## Login Shell Launch

Interactive tmux sessions start the provider CLI through the user's login shell
by default:

```sh
$SHELL -ilc 'cd "$1" || exit; shift; exec "$@"' coding-agent "$WORKING_DIR" <provider-cli> ...
```

This is required for desktop/DMG launches, where the backend process does not
inherit a Terminal tab's initialized environment. The login-shell launch gives
Claude Code, Codex CLI, and Gemini CLI the same `PATH`, shims, and exported
values the user normally gets from shell startup files.

Shell resolution order:

1. `CODING_AGENT_LOGIN_SHELL`
2. `SHELL`
3. macOS Directory Services `UserShell`
4. `/etc/passwd`
5. `/bin/zsh`, `/bin/bash`, `/bin/sh`

Supported shell families are POSIX-like shells (`zsh`, `bash`, `sh`, `dash`,
`ksh`) and `fish`. Unsupported or missing shells fall back to direct launch:
`cd "$WORKING_DIR" && exec <provider-cli> ...`.

Set `CODING_AGENT_SHELL_MODE=direct` to disable login-shell launch for
deployments where shell startup files are slow or unsafe for non-human
processes.

## Done Detection

A turn is done when the provider TUI is idle, not when the last text chunk was
seen.

The detector should combine:

- ready prompt visible
- no interrupt footer
- no active thinking/tool/calling progress line
- pane content stable for a short window
- provider-specific idle phrase if available

Provider hints:

- Claude Code: idle means a ready prompt is visible and `esc to interrupt` is
  gone.
- Codex CLI: idle means the Codex input prompt/footer is ready and no active
  running status is visible.
- Cursor CLI: idle means the Cursor Agent input prompt/footer is ready and no
  active thinking/running/editing/tool status is visible.
- Gemini CLI: idle means the TUI is ready for input, commonly including
  `Type your message`, with no active running state.

Never inject a final-answer marker into the prompt just to detect completion.

## Final Text Extraction

Final text extraction must use provider-native TUI structure when available:

- Claude Code: prefer the latest assistant block beginning with `⏺`.
- Codex CLI: prefer the assistant block framed by the long horizontal separator
  lines when present; otherwise fall back to the latest clean assistant segment.
- Cursor CLI: prefer the latest clean assistant segment after removing Cursor
  TUI chrome, tool/status lines, echoed user input, and old assistant replay.
- Gemini CLI: prefer the latest marked assistant block beginning with `✦`, `→`,
  or `->`; otherwise fall back to filtered visible assistant text.

The extracted final text must not include tool panels, shell output, footer
chrome, ready prompts, old assistant replay, or echoed user input.

## Live Follow-Up Input

If the user sends a message while a coding agent is still working, the runtime
must first try to send it to the registered tmux session.

Required behavior:

1. Look up `app_session_id -> tmux_session_name`.
2. Deliver the live message to the existing provider session or its adapter-level
   pending queue.
3. Do not start a duplicate provider run for the same app session.
4. Fall back to the generic steer queue only when no live tmux session exists.

Provider-specific behavior:

- Claude Code and Codex CLI may accept live input directly in the TUI.
- Gemini CLI 0.42 does not reliably process pasted follow-up input while a
  response is active. Its adapter must queue live input in-process, then submit
  queued messages with `Enter` when the Gemini ready prompt returns. Tests must
  fail if the message is only visible in the pane but never processed.

## Cancellation Contract

Cancellation must interrupt the TUI before cleanup.

Required behavior:

- Foreground cancellation sends the provider's interrupt key to the active tmux
  session.
- The adapter waits briefly for idle or process exit.
- Adapter-owned per-turn sessions are cleaned up after the turn exits.
- Persistent chat sessions are cleaned up only when the owner session is closed,
  the launch settings change, the idle timeout fires, or the server shuts down.

Cancellation for one app session must not kill a tmux session owned by a
different app session or a background agent.

## Multi-Turn Contract

Persistent interactive chat:

1. Start or reuse the tmux session for the app session id.
2. Launch the provider CLI with native system/MCP configuration.
3. Paste the current user message.
4. Stream assistant text and tool progress.
5. Detect idle.
6. Keep the session alive for follow-up input.
7. Reset idle cleanup after each completed turn.
8. On idle timeout, exit/interrupt gracefully, kill tmux, and unregister the
   session.

Per-turn/native resume flow:

1. Start a bounded provider invocation.
2. Send the latest user message.
3. Wait for completion.
4. Capture native session/thread/project ids.
5. Store provider-specific resume metadata.
6. Close the bounded invocation.
7. Resume the next turn with the latest user message plus native resume id.

Native resume metadata:

- Claude Code: `claude_code_session_id`, resumed with `--resume <uuid>`.
- Codex CLI: `codex_thread_id`, resumed with `codex exec resume`.
- Cursor CLI: `cursor_session_id` when available from Cursor-native session
  state, resumed with `cursor-agent --resume <chatId>` from the same workspace.
- Gemini CLI: `gemini_session_id` plus `gemini_project_dir_id`, resumed with
  `--resume <session_id>` from the same project dir.

On native resume, prefer sending only the latest user message when the provider
session/thread is proven to retain context. If a provider does not reliably
retain context in the current CLI build, the adapter must replay a bounded
prior-turn transcript while still reusing the same tmux/native session.

## Idle Cleanup

Persistent tmux sessions must not live forever.

Required behavior:

- Start an idle timer after each completed turn.
- Reset the timer when new input is accepted.
- On timeout, gracefully ask the TUI to exit when possible.
- Kill the tmux session if graceful exit does not complete.
- Remove the session from the live registry.

Provider-specific timeout env vars may exist for tests, but the product
contract is the same across providers.

## Testing Contract

Default tests must be deterministic and credit-free for pure parser and UI
normalization logic only. They should not install replacement provider binaries
or replacement `tmux` binaries.

Transport behavior must be validated by opt-in real CLI E2E tests, including:

- launch arguments, including model, approval mode, workspace, and provider
  config paths; the provider CLI cwd must match the MCP bridge shell cwd for
  the same app/session id
- system/developer prompt routing through the provider-native mechanism, never
  by concatenating those instructions into the pasted user message
- MCP config routing
- bridge-only tool policy routing, including denial/disablement of built-in
  filesystem/shell tools when required
- paste semantics for multiline text, blank lines, JSON, shell-looking text,
  quotes, markdown, unicode, and pasted blocks
- live input routing while the provider is active
- done detection from provider idle state
- cancel/interrupt behavior
- idle cleanup and server-shutdown cleanup
- no duplicate session creation for live follow-up input
- settings-change behavior: model, MCP config, tool policy, approval mode, or
  workspace root changes must close/recreate the tmux session; normal per-turn
  app system prompt variation must not close the session

Deterministic stream parsing tests must cover:

- provider startup banners are filtered
- shortcut/footer/status text is filtered
- spinner/progress frames are filtered or de-duplicated
- tool progress is de-duplicated
- raw MCP tool panels are not emitted as assistant content
- raw JSON tool results such as `stdout`, `stderr`, `exit_code`, and timing
  fields are not emitted as assistant content
- provider policy/admin warnings are filtered from user-visible assistant text
- echoed user prompts are filtered
- old assistant text still visible in the pane is not replayed on the next turn
- old assistant suffixes still visible in the pane are not replayed on the next
  turn
- final response parsing produces the user-visible completion without
  duplicating text from earlier turns
- terminal snapshot chunks are emitted as terminal/screen chunks, not assistant
  content chunks

Deterministic multi-turn tests must cover:

- turn 1 and turn 2 reuse the same tmux session for the same app session id
- providers that declare native context-only behavior send only the latest user
  message to the TUI on turn 2
- app-level E2E always validates the parsed final completion, not the raw event
  stream, so the token in the original prompt cannot create a false pass
- native-session memory, when claimed by a provider, is proven with a canary
  that cannot be satisfied by app-level history replay:
  - turn 1 says: `Take note of the word <TOKEN>. Do not save it to memory.`
  - turn 2 asks only: `What exact word did I ask you to take note of?`
  - turn 2's submitted prompt must not include turn 1 user text or assistant
    text
  - the returned answer must contain `<TOKEN>` and the provider session must be
    the same native tmux session
- prior assistant text from turn 1 is not pasted back as user input
- prior assistant text from turn 1 is not streamed or returned as part of turn 2
- stream deltas remain correct when the terminal redraw includes both old and
  new text
- a live follow-up sent during an active turn is pasted into the same tmux
  session and does not start a duplicate provider run

Deterministic resume tests must cover:

- per-turn mode captures the provider-native resume/thread/session metadata
- resume mode starts from the recorded provider metadata
- resume sends only the latest user message
- resume uses the same project/workspace directory when the provider requires
  it
- persistent tmux mode and per-turn native resume mode do not mix metadata
  incorrectly

Provider-contract validation must include real provider E2E. These tests stay
environment-gated so normal CI does not spend credits accidentally, but release
and transport-change validation should run them alongside deterministic tests:

- Claude Code: use Haiku unless explicitly testing another model.
- Codex CLI: use the cheaper contract model, currently
  `gpt-5.3-codex-spark`, unless explicitly testing another model.
- Gemini CLI: use the low tier, currently `gemini-3.1-flash-lite`, for the
  default smoke unless explicitly testing another tier.
- Cursor CLI: use the account/default selector (`cursor-cli`) for the default
  smoke unless explicitly testing a model available in that Cursor account.

Application-level chat E2E is required in addition to provider-adapter E2E.
The app-level test must drive the real `mcp-agent-builder-go` HTTP API because
that is where runtime capture, provider selection, session restoration, event
polling, and live `/steer` routing are wired together:

```sh
go run . test coding-agent-chat-e2e \
  --server-url http://localhost:<agent-port> \
  --provider gemini-cli \
  --model gemini-3.1-flash-lite \
  --selected-folder _users/default/Chats
```

The app-level E2E must fail if:

- `/api/query` silently falls back to a provider other than the requested
  provider/model
- turn 2 cannot recall a random token from turn 1 using the same app session id
- the live `/api/sessions/{session_id}/steer` message is only visible in the
  TUI draft but is never processed by the provider
- terminal/event polling completes without a parseable unified completion

Real tests should cover:

- one normal turn
- multi-turn in the same persistent tmux session
- live follow-up while the agent is active
- cancel/escape while the agent is active
- MCP bridge tool call
- resume after close
- large multiline user input
- large builder-style system prompt
- bridge-only policy with internal filesystem/shell tools disabled or denied
- useful assistant text streams before final completion when the provider emits
  it
- no old assistant text replay in streaming or final content across turns
- no raw TUI chrome, policy warnings, MCP panels, or JSON tool payloads in
  user-visible streaming or final content
- native web search, when the provider exposes `SearchWeb()`, including a
  streamed native tool-call event rather than only a final answer that could
  come from model memory

At least one real E2E per provider must combine the risky path in a single
scenario:

1. Start persistent tmux chat with a large builder-style system prompt.
2. Launch with MCP bridge config and bridge-only tool policy.
3. Send a large multiline first user message that requires one bridge tool call.
4. Assert useful stream chunks are clean and non-duplicated.
5. Wait for idle and assert final content is clean.
6. Send a second user message in the same tmux session.
7. Assert the second turn does not replay any first-turn assistant text.
8. Send live follow-up input while the provider is active.
9. Send cancel/escape while the provider is active.
10. Close or idle-clean the session, then resume when that provider supports
    native resume.

JSON/structured transports that remain available for legacy fallback or direct
provider regression tests must run equivalent stream hygiene tests for their
structured event path. Fixes for tmux parsing must not leave
`stream-json`/`--json` mode leaking policy warnings, tool payloads, or duplicated
assistant text.

Current Gemini CLI real contract command:

```sh
RUN_GEMINI_CLI_REAL_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli -run 'TestGeminiCLIRealInteractive|TestGeminiCLIInteractiveIntegrationFlashLite' -v -timeout 6m
```

Current Codex CLI real contract command:

```sh
RUN_CODEX_CLI_REAL_E2E=1 RUN_CODEX_CLI_INTERACTIVE_E2E=1 go test ./pkg/adapters/codexcli -run 'TestCodexCLIRealInteractive|TestCodexCLIInteractiveIntegrationSpark' -v -timeout 6m
```

Current Cursor CLI real contract command:

```sh
RUN_CURSOR_CLI_REAL_E2E=1 RUN_CURSOR_CLI_INTERACTIVE_E2E=1 go test ./pkg/adapters/cursorcli -run 'TestCursorCLIRealInteractive' -v -timeout 6m
```

Current legacy structured transport real contract commands:

```sh
RUN_CODEX_CLI_STREAM_JSON_E2E=1 go test ./pkg/adapters/codexcli -run 'TestCodexCLIRealExecJSON' -v -timeout 6m
RUN_GEMINI_CLI_STREAM_JSON_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli -run 'TestGeminiCLIRealStreamJSON' -v -timeout 6m
```

Current native web-search real contract commands:

```sh
RUN_CODEX_CLI_SEARCH_WEB_E2E=1 go test ./pkg/adapters/codexcli -run 'TestCodexCLIRealSearchWeb' -v -timeout 4m
RUN_GEMINI_CLI_SEARCH_WEB_E2E=1 GEMINI_API_KEY=<key> go test ./pkg/adapters/geminicli -run 'TestGeminiCLISearchWebSmoke' -v -timeout 4m
CLAUDE_CODE_ALLOW_LEGACY_PRINT=1 CLAUDE_CODE_TRANSPORT=print RUN_CLAUDE_CODE_SEARCH_WEB_E2E=1 go test ./pkg/adapters/claudecode -run 'TestClaudeCodeRealSearchWeb' -v -timeout 4m
RUN_CLAUDE_CODE_PRINT_INTEGRATION=1 go test ./pkg/adapters/claudecode -run 'TestClaudeCodeStreaming|TestRawClaude' -v -timeout 4m
```

Builder/workspace virtual-tool contract command:

```sh
WORKSPACE_API_URL=http://127.0.0.1:8081 go run . test search-web-llm-providers \
  --providers codex-cli \
  --models codex-cli=gpt-5.3-codex-spark \
  --provider-timeout 3m
```

Provider/model semantics:

- `auto` is a real CLI model selector and must be passed through as `auto`.
  Do not silently rewrite `auto` to `low`, `flash-lite`, or another tier.
- Cursor CLI is the exception: `cursor-cli` and `auto` are adapter sentinels
  that omit `--model`, because Cursor Agent CLI does not expose a documented
  `auto` model flag in the tmux path.
- Low/medium/high tiers are explicit aliases only when the user or test command
  asks for them.
- Search tests must assert a native tool-call path where the adapter exposes
  stream tool-call events. A memory-only final answer is not sufficient.

Current image-input real contract commands:

```sh
CLAUDE_CODE_ALLOW_LEGACY_PRINT=1 CLAUDE_CODE_TRANSPORT=print RUN_CLAUDE_CODE_IMAGE_E2E=1 go test ./pkg/adapters/claudecode -run 'TestClaudeCodePrintRealImageInput' -v -timeout 4m
RUN_CODEX_CLI_IMAGE_E2E=1 go test ./pkg/adapters/codexcli -run 'TestCodexCLIRealImageInput' -v -timeout 4m
```

Cursor CLI and Gemini CLI currently have negative image-input contract tests so
unsupported image parts fail clearly instead of being removed from the prompt.

Builder/workspace read-image virtual-tool contract command:

```sh
WORKSPACE_API_URL=http://127.0.0.1:8081 go run . test read-image-providers \
  --providers vertex,codex-cli,claude-code \
  --models vertex=gemini-3-pro-preview,codex-cli=gpt-5.4-mini,claude-code=claude-code \
  --image-path _users/default/Chats/misc-topic/google.png \
  --expect-any google \
  --provider-timeout 3m
```

Read-image provider/model semantics:

- Claude Code image input is supported only on the `print`/`-p` path today.
  Tmux chat must reject image parts until a reliable TUI attachment transport is
  implemented.
- Codex CLI image input must use an image-capable model, currently
  `gpt-5.4-mini` for the contract test. `gpt-5.3-codex-spark` is not a valid
  image-read contract model.
- Gemini CLI must reject `llmtypes.ImageContent` explicitly until its adapter
  supports a real image attachment path.

Current image-generation real contract commands:

```sh
go run ./cmd/llm-test codex-cli-image-generate \
  --model codex-cli \
  --prompt "A simple red square icon centered on a white background" \
  --aspect-ratio 1:1 \
  --num-images 1 \
  --output-dir /tmp/mlp-codex-image-gen

GEMINI_API_KEY=<key> go run ./cmd/llm-test vertex-imagen-generate \
  --model gemini-3.1-flash-image-preview \
  --prompt "A simple red square icon centered on a white background" \
  --aspect-ratio 1:1 \
  --num-images 1 \
  --output-dir /tmp/mlp-vertex-image-gen
```

Builder/workspace image-generation virtual-tool contract command:

```sh
WORKSPACE_API_URL=http://127.0.0.1:8081 go run . test image-gen-providers \
  --providers vertex,codex-cli \
  --models vertex=gemini-3.1-flash-image-preview,codex-cli=codex-cli \
  --provider-timeout 4m
```

Image-generation provider/model semantics:

- Only Vertex and Codex CLI are required for the current real contract.
- For Codex CLI image generation, `codex-cli` is the no-model sentinel. The
  adapter must not pass `--model` unless a concrete Codex model was explicitly
  requested.
- Image-generation test commands must fail on missing auth, provider errors,
  zero returned images, empty image bytes, or saved bytes that do not detect as
  `image/*`.
- The builder command must verify that `image_gen` saves the generated image
  back to the workspace and that the saved file downloads as `image/*`.
- Codex CLI image generation should use `gpt-5.4-mini` for the contract test.
  Spark can be tested separately, but it is not the release gate for image
  generation or image understanding.

## Related Docs

- `docs/CODING_AGENT_TRANSPORT_PATTERNS.md`
- `docs/CODEX_CLI_CODING_AGENT_CONTRACT.md`
- `docs/GEMINI_CLI_CODING_AGENT_CONTRACT.md`
