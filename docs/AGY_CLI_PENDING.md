# Antigravity CLI Deprecated Runtime Notes

Status: `agy-cli` is now **deprecated for new setup**. It remains fully implemented and runnable for existing sessions, restored chat history, and regression coverage, but new Google-backed coding-agent setup should use `pi-cli`.

This file tracks the retained runtime contract and verification milestones for keeping existing Antigravity CLI sessions safe while Pi CLI becomes the preferred Google/Gemini-backed coding-agent provider.

> [!NOTE]
> Antigravity CLI is deprecated for new setup, so it is no longer held to the full active-provider tmux promotion bar. Its retained runtime claims still have registered proofs; the extra full-promotion workspace-isolation proof remains useful only if Agy is ever re-promoted.

---

## 🔍 Certification Status Matrix

| Certification ID | Status | Proof Test Name | Description |
| :--- | :---: | :--- | :--- |
| **CertFreshLaunch** | ✅ | `TestAgyCLIRealInteractiveTmuxFullContract` | Reaches ready state on fresh launch and streams terminal chunks. |
| **CertStatusLine** | ✅ | `TestStreamAgyStatusLineEmitsFullChunk` | Emits a status_line chunk with token telemetry and tmux metadata. |
| **CertStartupTerminalVisibility** | ✅ | `TestAgyCLIRealInteractiveTmuxFullContract` | Foreground working/startup panes emit raw terminal rows to output. |
| **CertResumeCompactionStartup** | ✅ | `TestAgyCLIRealNativeResumeAfterTmuxLossContract` | Conversation relaunch accepts the next prompt without a blocking menu. |
| **CertTrustAuthPrompts** | ✅ | `TestAgyCLIRealAuthPromptSurfacedBeforePromptContract` | Relaunch surfaces auth/trust prompts cleanly back to the driver. |
| **CertNativeSystemPrompt** | ✅ | `TestAgyCLIRealSystemPromptRulesContract` | Loads instructions via workspace-scoped `.agents/rules` instead of raw paste. |
| **CertPromptPaste** | ✅ | `TestAgyCLIRealLargePastedPromptSubmits` | Large multiline prompt pastes and submits correctly via tmux. |
| **CertMCPBridge** | ✅ | `TestAgyCLIRealMCPBridgeContract` | Loads workspace-scoped `.agents/mcp_config.json` and bridges calls. |
| **CertBridgeOnlyTools** | ✅ | `TestAgyCLIRealBridgeOnlyToolsContract` | Denies built-in file/shell commands while preserving MCP bridge tools. |
| **CertWorkingDirectory** | ✅ | `TestAgyCLIRealWorkingDirectoryMCPContract` | Ensures MCP bridge tools run from the adapter-supplied directory. |
| **CertSlowToolFalseIdle** | ✅ | `TestAgyCLIRealSlowToolFalseIdleContract` | Tmux completion waits for slow MCP results instead of early idle. |
| **CertSlowToolLiveInput** | ✅ | `TestAgyCLIRealSlowToolLiveInputDoesNotCompleteContract` | Queues live user validation without interrupting slow-tool execution. |
| **CertDoneDetection** | ✅ | `TestAgyCLIRealSlowToolLiveInputDoesNotCompleteContract` | Slowly running MCP plus live input is not parsed as a finished turn. |
| **CertFinalExtraction** | ✅ | `TestAgyCLIRealFinalExtractionFromTmuxVertexJudgeE2E` | Semantic extraction cleans up thought/TUI noise and formats correctly. |
| **CertMultiTurn** | ✅ | `TestAgyCLIRealInteractiveTmuxFullContract` | Reuses persistent agy chat sessions across sequential turns. |
| **CertStaleDraftCleanup** | ✅ | `TestAgyCLIRealPersistentClearsStaleDraftBeforeNextTurn` | Clears any stranded prompt input before pasting the next user prompt. |
| **CertLifecyclePolicy** | ✅ | `TestAgyCLIRealInteractiveTmuxFullContract` | Persistent sessions are registered and survive completed turns. |
| **CertLiveInput** | ✅ | `TestAgyCLIRealInteractiveLiveInputAndEscapeContract` | Injects live keyboard feedback directly into the active agy session. |
| **CertCancellation** | ✅ | `TestAgyCLIRealCancellationClosesSessionContract` | Context cancellations interrupt active slow tools gracefully. |
| **CertPersistentCancelReuse** | ✅ | `TestAgyCLIRealCancellationClosesSessionContract` | Closed canceled sessions clean up and restart in fresh tmux states. |
| **CertBoundedRetention** | ✅ | `TestCleanupAgyCLIInteractiveSessionsDoesNotBlockOnBusySession` | Retention cleanup loop executes safely without blocking active sessions. |
| **CertParallelIsolation** | ✅ | `TestAgyCLIRealInteractiveParallelIsolation` | Parallel agy tmux sessions have completely isolated state and views. |
| **CertCleanup** | ✅ | `TestCleanupAgyCLIInteractiveSessionsDoesNotBlockOnBusySession` | Teardown path does not deadlock on busy persistent CLI sessions. |
| **CertSessionLoss** | ✅ | `TestAgyCLIRealNativeResumeAfterTmuxLossContract` | Correctly captures and persists provider conversation state upon tmux loss. |
| **CertSessionLossRecovery** | ✅ | `TestAgyCLIRealNativeResumeAfterTmuxLossContract` | Re-attaches with `--conversation` and resumes without replaying history. |
| **CertParallelStartupQueue** | ✅ | `TestAcquireQueuesConcurrentStarts` | Serializes concurrent agy-cli session startups. |
| **CertSharedWorkdirMCPIsolation** | ⚪ *Re-promotion only* | *Awaiting Test* | Two agy sessions in separate subdirectories must not cross-talk MCP. |

