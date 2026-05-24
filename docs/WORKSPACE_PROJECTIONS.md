# Workspace Projections

Optional, per-session projection of orchestrator-supplied configuration into
each coding CLI's project-conventional on-disk format. Pairs with the existing
`-c`-flag / `--mcp-config` / env-var injection paths — the projections are
*additive workspace visibility*, not a replacement for the primary injection.

Owned by `WithWriteProjectInstructionFile(enabled bool)` on each adapter; OFF
by default; byte-restore of any pre-existing operator content at session
teardown.

## 1. Why project at all

The orchestrator already injects every supported configuration through CLI
flags or env vars:

- system prompt → `--system-prompt-file` / `GEMINI_SYSTEM_MD` / `-c model_instructions_file` / in-prompt prefix
- MCP servers → `--mcp-config` / `-c mcp_servers.*` / `--include-directories`
- deny hooks → `WithDenyBuiltinTools` (cursor) / `WithBridgeOnlyTools` (agy) /
  per-CLI hook config dropped by the adapter

These primary paths work and are the load-bearing mechanism. The workspace
projection is useful when:

- A downstream tool reads the on-disk convention directly (linters,
  IDE plugins, other agents inspecting the workspace).
- An operator auditing a running session wants to see the prompt /
  MCP config / hooks at the conventional path without spelunking
  through tmux capture or temp files.
- A CLI's hook system *only* loads from on-disk config (opencode hooks
  require a plugin file; no in-flag path exists).

Off by default because workspace writes carry a non-zero risk surface
(crash-window during write/restore, discovery prompts on some CLIs).

## 2. The Matrix

