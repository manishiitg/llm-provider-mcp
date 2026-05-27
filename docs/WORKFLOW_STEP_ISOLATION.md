# Workflow Step Isolation (Per-Step Tmp Dir)

Proposal for isolating coding-CLI workflow steps from each other and from
the user's actual workflow directory by running each step in a fresh
per-call tmp dir. Chat (multi-agent + builder) continues to use the
user's workspace dir directly.

Status: **design proposal, not yet implemented.** Awaiting alignment
before code.

## 1. Why

### 1.1 The collision problem we have today

All coding-CLI sessions — chat AND workflow steps — currently run with
`CodingAgentWorkingDir` set to the user's workspace dir. The
WithWriteProjectInstructionFile pattern (shipped earlier this week)
projects per-session files into that dir:

- Claude Code: `CLAUDE.md`, `.mcp.json` (gated)
- Codex:       `AGENTS.md`, `.codex/config.toml`
- Cursor:     `.cursor/rules/mlp-system.mdc`, `.cursor/mcp.json`, `.cursor/hooks.json`
- Gemini:     `GEMINI.md`, `.gemini/settings.json`, `.gemini/hooks/...`
- OpenCode:   `AGENTS.md`, `opencode.jsonc`, `.opencode/plugins/...`
- Antigravity: `.agents/rules/mlp-system.md`, `.agents/mcp_config.json`, `.agents/hooks.json`

