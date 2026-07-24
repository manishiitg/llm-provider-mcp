package llmproviders

import (
	"sort"
	"strings"
)

// CodingAgentCertificationID is an executable proof point for one part of the
// coding-agent contract. Capability flags in CodingAgentProviderContract should
// map to these IDs instead of being treated as comments.
type CodingAgentCertificationID string

// CodingAgentCertificationPriority separates the absolute runtime contract
// from broader compatibility coverage. P0 is release-blocking: an active CLI
// provider is not usable when any P0 proof is missing.
type CodingAgentCertificationPriority string

const (
	CodingAgentCertificationPriorityP0 CodingAgentCertificationPriority = "P0"
	CodingAgentCertificationPriorityP1 CodingAgentCertificationPriority = "P1"
)

const (
	CertFreshLaunch               CodingAgentCertificationID = "fresh_launch"
	CertRuntimeContext            CodingAgentCertificationID = "runtime_context"
	CertResumeCompactionStartup   CodingAgentCertificationID = "resume_compaction_startup"
	CertStartupTerminalVisibility CodingAgentCertificationID = "startup_terminal_visibility"
	CertWorkingDirectory          CodingAgentCertificationID = "working_directory"
	CertTrustAuthPrompts          CodingAgentCertificationID = "trust_auth_prompts"
	CertNativeSystemPrompt        CodingAgentCertificationID = "native_system_prompt"
	CertPromptPaste               CodingAgentCertificationID = "prompt_paste"
	CertMCPBridge                 CodingAgentCertificationID = "mcp_bridge"
	CertBridgeOnlyTools           CodingAgentCertificationID = "bridge_only_tools"
	CertSlowToolLiveInput         CodingAgentCertificationID = "slow_tool_live_input"
	CertSlowToolFalseIdle         CodingAgentCertificationID = "slow_tool_false_idle"
	CertDoneDetection             CodingAgentCertificationID = "done_detection"
	CertFinalExtraction           CodingAgentCertificationID = "final_extraction"
	CertStatusLine                CodingAgentCertificationID = "statusline"
	CertMultiTurn                 CodingAgentCertificationID = "multi_turn"
	CertStaleDraftCleanup         CodingAgentCertificationID = "stale_draft_cleanup"
	CertLiveInput                 CodingAgentCertificationID = "live_input"
	CertBusyLiveInput             CodingAgentCertificationID = "busy_live_input"
	CertCancellation              CodingAgentCertificationID = "cancellation"
	CertPersistentCancelReuse     CodingAgentCertificationID = "persistent_cancel_reuse"
	CertLifecyclePolicy           CodingAgentCertificationID = "lifecycle_policy"
	CertBoundedRetention          CodingAgentCertificationID = "bounded_retention"
	CertParallelIsolation         CodingAgentCertificationID = "parallel_isolation"
	CertParallelStartupQueue      CodingAgentCertificationID = "parallel_startup_queue"
	CertSharedWorkdirMCPIsolation CodingAgentCertificationID = "shared_workdir_mcp_isolation"
	CertCleanup                   CodingAgentCertificationID = "cleanup"
	CertSessionLoss               CodingAgentCertificationID = "session_loss"
	CertSessionLossRecovery       CodingAgentCertificationID = "session_loss_recovery"
	// CertStructuredStreaming proves the adapter streams STRUCTURED assistant-text
	// + tool-call chunks live during a turn (the no-terminal streaming path),
	// verified by a real bridge/MCP turn. The source is provider-specific
	// (transcript tail for claude/codex/cursor, injected markers for pi). Required
	// as P0 for any provider whose contract sets SupportsStructuredStreaming.
	CertStructuredStreaming CodingAgentCertificationID = "structured_streaming"
	// CertCtrlCStatePreserved proves that sending Ctrl+C (the 0x03 keystroke
	// in tmux mode, SIGINT for structured mode) interrupts the current turn
	// WITHOUT corrupting the CLI's persisted chat state. The next launch
	// with --resume <id> must still see the prior conversation intact.
	// This is distinct from CertCancellation, which only proves the
	// interrupt is RECEIVED — not that state survives it.
	CertCtrlCStatePreserved CodingAgentCertificationID = "ctrl_c_state_preserved"
)

// requiredTmuxCertificationIDs is the full promotion bar for an active tmux
// coding-agent provider. Deprecated tmux providers keep legacy runtime wiring
// for restored sessions, but they are not treated as new-provider promotion
// targets; their certification requirements are derived from explicit
// capability flags below instead.
var requiredTmuxCertificationIDs = []CodingAgentCertificationID{
	CertFreshLaunch,
	CertRuntimeContext,
	CertResumeCompactionStartup,
	CertStartupTerminalVisibility,
	CertWorkingDirectory,
	CertTrustAuthPrompts,
	CertNativeSystemPrompt,
	CertPromptPaste,
	CertMCPBridge,
	CertBridgeOnlyTools,
	CertSlowToolLiveInput,
	CertSlowToolFalseIdle,
	CertDoneDetection,
	CertFinalExtraction,
	CertMultiTurn,
	CertStaleDraftCleanup,
	CertLiveInput,
	CertBusyLiveInput,
	CertCancellation,
	CertPersistentCancelReuse,
	CertLifecyclePolicy,
	CertBoundedRetention,
	CertParallelIsolation,
	CertParallelStartupQueue,
	CertSharedWorkdirMCPIsolation,
	CertCleanup,
	CertSessionLoss,
	CertSessionLossRecovery,
}

