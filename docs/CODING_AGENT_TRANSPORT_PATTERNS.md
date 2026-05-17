# Coding Agent Transport Patterns

This is the shared pattern set for terminal-native coding agents such as Claude
Code, Codex CLI, Gemini CLI, and Kimi CLI.

## 1. One Normal Transport

Interactive chat and workflow execution use the same tmux-backed coding-CLI
transport.

- Chat: persistent tmux TUI, with live follow-up input routed into the same
  owner session.
- Workflow: the same tmux TUI transport, with the orchestrator deciding when to
  send intermediate input, wait for idle, extract the final response, and close
  or retain the owner session.
- Legacy structured transports (`claude -p`, `codex exec --json`,
  `gemini --output-format stream-json`, `kimi --print --output-format
  stream-json`) are fallback/test-only paths unless a provider still lacks a
  tmux implementation.

Do not add product behavior that depends on structured CLI output unless the
tmux contract cannot support that capability yet.

## 2. Owner Session Registry

Long-lived TUI sessions are keyed by the application session id:

```text
app_session_id -> tmux_session_name
```

The app session id is the public routing key. The tmux session name is an
implementation detail and should not leak into UI or workflow terminology.

## 3. One Live TUI Per Owner

For persistent chat, reuse the existing tmux session for that owner id.

- Hold a per-session mutex while a turn is being submitted and parsed.
- Keep the session registered after the turn returns to idle.
- Close it only on explicit cleanup or idle timeout.
- If launch settings change, close the existing session before starting a new
  one.

## 4. Native Instructions, Pasted User Input

System/developer instructions must use the provider's native mechanism. User
input must be pasted into the TUI.

- Claude Code: system prompt file plus tmux paste for the user message.
- Codex CLI: `developer_instructions` config override plus tmux paste for the
  user message.
- Gemini CLI: `GEMINI_SYSTEM_MD` plus tmux paste for the user message.

Never concatenate system text into the pasted user prompt. This prevents bugs
where the agent sees empty or malformed user input.

## 5. MCP Bridge Is The Tool Surface

Coding agents should call our MCP bridge, not their internal local tools, when
the runtime wants policy-controlled workflow tools.

- Claude Code: pass `--mcp-config`, `--strict-mcp-config`, and disable internal
  tools unless explicitly allowed.
- Codex CLI: pass MCP config overrides and disable `shell_tool` when required.
- Gemini CLI: pass project settings/policies and deny built-in filesystem/shell
  tools when required.

This keeps provider-native terminal UX while preserving our bridge and policy
boundaries.

## 6. Done Means Idle, Not Last Text

Terminal output is noisy. A turn is done only when the TUI is idle.

Signals to combine:

- ready prompt visible
- no interrupt footer
- no active thinking/tool progress line
- pane content stable for a short window

Never rely on a final-answer marker injected into the prompt.

## 7. Stream Terminal Snapshots Separately

For interactive chat, providers may emit the full visible tmux pane as a live
terminal snapshot. This is useful for showing native TUI progress without trying
to parse every redraw.

- Emit terminal snapshots as a terminal/screen stream chunk.
- Do not append terminal snapshots to assistant-content chunks.
- Parse the final unified answer separately from the terminal pane.
- Let the host UI replace prior terminal snapshots and keep the last snapshot
  visible after the final answer when useful for debugging.

## 8. Live Input First, Queue Fallback Second

When the user sends a message during an active coding-agent turn:

1. Try to paste it into the registered tmux session.
2. If no session exists, fall back to the normal agent steer queue.

The fallback is for non-coding providers and early/late races. It should not
start a duplicate coding-agent run while the TUI is already active.

## 8. Cancellation Interrupts The TUI

Context cancellation should first interrupt the provider TUI, then clean up
owned sessions when appropriate.

- Foreground request cancellation sends Escape or Ctrl-C to the TUI.
- Server shutdown drains adapter-owned sessions.
- Background agents own their own lifecycle; unrelated cancellation should not
  kill sessions that belong to a different owner.

## 9. Idle Cleanup Is Ownership Cleanup

Persistent sessions are not permanent.

- Keep the session alive for follow-up chat.
- Reset the idle timer after every completed turn.
- On idle timeout, exit/interrupt gracefully and kill the tmux session.
- Remove registry entries during cleanup.

## 10. Test Both Contract And Reality

Default tests should be credit-free and deterministic for parser and UI
normalization logic only. They should not install replacement provider binaries
or replacement `tmux` binaries.

Provider-contract validation should include real provider E2E. These tests are
environment-gated so normal CI does not spend credits accidentally:

- Claude Code with Haiku
- Codex CLI with the cheaper contract model, currently `gpt-5.3-codex-spark`
- Gemini CLI low tier, currently `gemini-3.1-flash-lite`
- Kimi CLI with `kimi-code` in no-tools stream-json mode
- multi-turn memory in the same persistent tmux session

This gives fast CI coverage for the contract and occasional real validation for
provider TUI changes.