All instruction-file paths are now fixed-name single-file conventions
(`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, `mlp-system.mdc/md`) under the
"one chat owns the workdir at a time" assumption. The same single-file
race exists for the MCP / hooks files alongside.

When two workflow steps run concurrently (or back-to-back in
persistent-session mode) against the same workspace:

1. **Single-file race**: step A writes `AGENTS.md`, step B writes
   `AGENTS.md` mid-flight. Last-write-wins; the earlier step's
   content vanishes.

2. **Byte-restore corruption**: step A captures pre-existing
   operator content into its restore registry. Step B then writes
   over the file. Step A's cleanup runs first and "restores" — but
   the bytes it captured may already be step B's content, not the
   operator's original. Restore order matters and gets it wrong.

3. **Cross-CLI confusion**: step A uses Claude (drops `.claude/`),
   step B uses Codex (drops `AGENTS.md`). Steps don't collide
   directly, but the workspace ends up with stale config from both
   agents that the next step sees and tries to interpret.

4. **Crash window**: if the orchestrator process dies between write
   and cleanup, operator-owned single-file content (`AGENTS.md`,
   `GEMINI.md`) is destroyed. This is the documented caveat behind
   `MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS` for claude `.mcp.json`
   and (previously, now removed) codex `.codex/hooks.json`.

### 1.2 Why chat doesn't share this problem

Chat sessions:

- One user, one workspace, one session at a time per workspace
- Long-lived persistent sessions (resume tied to dir)
- User EXPECTS the agent to edit files in their actual workspace
  (the whole point of running a coding assistant)

Workflow steps:

- N steps, often concurrent, against the same workflow dir
- Stateless per step (no resume needed across steps)
- Steps should NOT directly edit the workflow dir's actual files;
  the MCP bridge is the orchestration contract for file changes

## 2. The Decision Tree

| Use case | Working dir | Resume? | Action |
|---|---|---|---|
| 1. Multi-agent chat | User's workspace | Yes (native CLI resume) | **No change** |
| 2. Builder chat | User's workspace | Yes | **No change** |
| 3. Workflow steps | Per-step tmp dir | **No** | **New behavior: tmp-dir isolation** |

The orchestrator already knows which mode it's running in. The new
mcpagent option flags the call as "isolated workspace required."

## 3. Public API Shape

### 3.1 New mcpagent agent option

```go
// WithIsolatedSessionWorkspace asks the coding-CLI session to run in a
// fresh per-call tmp dir instead of CodingAgentWorkingDir. When set:
//
//   - The agent creates a new os.MkdirTemp("", "mlp-cli-session-*") dir
//     before launching the CLI session.
//   - The CLI's cwd / --dir / --cd / WithXxxWorkingDir is overridden to
//     that tmp path.
//   - The tmp dir is rm -rf'd when the session completes (success or
//     failure).
//   - The MCP bridge config (which already carries the actual workflow
//     dir paths in its env / args) is unchanged — the bridge subprocess
//     runs outside the CLI's sandbox so it can still touch the user's
//     workflow dir for file ops the model invokes via bridge tools.
//
// Intended for workflow-step calls where resume is never needed and
// pollution + concurrent overlap is a real risk. Chat calls should not
// set this; they need persistent state on the user's actual workspace.
func WithIsolatedSessionWorkspace(enabled bool) AgentOption {
    return func(a *Agent) {
        a.IsolatedSessionWorkspace = enabled
    }
}
```

### 3.2 Agent struct field

```go
type Agent struct {
    // ...
    CodingAgentWorkingDir       string
    IsolatedSessionWorkspace    bool  // NEW
    // ...
}
```

### 3.3 Workflow orchestrator wires it

In `mcp-agent-builder-go/agent_go/pkg/orchestrator/agents/workflow/step_based_workflow/...`
or equivalent, where a step's agent is built, append:

```go
agentOpts := []mcpagent.AgentOption{
    // ... existing options ...
    mcpagent.WithIsolatedSessionWorkspace(true),
}
```

Chat code paths in `mcp-agent-builder-go/agent_go/pkg/agentwrapper/llm_agent.go`
do NOT set this option.

## 4. Per-CLI Implementation

The isolation logic lives in mcpagent's option-appending layer (e.g.
`appendCodingAgentWorkingDirOptionForProvider` or a new sibling),
NOT in each adapter. The pattern:

1. If `IsolatedSessionWorkspace` is true:
   - Create `tmpDir := os.MkdirTemp("", "mlp-cli-session-*")`
   - Override the working-dir option for the active provider with
     `tmpDir`
   - Register a deferred cleanup: `defer os.RemoveAll(tmpDir)` at the
     call-site that owns the session lifecycle

2. Provider-specific options for sandbox confinement (extra defense
   in depth):

| CLI | Sandbox flag | Effect |
|---|---|---|
| Claude Code | `--permission-mode dontAsk` (already set) | Already permit-all within workspace |
| Codex | `--sandbox workspace-write` | Confines apply_patch + shell to cwd (tmp dir). Outside writes blocked. |
| Cursor | `--force` (already set) | Already permit-all |
| Gemini | `--approval-mode yolo` (already set) | Already permit-all |
| OpenCode | `--dangerously-skip-permissions` (already set) | Already permit-all; tools-deny block separately limits builtins |
| Agy | `--sandbox workspace-write` | Same as codex |

For codex specifically: setting `--sandbox workspace-write` AND
`cwd = /tmp/foo` means even if the model invokes `apply_patch`
against `/Users/x/workflow/main.go`, codex's sandbox rejects the
write because the path is outside cwd. **This is the security
property we want**: the model has NO way to mutate the user's
workflow dir except via the MCP bridge.

## 5. What Goes Away (or Becomes Simpler)

When `IsolatedSessionWorkspace` is true:

- **Byte-restore complexity**: every file in the tmp dir is ours.
  `rm -rf tmp` is the entire cleanup. No restore registry, no
  pre-existing-content capture, no LIFO ordering.

- **`MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS` gate becomes
  unnecessary for workflow mode**: the .mcp.json discovery prompt
  (claude) and hook trust prompt (codex) still fire, but they fire
  in a FRESH tmp dir — no operator content to destroy, so the
  "unsafe" framing doesn't apply. The auto-dismiss code in
  `waitForCodexPrompt` can stay for cases where the prompt slips
  through.

- **Codex hooks projection can return**: the reason we removed it
  (trust prompts + apply_patch can't be CLI-disabled) is moot when
  the workspace is fresh and the SHA is constant. Codex's
  per-SHA trust cache will hit after the first ever invocation in
  the user's `~/.codex/config.toml`. We could even pre-write the
  `[hooks.state]` block (the task #31 follow-up I deferred) and
  have zero prompts ever.

- **All the file-collision risk goes away** by construction. Two
  steps with different tmp dirs literally cannot collide.

What STAYS the same:

- The chat code path (multi-agent + builder) is untouched.
- Per-CLI byte-restore code is still needed for chat — operators
  WILL have pre-existing files in their workspace and the
  projection must restore them.

## 6. Test Plan

### 6.1 New unit tests

- `TestAgentWithIsolatedSessionWorkspaceCreatesAndCleansUpTmpDir`:
  the option creates a tmp dir, passes it through as the cwd
  option, and rm -rf's it after the call.
- `TestAgentWithIsolatedSessionWorkspaceDoesNotTouchCodingAgentWorkingDir`:
  pre-seed `CodingAgentWorkingDir` with files, confirm none are
  touched (read, written, or deleted) when isolation is on.

### 6.2 Updated existing tests

- Project-artifacts E2Es (claude/codex/gemini/opencode) currently
  pre-seed operator content in `t.TempDir()` and verify
  byte-restore. With isolation on, byte-restore wouldn't trigger
  (no operator content in the fresh tmp dir). Add a parallel test
  per CLI that exercises the isolation path: `RealProjectArtifactsLifecycleIsolated`.

### 6.3 New workflow-level E2E

- `TestWorkflowStepIsolationPreservesUserWorkflowDir`: run a
  workflow step against a workspace dir containing operator files
  (`README.md`, `.cursor/mcp.json` with operator content, etc.).
  Assert NONE of those files have been modified or touched after
  the step completes. The model can only see them via MCP bridge.

### 6.4 Concurrent-step regression test

- `TestConcurrentWorkflowStepsDoNotCollide`: fire two workflow
  steps against the same `CodingAgentWorkingDir` in parallel.
  Each step writes a distinct sentinel to `AGENTS.md` (within its
  tmp dir, so they don't collide). Assert both finish without
  error, both tmp dirs are gone afterwards, and the user's actual
  workspace's `AGENTS.md` (if any) is untouched.

## 7. Open Questions

1. **Cache location**: `os.MkdirTemp("", ...)` puts it in
   `/var/folders/...` (macOS) or `/tmp` (Linux). Workflow steps are
   short-lived, so OS cleanup-on-reboot is fine. But if a step ever
   needs to persist artifacts past the call (debug archive,
   conversation log), we'd need a longer-lived location. Decision:
   `os.MkdirTemp` is fine for v1. Reconsider if any step needs
   persistence.

2. **Cleanup on adapter crash**: if the mcpagent process crashes
   between tmp-dir creation and cleanup, the tmp dir leaks. Tmp
   dirs are OS-cleaned eventually but until then they accumulate.
   Mitigation: a startup scan of `os.TempDir()` for stale
   `mlp-cli-session-*` dirs older than 1h, with rm -rf. Add to
   adapter startup.

3. **MCP bridge env passing**: the bridge needs to know the user's
   actual workflow dir for its file-read/write tools. Currently
   `WithMCPConfig` carries this. Need to confirm: when we override
   the CLI's cwd to tmp dir, the bridge config (which is built
   BEFORE the override) still carries the original workflow dir.
   Should be fine because the bridge is a subprocess spawned by
   the CLI; the CLI doesn't filter env to the bridge.

4. **Symlink trickery**: should the tmp dir contain a symlink to
   the workflow dir for convenience? E.g.
   `tmp/workspace -> /Users/x/workflow`. Operators inspecting the
   running session can poke around. Trade-off: symlinks can
   confuse the model (it sees both `tmp/` and `tmp/workspace/`).
   Decision: NO symlinks. Tmp dir is opaque; MCP bridge is the
   only path to user files.

5. **Persistent-tmux-session workflow steps**: do we want
   persistent sessions for workflow steps? If yes, the tmp dir
   needs to live for the session lifetime, not the call lifetime.
   Currently workflow steps default to NON-persistent
   (`CodexPersistentInteractiveSession=false`,
   `CursorPersistentInteractiveSession=false`, etc.). Decision:
   leave the default; if a workflow step opts into persistent
   mode, isolation lives for the session and is cleaned up at
   session teardown (matches the existing
   `projectInstructionCleanup` pattern).

## 8. Rollout Order

1. **Design alignment** (this doc, awaiting sign-off) — ✅ done
2. **Prototype on cursor** — has clean deny coverage + shared-dir
   lease test for regression catching — ✅ done (Phase A in
   mcpagent commit 6032608, Phase B in
   multi-llm-provider-go commit 955f40e)
3. **Workflow orchestrator wire-through**: flip the flag for all
   workflow-step code paths in mcp-agent-builder-go — ✅ done
   (commit d438a5b1)
4. **Update WORKSPACE_PROJECTIONS.md**: document the chat-vs-workflow
   split explicitly. Note that several "risks" called out there go
   away in workflow mode.
5. **Pin sandbox=workspace-write for codex** in mcpagent's codex
   integration path so apply_patch (which codex has no `--disable`
   flag for) is confined to cwd. Combined with the tmp-dir cwd from
   Phase 3, this closes the apply_patch gap structurally — no hooks
   needed. — ✅ done
6. **Template Phase 2's cursor isolation E2E to other 5 CLIs** as
   regression coverage.

### ~~Dropped~~ — Re-add codex hooks projection for workflow mode

The earlier proposal was: now that the tmp dir is fresh per step,
codex's per-SHA hook trust cache would hit consistently, so we
could safely re-add the `.codex/hooks.json` + deny script
projection (which was removed because of trust-prompt flakiness).
The goal was closing the `apply_patch` deny gap — codex's
`--disable <feature>` flag list does not include `apply_patch`, so
hooks were the only deny lever.

**Dropped because the tmp-dir + sandbox combination closes the gap
structurally without re-introducing hooks**:

- Phase 3 makes cwd a fresh `/tmp/mlp-cli-session-*` per step.
- Codex's `--sandbox workspace-write` (now explicitly pinned in
  Phase 5) confines BOTH `shell` AND `apply_patch` writes to cwd.
- So `apply_patch` can only write to the tmp dir, never the user's
  workflow dir.
- Hooks are no longer necessary for the security property they
  would have provided.

Re-adding hooks would add per-SHA trust-state file management,
write coordination with `~/.codex/config.toml`, and the (admittedly
shrunken) risk surface of misconfigured deny scripts — for zero
incremental safety. Better to leave the hooks projection removed
and rely on the structural confinement.

## 9. Non-Goals

- This is NOT a permission-system redesign. Existing
  WithDenyBuiltinTools / WithBridgeOnlyTools options continue to
  apply when set; isolation is orthogonal.
- This does NOT change chat behavior. Chat users still get the
  "agent edits my files directly" experience.
- This does NOT replace the MCP bridge. The bridge is the
  orchestration contract for file changes; this just narrows where
  the CLI can write to enforce that contract.
