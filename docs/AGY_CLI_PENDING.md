# Antigravity CLI Pending Items

Status: do not publish or promote `agy-cli` as a production CLI provider yet.

This file tracks the known gaps before Antigravity CLI should be treated as a
fully supported coding-agent provider across `multi-llm-provider-go`,
`mcpagent`, and `mcp-agent-builder-go`.

## Pending Before Publish

- [ ] Verify workspace-scoped MCP isolation.
  - Two Agy sessions in separate working dirs must not see each other's bridge
    config or tools.
  - Shared workdir behavior must be explicitly tested or disallowed.

- [x] Keep Agy hidden from production/provider publishing until broader
  certification gaps are resolved.
  - Chat, tmux, live input, native resume, system rules, and workspace MCP
    config writing are working. A real MCP bridge tool call is now certified,
    but other tmux contract gaps remain.
  - Agy remains available for explicit local/contract tests, but it is removed
    from published/default provider lists until the remaining gaps are accepted
    or resolved.

- [ ] Finish remaining certification gaps.
  - Known gaps are tracked in `coding_agent_contract_test.go` under
    `knownCertificationGaps[ProviderAgyCLI]`.
  - Native resume after tmux loss has a real E2E, and system prompts are now
    written as workspace rules.
  - Prompt paste, slow-tool behavior, cancellation, trust/auth prompts, working
    directory, and MCP bridge coverage still need to be drained.

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
- [x] Real MCP bridge E2E passed:
  - `TestAgyCLIRealMCPBridgeContract`
- [x] Real bridge-only tool steering E2E passed:
  - `TestAgyCLIRealBridgeOnlyWriteContract`
- [x] Real system-rule E2E passed:
  - `TestAgyCLIRealSystemPromptRulesContract`
- [x] Adapter captures `agy_session_id` from Agy local state/logs.
- [x] Adapter resumes with `agy --conversation <id>`.
- [x] Real native-resume E2E passed:
  - `TestAgyCLIRealNativeResumeAfterTmuxLossContract`

## Publish Gate

Agy can be used for local experimentation only. Production publishing requires:

1. Provider certification gaps reduced or explicitly accepted.
2. Builder provider exposure reviewed after the above.