// requiredP0CertificationIDs is deliberately short. These are the contracts
// without which AgentWorks cannot safely run a coding CLI: launch in the right
// workspace, clear trust/auth startup gates before the first prompt, receive the
// system prompt/skills/MCP runtime, avoid false idle, detect completion, accept
// and process live follow-up input while busy, return the final answer, cancel,
// and isolate concurrency.
var requiredP0CertificationIDs = []CodingAgentCertificationID{
	CertFreshLaunch,
	CertRuntimeContext,
	CertWorkingDirectory,
	CertTrustAuthPrompts,
	CertMCPBridge,
	CertSlowToolFalseIdle,
	CertDoneDetection,
	CertFinalExtraction,
	CertLiveInput,
	CertBusyLiveInput,
	CertCancellation,
	CertParallelIsolation,
}

// CodingAgentCertification records the real or deterministic test that proves a
// certification ID. TestFile is repository-relative so normal unit tests can
// verify that the referenced proof still exists.
type CodingAgentCertification struct {
	ID          CodingAgentCertificationID
	Priority    CodingAgentCertificationPriority
	TestFile    string
	TestName    string
	Env         []string
	Description string
	RealE2E     bool
}

var codingAgentCapabilityCertifications = []struct {
	name     string
	enabled  func(CodingAgentProviderContract) bool
	required []CodingAgentCertificationID
}{
	{"working directory", func(c CodingAgentProviderContract) bool { return c.RequiresWorkingDir }, []CodingAgentCertificationID{CertWorkingDirectory}},
	{"trust/auth prompts", func(c CodingAgentProviderContract) bool { return c.RequiresWorkspaceTrust }, []CodingAgentCertificationID{CertTrustAuthPrompts}},
	{"restore interactive prompts", func(c CodingAgentProviderContract) bool { return c.RestoreAsksInteractivePrompts }, []CodingAgentCertificationID{CertResumeCompactionStartup}},
	{"native system prompt", func(c CodingAgentProviderContract) bool { return c.UsesNativeSystemPrompt }, []CodingAgentCertificationID{CertNativeSystemPrompt}},
	{"mcp bridge", func(c CodingAgentProviderContract) bool { return c.UsesMCPBridge }, []CodingAgentCertificationID{CertMCPBridge}},
	{"bridge only tools", func(c CodingAgentProviderContract) bool { return c.SupportsBridgeOnlyTools }, []CodingAgentCertificationID{CertBridgeOnlyTools}},
	{"terminal stream", func(c CodingAgentProviderContract) bool { return c.SupportsTerminalStream }, []CodingAgentCertificationID{CertFreshLaunch}},
	{"statusline", func(c CodingAgentProviderContract) bool { return c.SupportsStatusLine }, []CodingAgentCertificationID{CertStatusLine}},
	{"final extraction", func(c CodingAgentProviderContract) bool { return c.SupportsFinalExtraction }, []CodingAgentCertificationID{CertFinalExtraction}},
	{"persistent session", func(c CodingAgentProviderContract) bool { return c.UsesPersistentSession }, []CodingAgentCertificationID{CertMultiTurn, CertStaleDraftCleanup}},
	{"live input", func(c CodingAgentProviderContract) bool { return c.SupportsLiveInput }, []CodingAgentCertificationID{CertLiveInput, CertBusyLiveInput}},
	{"interrupt", func(c CodingAgentProviderContract) bool { return c.SupportsInterrupt }, []CodingAgentCertificationID{CertCancellation}},
	{"ctrl-c state preserved", func(c CodingAgentProviderContract) bool { return c.HandlesCtrlCCleanExit }, []CodingAgentCertificationID{CertCtrlCStatePreserved}},
	{"process cleanup", func(c CodingAgentProviderContract) bool { return c.ProcessScopedCleanup }, []CodingAgentCertificationID{CertCleanup}},
	{"session loss", func(c CodingAgentProviderContract) bool { return c.HandlesTmuxSessionLoss }, []CodingAgentCertificationID{CertSessionLoss, CertSessionLossRecovery}},
	{"structured streaming", func(c CodingAgentProviderContract) bool { return c.SupportsStructuredStreaming }, []CodingAgentCertificationID{CertStructuredStreaming}},
}

