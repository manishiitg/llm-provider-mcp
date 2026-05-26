# Antigravity CLI Pending Items

Status: `agy-cli` may be exposed as an alpha local CLI provider, but must not be
promoted as a production CLI provider yet.

This file tracks the known gaps before Antigravity CLI should be treated as a
fully supported coding-agent provider across `multi-llm-provider-go`,
`mcpagent`, and `mcp-agent-builder-go`.

## Pending Before Production

- [ ] Verify workspace-scoped MCP isolation.
  - Two Agy sessions in separate working dirs must not see each other's bridge
    config or tools.
  - Shared workdir behavior with different concurrent MCP configs is currently
    fail-closed, not certified as isolated.

- [x] Keep Agy marked alpha until broader certification gaps are resolved.
  - Chat, tmux, live input, native resume, system rules, and workspace MCP
    config writing are working. A real MCP bridge tool call is now certified,
    but other tmux contract gaps remain.
  - Agy may be visible in provider setup as `Antigravity CLI (Alpha)`, but the
    contract remains local-sign-in, tmux-only, and not structured-JSON.

- [ ] Finish remaining certification gaps.
  - Known gaps are tracked in `coding_agent_contract_test.go` under
    `knownCertificationGaps[ProviderAgyCLI]`.
  - Native resume after tmux loss has a real E2E, and system prompts are now
    written as workspace rules.
  - Shared-workdir MCP isolation coverage still needs to be drained.

- [ ] Confirm token/cost expectations.
  - Tmux mode currently estimates token usage from text length.
  - If Agy exposes exact token accounting later, replace the estimate and update
    `TokenUsageSource`.

- [ ] Re-run full cross-repo validation after MCP wiring.
  - `multi-llm-provider-go` focused provider/contract tests.
  - `mcpagent` session handle/resume option tests.
  - `mcp-agent-builder-go` chat history/runtime persistence tests.
  - Opt-in real Agy E2E suite.

## Already Verified

- [x] Local `agy` supports native resume flags:
  - `--conversation <id>`
  - `--continue`
- [x] Adapter launches Agy TUI through tmux with `--prompt-interactive ""`.
- [x] Adapter writes system prompts to workspace-scoped Agy rules:
  - `.agents/rules/mlp-system-*.md`
- [x] Adapter writes MCP config to workspace-scoped Agy config:
  - `.agents/mcp_config.json`
- [x] Adapter writes bridge-only hooks to workspace-scoped Agy config:
  - `.agents/hooks.json`
- [x] Real MCP bridge E2E passed:
  - `TestAgyCLIRealMCPBridgeContract`
- [x] Real large-paste prompt E2E passed:
  - `TestAgyCLIRealLargePastedPromptSubmits`
- [x] Real stale-draft cleanup E2E passed:
  - `TestAgyCLIRealPersistentClearsStaleDraftBeforeNextTurn`
- [x] Real hook-enforced bridge-only tool E2E passed:
  - `TestAgyCLIRealBridgeOnlyWriteContract`
  - `TestAgyCLIRealBridgeOnlyHookBlocksBuiltInCommandContract`
  - `TestAgyCLIRealBridgeOnlyHookBlocksBuiltInReadContract`
  - `TestAgyCLIRealBridgeOnlyHookBlocksBuiltInListDirContract`
  - `TestAgyCLIRealBridgeOnlyHookBlocksBuiltInSearchContract`
- [x] Real working-directory E2E passed:
  - `TestAgyCLIRealWorkingDirectoryMCPContract`
- [x] Real slow-tool false-idle E2E passed:
  - `TestAgyCLIRealSlowToolFalseIdleContract`
- [x] Real slow-tool live-input / done-detection E2E passed:
  - `TestAgyCLIRealSlowToolLiveInputDoesNotCompleteContract`
- [x] Real cancellation and post-cancel retry E2E passed:
  - `TestAgyCLIRealCancellationClosesSessionContract`
- [x] Real parallel-session isolation E2E passed:
  - `TestAgyCLIRealInteractiveParallelIsolation`
- [x] Unsafe shared-workdir MCP config conflicts are rejected:
  - `TestAgyCLIRealSharedWorkingDirMCPConfigConflictRejected`
- [x] Real fresh-workspace startup E2E passed:
  - `TestAgyCLIRealTrustPromptFreshWorkspaceContract`
- [x] Real auth/login prompt surfacing E2E passed:
  - `TestAgyCLIRealAuthPromptSurfacedBeforePromptContract`
- [x] Agy trust/auth prompt detection and response mapping is covered:
  - `TestAgyTrustPromptDetectionAndResponse`
  - `TestAgyAuthPromptDetection`
- [x] Real system-rule E2E passed:
  - `TestAgyCLIRealSystemPromptRulesContract`
- [x] Agy session cleanup requests `/exit` before falling back to tmux kill.
- [x] Adapter captures `agy_session_id` from Agy local state/logs.
- [x] Adapter resumes with `agy --conversation <id>`.
- [x] Real native-resume E2E passed:
  - `TestAgyCLIRealNativeResumeAfterTmuxLossContract`
- [x] Resume startup accepts the next prompt without a blocking compaction menu:
  - `TestAgyCLIRealNativeResumeAfterTmuxLossContract`

## Publish Gate

Agy can be used for local experimentation only. Production publishing requires:

1. Provider certification gaps reduced or explicitly accepted.
2. Builder provider exposure reviewed after the above.