---

## 🚧 Retained Runtime Gap

> [!WARNING]
> Agy is not currently a production-promotion target for new setup. If it is ever re-promoted, the following final item must be resolved first:

### 1. 🔄 Verify Workspace-Scoped MCP Isolation (`CertSharedWorkdirMCPIsolation`)
*   **Gap Description:** Two concurrent `agy` sessions started under distinct working sub-directories must not see each other's custom bridge configuration, rule folders, or active tool bindings.
*   **Status:** Currently fail-closed but untested under concurrent setups.
*   **Drain Path:** Implement an E2E test verifying workspace MCP isolation and register it in `coding_agent_certification.go` before treating Agy as an active provider again.

### 2. 📊 Confirm Token & Cost Estimations
*   **Detail:** Tmux-mode currently calculates estimated token counts based on plain text length as `agy` does not expose exact API token usage metrics natively in TUI mode.
*   **Runtime Note:** Keep token estimation as-is for restored Agy sessions, or update `TokenUsageSource` if `agy` adds exact token auditing logs.

### 3. 🧪 Re-run Full Cross-Repo Validation
Validate integrated execution flows across the three core repos:
- `multi-llm-provider-go` focused provider/contract tests.
- `mcpagent` session handle/resume option tests.
- `mcp-agent-builder-go` chat history/runtime persistence tests.

---

## 🎯 Already Fully Verified (Detailed)

*   **Native Resume Support:** Full programmatic support for the `--conversation <id>` and `--continue` CLI flags.
*   **Interactive TUI Tmux Layer:** launches through tmux with `--prompt-interactive ""` and captures output.
*   **Workspace-Scoped Conventions:**
    *   Writes instructions/system prompts under `<workingDir>/.agents/rules/mlp-system-*.md`.
    *   Writes MCP settings into `<workingDir>/.agents/mcp_config.json`.
    *   Writes custom lifecycle hooks under `<workingDir>/.agents/hooks.json`.
*   **Failures & Authentication Surfacing:** Captures startup `trusting workspace` or login/auth prompts and formats them back to the caller as actionable errors.
*   **Graceful Exit Hook:** Requesting exit sends `/exit` to the terminal first, falling back to a hard SIGKILL on tmux panels only when unresponsive.