var codingAgentProviderCertifications = map[Provider][]CodingAgentCertification{
	ProviderClaudeCode: {
		{
			ID:          CertStructuredStreaming,
			TestFile:    "pkg/adapters/claudecode/claudecode_transcript_stream_realworld_test.go",
			TestName:    "TestClaudeCodeTranscriptStreamingRealWorldLive",
			Description: "tails the live JSONL transcript and streams structured assistant-text + MCP tool-call chunks across a real search→write→read bridge task",
			RealE2E:     true,
		},
		{
			ID:          CertRuntimeContext,
			TestFile:    "pkg/adapters/claudecode/claudecode_runtime_self_check_e2e_test.go",
			TestName:    "TestClaudeCodeTmuxRuntimeSelfCheckContract",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "proves the real Claude Code session receives its system prompt, attached skill, and MCP bridge",
			RealE2E:     true,
		},
		{
			ID:          CertFreshLaunch,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationFreshPromptCarriesUserText",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "starts Claude Code tmux transport and carries the first user prompt",
			RealE2E:     true,
		},
		{
			ID:          CertStatusLine,
			TestFile:    "pkg/adapters/claudecode/claudecode_statusline_stream_test.go",
			TestName:    "TestStreamClaudeStatusLineEmitsChunk",
			Description: "emits a status_line chunk carrying the real model (display_name), cost, tokens, and owning tmux session — no hardcoded default model",
		},
		{
			ID:          CertResumeCompactionStartup,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_adapter_test.go",
			TestName:    "TestClaudeResumeSummaryMenuSubmitsDefaultChoice",
			Description: "resume/compaction startup prompts are detected and submitted through the global prompt-wait path",
		},
		{
			ID:          CertStartupTerminalVisibility,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_adapter_test.go",
			TestName:    "TestClaudeTerminalStreamCapturesRawScreenRows",
			Description: "Claude startup/working panes emit raw terminal rows before final completion",
		},
		{
			ID:          CertWorkingDirectory,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuWorkingDirectory",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "proves Claude Code and its MCP bridge run in the requested cwd",
			RealE2E:     true,
		},
		{
			ID:          CertTrustAuthPrompts,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuWorkingDirectory",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "fresh Claude workspace trust setup is handled before launching in a requested cwd",
			RealE2E:     true,
		},
		{
			ID:          CertNativeSystemPrompt,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationNativeSystemPrompt",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "system prompt reaches Claude through native system-prompt transport",
			RealE2E:     true,
		},
		{
			ID:          CertPromptPaste,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationLargePastedPromptSubmits",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "large pasted prompt submits correctly through tmux",
			RealE2E:     true,
		},
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuMCPBridgeContract",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "Claude Code calls a real MCP bridge tool",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationNoInternalTools",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "Claude Code bridge-only mode does not expose internal shell/file tools",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolLiveInput,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "live validation input during a slow MCP tool does not complete the foreground turn",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolFalseIdle,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "slow MCP activity keeps Claude classified active even if prompt-looking UI appears",
			RealE2E:     true,
		},
		{
			ID:          CertDoneDetection,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "slow MCP plus live input does not falsely complete while active",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxRealFinalExtractionFromTmuxVertexJudgeE2E",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1", "GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "real Claude Code tmux turn is captured and Vertex judges final extraction quality, formatting, and TUI noise removal",
			RealE2E:     true,
		},
		{
			ID:          CertMultiTurn,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuPersistentInteractiveMultiTurn",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "persistent Claude Code tmux session keeps native multi-turn memory",
			RealE2E:     true,
		},
		{
			ID:          CertStaleDraftCleanup,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationPersistentClearsStaleDraftBeforeNextTurn",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "stale prompt drafts are cleared before the next backend turn",
			RealE2E:     true,
		},
		{
			ID:          CertLiveInput,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "live input routes into the active Claude Code tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertBusyLiveInput,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationLiveInputProcessesQueuedFollowup",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "a follow-up submitted during a slow Claude Code tool call is processed by the same live session",
			RealE2E:     true,
		},
		{
			ID:          CertCancellation,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationPersistentCancelDoesNotLeaveBusySessionReusable",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "canceled Claude Code persistent sessions are not reused while busy",
			RealE2E:     true,
		},
		{
			ID:          CertPersistentCancelReuse,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationPersistentCancelDoesNotLeaveBusySessionReusable",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "a canceled persistent Claude pane is not reused unless it is prompt-ready",
			RealE2E:     true,
		},
		{
			ID:          CertLifecyclePolicy,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuPersistentInteractiveMultiTurn",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "persistent chat sessions remain registered after a completed turn",
			RealE2E:     true,
		},
		{
			ID:          CertBoundedRetention,
			TestFile:    "pkg/adapters/claudecode/claudecode_cleanup_test.go",
			TestName:    "TestCleanupClaudeCodeTmuxSessionsDoesNotBlockOnBusyPersistentSession",
			Description: "bounded Claude cleanup/retention path can drain without blocking active sessions",
		},
		{
			ID:          CertParallelIsolation,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationParallelIsolation",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "parallel Claude tmux sessions do not share state",
			RealE2E:     true,
		},
		{
			ID:          CertParallelStartupQueue,
			TestFile:    "pkg/adapters/internal/tmuxlaunch/tmuxlaunch_test.go",
			TestName:    "TestAcquireQueuesConcurrentStarts",
			Description: "tmux startup acquisition serializes concurrent provider launches when configured",
		},
		{
			ID:          CertSharedWorkdirMCPIsolation,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationSharedWorkingDirMCPIsolation",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "parallel Claude sessions in one cwd keep MCP sessions isolated",
			RealE2E:     true,
		},
		{
			ID:          CertCleanup,
			TestFile:    "pkg/adapters/claudecode/claudecode_cleanup_test.go",
			TestName:    "TestCleanupClaudeCodeTmuxSessionsDoesNotBlockOnBusyPersistentSession",
			Description: "cleanup does not deadlock on busy persistent Claude Code sessions",
		},
		{
			ID:          CertSessionLoss,
			TestFile:    "pkg/adapters/claudecode/claudecode_interactive_adapter_test.go",
			TestName:    "TestClaudeTmuxSessionLostErrorDetection",
			Description: "Claude Code classifies missing tmux server/session/pane errors",
		},
		{
			ID:          CertSessionLossRecovery,
			TestFile:    "coding_agent_continuation_real_test.go",
			TestName:    "TestCodingAgentContinuationRealE2EAfterTmuxLoss",
			Env:         []string{"RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1"},
			Description: "Claude Code recovers a remembered-token conversation after its tmux session is killed",
			RealE2E:     true,
		},
	},
	ProviderCodexCLI: {
		{
			ID:          CertStructuredStreaming,
			TestFile:    "pkg/adapters/codexcli/codexcli_transcript_stream_realworld_test.go",
			TestName:    "TestCodexCLITranscriptStreamingRealWorldLive",
			Description: "tails the live rollout JSONL and streams structured assistant-text + MCP tool-call chunks across a real search→write→read bridge task",
			RealE2E:     true,
		},
		{
			ID:          CertRuntimeContext,
			TestFile:    "pkg/adapters/codexcli/codexcli_runtime_self_check_e2e_test.go",
			TestName:    "TestCodexCLIRealRuntimeSelfCheckContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1"},
			Description: "proves the real Codex CLI session receives its system prompt, attached skill, and MCP bridge",
			RealE2E:     true,
		},
		{
			ID:          CertFreshLaunch,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "starts Codex CLI tmux transport, reaches ready, and streams terminal-only chunks",
			RealE2E:     true,
		},
		{
			ID:          CertStatusLine,
			TestFile:    "pkg/adapters/codexcli/codexcli_statusline_stream_test.go",
			TestName:    "TestStreamCodexStatusLineEmitsChunk",
			Description: "emits a status_line chunk from the rollout JSONL token_count (uncached/cached/output tokens, real model, tmux session) — tmux mode has no statusline command hook",
		},
		{
			ID:          CertResumeCompactionStartup,
			TestFile:    "pkg/adapters/codexcli/codexcli_adapter_test.go",
			TestName:    "TestCodexInteractivePromptWaitDefaultsToStartupBudget",
			Description: "Codex prompt waits use the startup budget rather than a fixed short cutoff",
		},
		{
			ID:          CertStartupTerminalVisibility,
			TestFile:    "pkg/adapters/codexcli/codexcli_adapter_test.go",
			TestName:    "TestCodexTerminalStreamCapturesRawScreenRows",
			Description: "Codex startup/working panes emit raw terminal rows before final completion",
		},
		{
			ID:          CertWorkingDirectory,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveWorkingDirectoryContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "proves Codex CLI and its MCP bridge run in the requested cwd",
			RealE2E:     true,
		},
		{
			ID:          CertTrustAuthPrompts,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveWorkspaceTrustPromptContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "fresh Codex workspace trust prompt is handled and excluded from final output",
			RealE2E:     true,
		},
		{
			ID:          CertNativeSystemPrompt,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "system/developer instructions reach Codex through native config override",
			RealE2E:     true,
		},
		{
			ID:          CertPromptPaste,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "large multiline prompt submits correctly through tmux",
			RealE2E:     true,
		},
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveMCPBridgeContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "Codex CLI calls a real MCP bridge tool",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveMCPBridgeContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "Codex CLI bridge-only mode runs through MCP with internal shell disabled",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolLiveInput,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "live validation input during a slow MCP tool does not complete the foreground turn",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolFalseIdle,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "slow MCP activity keeps Codex classified active even if prompt-looking UI appears",
			RealE2E:     true,
		},
		{
			ID:          CertDoneDetection,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "slow MCP plus queued validation does not falsely complete while active",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealMCPBridgeFileFinalExtractionContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "real Codex CLI performs an MCP bridge file write and returns only its final assistant message, excluding MCP/TUI trail from workflow completion notifications",
			RealE2E:     true,
		},
		{
			ID:          CertMultiTurn,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "persistent Codex CLI tmux session keeps native multi-turn memory",
			RealE2E:     true,
		},
		{
			ID:          CertStaleDraftCleanup,
			TestFile:    "pkg/adapters/codexcli/codexcli_adapter_test.go",
			TestName:    "TestCodexQueuedInputKeepsSessionActive",
			Description: "queued/draft input is classified active and not completed as final output",
		},
		{
			ID:          CertLiveInput,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveLiveInputAndEscapeContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "live input routes into the active Codex CLI tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertBusyLiveInput,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveLiveInputSteersBusyTurnContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "a follow-up submitted during a slow Codex tool call is durably applied by the same live session",
			RealE2E:     true,
		},
		{
			ID:          CertCancellation,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveLiveInputAndEscapeContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "escape/interrupt path is exercised against a live Codex CLI session",
			RealE2E:     true,
		},
		{
			ID:          CertPersistentCancelReuse,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveLiveInputAndEscapeContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "a canceled persistent Codex pane is not reused unless it is prompt-ready",
			RealE2E:     true,
		},
		{
			ID:          CertLifecyclePolicy,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "persistent chat sessions remain registered after a completed turn",
			RealE2E:     true,
		},
		{
			ID:          CertBoundedRetention,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveCleanup",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "bounded Codex cleanup/retention path unregisters and removes owned sessions",
			RealE2E:     true,
		},
		{
			ID:          CertParallelStartupQueue,
			TestFile:    "pkg/adapters/internal/tmuxlaunch/tmuxlaunch_test.go",
			TestName:    "TestAcquireQueuesConcurrentStarts",
			Description: "tmux startup acquisition serializes concurrent provider launches when configured",
		},
		{
			ID:          CertCleanup,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveCleanup",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "Codex cleanup unregisters and kills owned tmux sessions",
			RealE2E:     true,
		},
		{
			ID:          CertSessionLoss,
			TestFile:    "pkg/adapters/codexcli/codexcli_cleanup_test.go",
			TestName:    "TestCleanupCodexCLIInteractiveSessionsDoesNotBlockOnBusySession",
			Description: "Codex cleanup path is safe when a session is busy or unavailable",
		},
		{
			ID:          CertSessionLossRecovery,
			TestFile:    "coding_agent_continuation_real_test.go",
			TestName:    "TestCodingAgentContinuationRealE2EAfterTmuxLoss",
			Env:         []string{"RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1"},
			Description: "Codex CLI recovers a remembered-token conversation after its tmux session is killed",
			RealE2E:     true,
		},
		{
			ID:          CertParallelIsolation,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveParallelIsolation",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "parallel Codex tmux sessions do not share state",
			RealE2E:     true,
		},
		{
			ID:          CertSharedWorkdirMCPIsolation,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveSharedWorkingDirMCPIsolation",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "parallel Codex sessions in one cwd keep MCP sessions isolated",
			RealE2E:     true,
		},
	},
	ProviderCursorCLI: {
		{
			ID:          CertStructuredStreaming,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_transcript_stream_realworld_test.go",
			TestName:    "TestCursorCLITranscriptStreamingRealWorldLive",
			Description: "tails the async store.db and streams structured assistant-text + MCP tool-call chunks across a real search→write→read bridge task",
			RealE2E:     true,
		},
		{
			ID:          CertRuntimeContext,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_runtime_self_check_e2e_test.go",
			TestName:    "TestCursorCLIRealRuntimeSelfCheckContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "proves the real Cursor CLI session receives its system prompt, attached skill, and MCP bridge",
			RealE2E:     true,
		},
		{
			ID:          CertFreshLaunch,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "starts Cursor Agent CLI tmux transport, submits a prompt, streams terminal rows, and returns a final answer",
			RealE2E:     true,
		},
		{
			ID:          CertResumeCompactionStartup,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_resume_e2e_test.go",
			TestName:    "TestCursorCLIRealCrossRestartResume",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "Cursor native --resume survives process restart and accepts the next prompt without blocking startup",
			RealE2E:     true,
		},
		{
			ID:          CertStartupTerminalVisibility,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "Cursor startup/working panes emit terminal-only stream chunks before final completion",
			RealE2E:     true,
		},
		{
			ID:          CertWorkingDirectory,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_isolated_workspace_e2e_test.go",
			TestName:    "TestCursorCLIRealIsolatedTmpDirDoesNotTouchOuterWorkspace",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "Cursor runs in the requested isolated working directory without leaking .cursor artifacts into the outer workspace",
			RealE2E:     true,
		},
		{
			ID:          CertTrustAuthPrompts,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealFreshWorkspaceTrustFirstPromptContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "fresh Cursor workspace trust is accepted, allowed to settle, and the first prompt produces a real answer instead of being dropped",
			RealE2E:     true,
		},
		{
			ID:          CertNativeSystemPrompt,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorTmuxSystemPromptSteersWritesThroughBridge",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "Cursor loads tmux-mode system prompt through .cursor/rules and uses it to route writes through the MCP bridge",
			RealE2E:     true,
		},
		{
			ID:          CertPromptPaste,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealVisibleMultilineDraftContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "multiline prompts remain literal and visible in Cursor's editor instead of collapsing into an opaque pasted-text marker",
			RealE2E:     true,
		},
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveMCPBridgeContractTmux",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "Cursor CLI tmux transport loads a workspace MCP config and calls a real MCP bridge tool",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_deny_behavioral_e2e_test.go",
			TestName:    "TestCursorCLIRealDenyBuiltinHookActuallyFires",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "Cursor CLI live tmux run denies built-in read/list/search paths and prevents sentinel leakage",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolLiveInput,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "live validation input during a slow Cursor MCP tool does not complete the foreground turn",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolFalseIdle,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveQueuedValidationDoesNotCompleteDuringMCPTool",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "slow MCP activity keeps Cursor classified active even if prompt-looking UI appears",
			RealE2E:     true,
		},
		{
			ID:          CertDoneDetection,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealCompletionDetection",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "Cursor tmux pane is idle and ready only after the response has completed",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealFinalExtractionFromTmuxVertexJudgeE2E",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1", "RUN_CURSOR_CLI_INTERACTIVE_E2E=1", "GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "real Cursor CLI tmux turn is captured and Vertex judges final extraction quality, formatting, and shell/TUI transcript removal",
			RealE2E:     true,
		},
		{
			ID:          CertMultiTurn,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "persistent Cursor tmux session keeps native multi-turn memory and reuses the same backing tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertLiveInput,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveLiveInputProcessesQueuedFollowupContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "live input routes into Cursor's active tmux session and is processed after the in-flight tool call",
			RealE2E:     true,
		},
		{
			ID:          CertBusyLiveInput,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveLiveInputProcessesQueuedFollowupContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "a follow-up submitted during a slow Cursor tool call is processed by the same live session",
			RealE2E:     true,
		},
		{
			ID:          CertCancellation,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveLiveInputAndEscapeContract",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "canceling a live Cursor tmux turn interrupts the running GenerateContent call",
			RealE2E:     true,
		},
		{
			ID:          CertParallelIsolation,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveParallelIsolation",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "parallel Cursor tmux sessions keep final answers isolated from one another",
			RealE2E:     true,
		},
		{
			ID:          CertParallelStartupQueue,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveParallelIsolation",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "concurrent Cursor tmux launches run through the shared startup gate without cross-session interference",
			RealE2E:     true,
		},
		{
			ID:          CertSharedWorkdirMCPIsolation,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveSharedWorkingDirMCPIsolation",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "parallel Cursor sessions with related workdirs keep MCP sessions and outputs isolated",
			RealE2E:     true,
		},
		{
			ID:          CertCleanup,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealInteractiveCleanup",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1"},
			Description: "explicit Cursor cleanup unregisters and kills the backing tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertSessionLoss,
			TestFile:    "coding_agent_continuation_real_test.go",
			TestName:    "TestCodingAgentContinuationRealE2EAfterTmuxLoss",
			Env:         []string{"RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1", "RUN_CURSOR_CLI_REAL_E2E via subtest cursor-cli"},
			Description: "Cursor tmux session loss is detected and exposed as a typed coding-agent session-loss condition",
			RealE2E:     true,
		},
		{
			ID:          CertSessionLossRecovery,
			TestFile:    "coding_agent_continuation_real_test.go",
			TestName:    "TestCodingAgentContinuationRealE2EAfterTmuxLoss",
			Env:         []string{"RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1", "RUN_CURSOR_CLI_REAL_E2E via subtest cursor-cli"},
			Description: "Cursor continuation after killed tmux starts a fresh tmux session and resumes provider-native memory",
			RealE2E:     true,
		},
	},
	ProviderPiCLI: {
		{
			ID:          CertStructuredStreaming,
			TestFile:    "pkg/adapters/picli/picli_structured_stream_realworld_test.go",
			TestName:    "TestPiCLIStructuredStreamingRealWorldLive",
			Description: "streams structured assistant-text + tool-call chunks from pi's injected marker stream across a real bridge task",
			RealE2E:     true,
		},
		{
			ID:          CertRuntimeContext,
			TestFile:    "pkg/adapters/picli/picli_runtime_self_check_e2e_test.go",
			TestName:    "TestPiCLIRealRuntimeSelfCheckContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "proves the real Pi CLI session receives its system prompt, attached skill, and MCP bridge",
			RealE2E:     true,
		},
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/picli/picli_mcp_bridge_real_test.go",
			TestName:    "TestPiCLIRealMCPBridgeOnlyToolsContract",
			Env:         []string{"RUN_PI_CLI_MCP_BRIDGE_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "launches real Pi with pi-mcp-adapter and proves an MCP stdio canary tool is called",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/picli/picli_mcp_bridge_real_test.go",
			TestName:    "TestPiCLIRealMCPBridgeOnlyToolsContract",
			Env:         []string{"RUN_PI_CLI_MCP_BRIDGE_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "real Pi bridge-only run disables built-ins and still reaches the MCP canary through extension tools",
			RealE2E:     true,
		},
		{
			ID:          CertStatusLine,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "real Pi tmux run emits a status_line chunk with selected provider/model route, transcript-backed token/cost telemetry when available, and owning tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertFreshLaunch,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "starts a real Pi tmux transport, streams terminal output, and carries the first user prompt",
			RealE2E:     true,
		},
		{
			ID:          CertResumeCompactionStartup,
			TestFile:    "coding_agent_continuation_real_test.go",
			TestName:    "TestCodingAgentContinuationRealE2EAfterTmuxLoss",
			Env:         []string{"RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "relaunches Pi with --session-id and proves no resume startup prompt blocks continuation",
			RealE2E:     true,
		},
		{
			ID:          CertStartupTerminalVisibility,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "real Pi startup/working pane emits terminal snapshots while the first turn is running",
			RealE2E:     true,
		},
		{
			ID:          CertWorkingDirectory,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealWorkingDirectoryMCPContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "real Pi launches in the requested working directory and an MCP server reports that cwd",
			RealE2E:     true,
		},
		{
			ID:          CertTrustAuthPrompts,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "fresh Pi workspace starts and completes without a trust/auth prompt being misclassified as ready output",
			RealE2E:     true,
		},
		{
			ID:          CertNativeSystemPrompt,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "system prompt reaches Pi through --append-system-prompt and controls the first response",
			RealE2E:     true,
		},
		{
			ID:          CertPromptPaste,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "large multiline pasted prompt submits to the real Pi tmux TUI and preserves the tail token",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolLiveInput,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealSlowToolLiveInputAndCancellationContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "live validation input is injected into a real Pi pane while a slow MCP tool is active without completing the foreground turn",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolFalseIdle,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealSlowMCPToolDoneDetectionContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "real Pi waits for a slow MCP tool delay before classifying the turn as done",
			RealE2E:     true,
		},
		{
			ID:          CertDoneDetection,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealSlowMCPToolDoneDetectionContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "done detection waits for Pi's agent_end marker after a real slow MCP tool completes",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "real Pi final content is extracted without prompt, marker, or terminal noise",
			RealE2E:     true,
		},
		{
			ID:          CertMultiTurn,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealTmuxFullContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "persistent Pi tmux session keeps native multi-turn memory and reuses the same tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertStaleDraftCleanup,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealPersistentClearsStaleDraftBeforeNextTurn",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "stale text typed into a Pi pane is cleared before the next backend prompt is pasted",
			RealE2E:     true,
		},
		{
			ID:          CertLiveInput,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealSlowToolLiveInputAndCancellationContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "live input routes into a registered real Pi tmux session while it is active",
			RealE2E:     true,
		},
		{
			ID:          CertBusyLiveInput,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealInteractiveLiveInputProcessesQueuedFollowupContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "a follow-up submitted during a slow Pi tool call is processed by the same live session",
			RealE2E:     true,
		},
		{
			ID:          CertCancellation,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealSlowToolLiveInputAndCancellationContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "canceled Pi persistent sessions are unregistered and their tmux session is closed",
			RealE2E:     true,
		},
		{
			ID:          CertPersistentCancelReuse,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealSlowToolLiveInputAndCancellationContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "after cancellation, the same owner starts a fresh Pi tmux session and completes a retry turn",
			RealE2E:     true,
		},
		{
			ID:          CertLifecyclePolicy,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealCleanupAndBoundedRetentionContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "persistent Pi sessions remain active after a turn while bounded sessions are retained only until the retention timer fires",
			RealE2E:     true,
		},
		{
			ID:          CertBoundedRetention,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealCleanupAndBoundedRetentionContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "bounded non-persistent Pi tmux sessions are cleaned up after a short retention interval",
			RealE2E:     true,
		},
		{
			ID:          CertParallelIsolation,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealParallelIsolationContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "parallel real Pi tmux sessions keep distinct owners, tmux sessions, and remembered tokens",
			RealE2E:     true,
		},
		{
			ID:          CertParallelStartupQueue,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealParallelIsolationContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "parallel real Pi launches acquire distinct tmux sessions without startup collisions",
			RealE2E:     true,
		},
		{
			ID:          CertSharedWorkdirMCPIsolation,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealSharedWorkingDirMCPConfigConflictRejected",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "shared-workdir Pi sessions with conflicting MCP configs are rejected while the first real session is live",
			RealE2E:     true,
		},
		{
			ID:          CertCleanup,
			TestFile:    "pkg/adapters/picli/picli_real_contract_test.go",
			TestName:    "TestPiCLIRealCleanupAndBoundedRetentionContract",
			Env:         []string{"RUN_PI_CLI_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "process cleanup kills registered Pi tmux sessions and restores/removes .pi/mcp.json",
			RealE2E:     true,
		},
		{
			ID:          CertSessionLoss,
			TestFile:    "coding_agent_continuation_real_test.go",
			TestName:    "TestCodingAgentContinuationRealE2EAfterTmuxLoss",
			Env:         []string{"RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "simulates Pi tmux loss after capturing a provider-native --session-id handle",
			RealE2E:     true,
		},
		{
			ID:          CertSessionLossRecovery,
			TestFile:    "coding_agent_continuation_real_test.go",
			TestName:    "TestCodingAgentContinuationRealE2EAfterTmuxLoss",
			Env:         []string{"RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1", "GEMINI_API_KEY or GOOGLE_API_KEY or PI_API_KEY"},
			Description: "relaunches Pi with --session-id and recalls a canary without app history replay",
			RealE2E:     true,
		},
	},
}

// RequiredCodingAgentCertificationIDs returns the proof IDs implied by the
// provider's claimed capabilities.
func RequiredCodingAgentCertificationIDs(contract CodingAgentProviderContract) []CodingAgentCertificationID {
	seen := make(map[CodingAgentCertificationID]struct{}, len(requiredTmuxCertificationIDs))
	if contract.Transport == CodingAgentTransportTmux && !contract.Deprecated {
		for _, id := range requiredTmuxCertificationIDs {
			seen[id] = struct{}{}
		}
	} else if contract.Transport != CodingAgentTransportTmux {
		seen[CertFreshLaunch] = struct{}{}
		seen[CertPromptPaste] = struct{}{}
		seen[CertDoneDetection] = struct{}{}
		seen[CertFinalExtraction] = struct{}{}
	}
	for _, rule := range codingAgentCapabilityCertifications {
		if !rule.enabled(contract) {
			continue
		}
		for _, id := range rule.required {
			seen[id] = struct{}{}
		}
	}

	out := make([]CodingAgentCertificationID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func CodingAgentProviderCertifications(provider Provider) []CodingAgentCertification {
	provider = Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	certs := append([]CodingAgentCertification(nil), codingAgentProviderCertifications[provider]...)
	for i := range certs {
		if certs[i].Priority == "" {
			certs[i].Priority = CodingAgentCertificationPriorityForID(certs[i].ID)
		}
		if certs[i].Priority == CodingAgentCertificationPriorityP0 {
			// P0 has one supported gate: the authenticated live runner. Legacy
			// per-provider RUN_* environment switches remain metadata for P1
			// integration tests, but must not define or weaken P0 execution.
			certs[i].Env = []string{"-coding-cli-p0-live"}
		}
	}
	sort.Slice(certs, func(i, j int) bool {
		if certs[i].ID == certs[j].ID {
			return certs[i].TestName < certs[j].TestName
		}
		return certs[i].ID < certs[j].ID
	})
	return certs
}

// CodingAgentCertificationPriorityForID returns the release priority for a
// proof ID. New certifications default to P1 until deliberately promoted.
func CodingAgentCertificationPriorityForID(id CodingAgentCertificationID) CodingAgentCertificationPriority {
	for _, required := range requiredP0CertificationIDs {
		if id == required {
			return CodingAgentCertificationPriorityP0
		}
	}
	// Streaming is P0 wherever it is required (capability-gated per provider via
	// RequiredP0CodingAgentCertificationIDs), so its registered cert must carry P0
	// priority + the live gate rather than defaulting to P1.
	if id == CertStructuredStreaming {
		return CodingAgentCertificationPriorityP0
	}
	return CodingAgentCertificationPriorityP1
}

// RequiredP0CodingAgentCertificationIDs returns the non-negotiable runtime
// proofs for active tmux coding-agent providers.
func RequiredP0CodingAgentCertificationIDs(contract CodingAgentProviderContract) []CodingAgentCertificationID {
	if contract.Transport != CodingAgentTransportTmux || contract.Deprecated {
		return nil
	}
	ids := append([]CodingAgentCertificationID(nil), requiredP0CertificationIDs...)
	// Streaming is release-blocking only for providers that actually stream
	// structured transcript chunks. A provider that merely reads a transcript for
	// a final-answer summary (pi today) is not required to certify streaming —
	// until its live tailer + streaming E2E land and it flips the flag on.
	if contract.SupportsStructuredStreaming {
		ids = append(ids, CertStructuredStreaming)
	}
	return ids
}

// MissingP0CodingAgentCertifications is intentionally independent from the
// broader known-gap mechanism: P0 gaps can never be waived as TODOs.
func MissingP0CodingAgentCertifications(contract CodingAgentProviderContract) []CodingAgentCertificationID {
	have := make(map[CodingAgentCertificationID]struct{})
	for _, cert := range CodingAgentProviderCertifications(contract.Provider) {
		have[cert.ID] = struct{}{}
	}
	var missing []CodingAgentCertificationID
	for _, id := range RequiredP0CodingAgentCertificationIDs(contract) {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func MissingCodingAgentCertifications(contract CodingAgentProviderContract) []CodingAgentCertificationID {
	have := make(map[CodingAgentCertificationID]struct{})
	for _, cert := range CodingAgentProviderCertifications(contract.Provider) {
		have[cert.ID] = struct{}{}
	}
	var missing []CodingAgentCertificationID
	for _, id := range RequiredCodingAgentCertificationIDs(contract) {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}