What lands in `<workingDir>` when `WithWriteProjectInstructionFile(true)` is
set, for each CLI. *Safe* drops are unconditional under the flag; *Unsafe*
drops require also setting `MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS=1` —
see [Section 4](#4-the-mlp_enable_unsafe_workspace_projections-gate).

| CLI         | Instruction file                            | MCP config                                      | Deny-builtin hook                                                     |
| :---------- | :------------------------------------------ | :---------------------------------------------- | :-------------------------------------------------------------------- |
| Claude Code | `.claude/rules/mlp-session-<hex>.md`        | `.mcp.json` *(unsafe)*                          | n/a (use `--allowed-tools` / `--disallowed-tools`)                    |
| Codex       | `AGENTS.md`                                 | `.codex/config.toml` ([mcp_servers.*])          | `.codex/hooks.json` + `.codex/hooks/deny-builtin.sh` *(unsafe)*        |
| Gemini      | `GEMINI.md`                                 | `.gemini/settings.json` (merged with hooks)     | `.gemini/settings.json` `hooks.BeforeTool` + `.gemini/hooks/deny-builtin.sh` |
| OpenCode    | `AGENTS.md`                                 | `opencode.jsonc` (`"mcp"` key, separate option) | `.opencode/plugins/deny-builtin.js` (ES-module plugin)                |
| Cursor      | `.cursor/rules/mlp-session-<hex>.md`        | `.cursor/mcp.json`                              | `.cursor/hooks.json` + `.cursor/hooks/mlp-deny-builtin.sh`            |
| Antigravity | `.agents/rules/mlp-system-<hex>.md`         | `.agents/mcp_config.json`                       | `.agents/hooks.json` (deny entries inline)                            |

### Notes per CLI

- **Claude Code's `.claude/rules/`** is a multi-file directory: every
  `.md` is loaded as a project rule. Unique-per-session hex suffix means
  concurrent orchestrator sessions in the same workspace don't collide,
  and operator-owned files at `.claude/rules/*.md` are never touched.
- **Cursor's `.cursor/rules/`** has the same multi-file semantics.
- **Antigravity's `.agents/rules/`** ditto.
- **All other instruction files are single-file conventions**
  (`AGENTS.md`, `GEMINI.md`). Byte-restore captures any pre-existing
  operator content and writes it back at session teardown. If the
  orchestrator process crashes between write and restore, the operator's
  file is destroyed — single-file conventions inherently carry this
  crash-window risk.

## 3. Lifecycle

For every projected file, the adapter:

1. **Read pre-existing content** at the target path. If the file
   exists, capture its bytes into a per-session restore registry. If
   it doesn't, record "no prior".
2. **Write the orchestrator content** to the path.
3. **Run the session.** The CLI sees the on-disk content alongside whatever
   the primary injection path already provided.
4. **At teardown**, restore: if there was prior content, write it back
   byte-for-byte. Otherwise, `os.Remove` the file we created. Empty
   directories we created (`.codex/hooks/`, `.gemini/hooks/`) are removed
   on a best-effort basis.

Teardown runs from three places — all three must honor the restore
registry, or the byte-restore promise breaks:

- The normal end-of-session path (`closeXxxPersistentSession`).
- The error path during launch (`cleanupFailedXxxInteractiveSession`).
- The process-wide global cleanup (`CleanupXxxCLIInteractiveSessions`),
  called from test teardown and graceful shutdown.

A bug in commit `3fff052` was that codex's global cleanup path
skipped the new field; it's wired in all three paths now.

## 4. The `MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS` gate

Two specific drops trigger startup prompts on real CLI binaries that the
tmux adapter cannot dismiss. Both are disabled by default and gated
behind this env var:

### Claude `.mcp.json`

Dropping `<workingDir>/.mcp.json` triggers Claude Code v2.1.150's
"New MCP server found in `.mcp.json` — approve?" interactive prompt at
startup. The tmux adapter cannot dismiss it; the session times out.

**Attempted fixes that don't work on v2.1.150:**

- Pre-populating `~/.claude.json` `projects.<dir>.enabledMcpjsonServers`
  with the server names. The field exists, gets written, but Claude
  still shows the prompt.
- Also populating `projects.<dir>.mcpServers` with the full server
  config (so there's no diff between disk and known state). Same
  outcome.
- Using `--strict-mcp-config` so Claude ignores everything except
  `--mcp-config` files. The discovery prompt still fires because Claude
  *sees* `.mcp.json` even when it would have ignored its contents.

The `--mcp-config <temp>` + `--strict-mcp-config` flags already load
the MCP servers without triggering the prompt, so disabling this
projection loses only the workspace-visibility belt-and-suspenders,
not the actual MCP loading.

### Codex `.codex/hooks.json`

Dropping `<workingDir>/.codex/hooks.json` triggers codex v0.131.0's
hook trust review screen ("⚠ 1 hook needs review before it can run").
The documented `--dangerously-bypass-hook-trust` flag *is* recognized
(codex prints "flag is enabled. Enabled hooks may run without review")
but does NOT auto-dismiss the visual review screen in interactive
mode — only the trust check on hook *execution*. The tmux adapter
blocks waiting for ready state.

Disabling this projection removes the deny-builtin lever from codex.
The trade-off is acceptable because the alternative (a session that
always times out) is strictly worse.

### Opting back in

Set the env var when you have a way to dismiss the prompts:

```bash
export MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS=1
```

Examples of "a way to dismiss":

- A custom post-launch `tmux send-keys` that sends Enter or `t` after
  detecting the prompt's distinctive output.
- A newer CLI version that suppresses the prompt with the existing
  flag.
- A non-interactive call path (`-p` / `exec` mode) where the prompt
  is skipped automatically.

## 5. What's NOT gated

These drops have been confirmed to NOT trigger discovery prompts on
their live CLIs and ship enabled-by-default under the flag:

- **Cursor's `.cursor/mcp.json`** — cursor handles this via
  `--approve-mcps` which auto-accepts the in-TUI consent dialog.
- **Cursor's `.cursor/hooks.json`** — cursor reads silently.
- **Agy's `.agents/mcp_config.json`** + `.agents/hooks.json` — antigravity
  reads silently.
- **Gemini's merged `.gemini/settings.json`** — gemini accepts the
  hooks block without a review screen, even with `hooks.BeforeTool`
  entries. (Notable: gemini behaves better here than codex despite
  using the same hook event family.)
- **OpenCode's `.opencode/plugins/deny-builtin.js`** — opencode
  auto-loads anything in `.opencode/plugins/`, no discovery prompt.
- **Claude's `.claude/rules/mlp-session-<hex>.md`** — Claude's
  per-rule loading silently picks up new files.

## 6. Test coverage

### Unit (every adapter)

- Lifecycle tests for each helper: fresh-workspace write+remove,
  pre-existing-operator-content write+byte-restore, empty-workingDir
  no-op.
- Format invariants: codex TOML emitter (key ordering, bare vs quoted
  keys, env subtable), gemini hooks matcher covers documented tool
  names, opencode plugin uses `tool.execute.before`.
- Gate enforcement: `TestWriteCodexProjectArtifactsHooksGatedByEnvVar`
  exercises both branches of the unsafe gate.

### Real-CLI E2E (gated per CLI)

| CLI         | Env var                                            | Status |
| :---------- | :------------------------------------------------- | :----- |
| Claude Code | `RUN_CLAUDE_CODE_EXPERIMENTAL_INTEGRATION=1`       | PASS   |
| Codex       | `RUN_CODEX_CLI_REAL_E2E=1`                          | PASS   |
| Gemini      | `RUN_GEMINI_CLI_REAL_E2E=1` + `GEMINI_API_KEY`     | PASS   |
| OpenCode    | `RUN_OPENCODE_CLI_REAL_E2E=1`                       | PASS   |
| Cursor      | `RUN_CURSOR_CLI_REAL_E2E=1`                         | covered by older `cursorcli_deny_builtin_hooks_test.go` |
| Antigravity | `RUN_AGY_CLI_REAL_E2E=1`                            | PASS (`TestAgyCLIRealSystemPromptRulesContract`) |

Each lifecycle E2E pre-seeds an operator-owned file, runs a real
adapter call with the flag on, force-cleans up if persistent-session
mode is in use, and asserts:

- Operator file content is byte-for-byte restored.
- mtime advanced past the pre-seed (proves the writer fired
  mid-session — without this assertion, byte-restore could pass via
  the null hypothesis "writer never ran").
- For codex specifically: `.codex/hooks.json` was NOT created
  (proves the unsafe-projection gate is honored).

### What's NOT covered yet

- **Behavioral deny-hook verification.** No test proves that prompting
  the CLI to invoke a built-in tool actually results in the deny hook
  firing and the tool call getting vetoed. The lifecycle tests cover
  "the file we wrote lands in the right place with the right shape";
  the missing piece is "the CLI honors the config we drop." This is
  flaky to test (model decides whether to invoke a built-in) and is
  the next gap if behavioral coverage becomes important.
- **Cross-CLI plugin-format validation.** Our opencode deny-plugin
  source was written against the implied shape from the `.env`
  example in `opencode.ai/docs/plugins` — the default-export + record
  shape has not been independently verified against opencode's SDK
  type definitions.

## 7. Adding a new CLI

Follow the established shape:

1. Add a `MetadataKeyWriteProjectInstructionFile` constant to the
   adapter's `options.go`.
2. Add `WithWriteProjectInstructionFile(enabled bool)` option function.
3. Add a `writeXxxProjectArtifacts` helper that composes individual
   writers per artifact. Each individual writer captures any
   pre-existing operator content into a per-session restore registry
   and returns a cleanup func.
4. Compose all individual cleanups into a single composite cleanup
   stored on the session struct (`projectInstructionCleanup func()`).
5. Wire the composite cleanup into all teardown paths:
   `closeXxxPersistentSession`, `cleanupFailedXxxInteractiveSession`,
   `CleanupXxxCLIInteractiveSessions`. **Missing any one of these is
   a latent destroy-operator-content bug.**
6. Add a public re-export `WithXxxWriteProjectInstructionFile` in
   `providers.go`.
7. Add unit tests for each writer (fresh + restore + empty no-op) and
   one composite lifecycle test.
8. Add a real-CLI E2E gated by `RUN_XXX_CLI_REAL_E2E=1` that
   pre-seeds, runs, force-cleanups if persistent, and asserts
   byte-restore + mtime advance.
9. If the CLI shows a startup prompt on launch with the projection
   in place, gate the problematic drop behind
   `MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS` and document why in the
   adapter comment and in [Section 4](#4-the-mlp_enable_unsafe_workspace_projections-gate)
   above.
10. Update [Section 2](#2-the-matrix) and [Section 6](#6-test-coverage)
    of this doc.
