package llmproviders

import (
	"sort"
	"strings"
)

// CodingAgentCertificationID is an executable proof point for one part of the
// coding-agent contract. Capability flags in CodingAgentProviderContract should
// map to these IDs instead of being treated as comments.
type CodingAgentCertificationID string

const (
	CertFreshLaunch               CodingAgentCertificationID = "fresh_launch"
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
	CertMultiTurn                 CodingAgentCertificationID = "multi_turn"
	CertStaleDraftCleanup         CodingAgentCertificationID = "stale_draft_cleanup"
	CertLiveInput                 CodingAgentCertificationID = "live_input"
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
	// CertCtrlCStatePreserved proves that sending Ctrl+C (the 0x03 keystroke
	// in tmux mode, SIGINT for structured mode) interrupts the current turn
	// WITHOUT corrupting the CLI's persisted chat state. The next launch
	// with --resume <id> must still see the prior conversation intact.
	// This is distinct from CertCancellation, which only proves the
	// interrupt is RECEIVED — not that state survives it.
	CertCtrlCStatePreserved CodingAgentCertificationID = "ctrl_c_state_preserved"
)

var requiredTmuxCertificationIDs = []CodingAgentCertificationID{
	CertFreshLaunch,
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

// CodingAgentCertification records the real or deterministic test that proves a
// certification ID. TestFile is repository-relative so normal unit tests can
// verify that the referenced proof still exists.
type CodingAgentCertification struct {
	ID          CodingAgentCertificationID
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
	{"final extraction", func(c CodingAgentProviderContract) bool { return c.SupportsFinalExtraction }, []CodingAgentCertificationID{CertFinalExtraction}},
	{"persistent session", func(c CodingAgentProviderContract) bool { return c.UsesPersistentSession }, []CodingAgentCertificationID{CertMultiTurn, CertStaleDraftCleanup}},
	{"live input", func(c CodingAgentProviderContract) bool { return c.SupportsLiveInput }, []CodingAgentCertificationID{CertLiveInput}},
	{"interrupt", func(c CodingAgentProviderContract) bool { return c.SupportsInterrupt }, []CodingAgentCertificationID{CertCancellation}},
	{"ctrl-c state preserved", func(c CodingAgentProviderContract) bool { return c.HandlesCtrlCCleanExit }, []CodingAgentCertificationID{CertCtrlCStatePreserved}},
	{"process cleanup", func(c CodingAgentProviderContract) bool { return c.ProcessScopedCleanup }, []CodingAgentCertificationID{CertCleanup}},
	{"session loss", func(c CodingAgentProviderContract) bool { return c.HandlesTmuxSessionLoss }, []CodingAgentCertificationID{CertSessionLoss, CertSessionLossRecovery}},
}

var codingAgentProviderCertifications = map[Provider][]CodingAgentCertification{
	ProviderClaudeCode: {
		{
			ID:          CertFreshLaunch,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationFreshPromptCarriesUserText",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "starts Claude Code tmux transport and carries the first user prompt",
			RealE2E:     true,
		},
		{
			ID:          CertResumeCompactionStartup,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_adapter_test.go",
			TestName:    "TestClaudeResumeSummaryMenuSubmitsDefaultChoice",
			Description: "resume/compaction startup prompts are detected and submitted through the global prompt-wait path",
		},
		{
			ID:          CertStartupTerminalVisibility,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_adapter_test.go",
			TestName:    "TestClaudeTerminalStreamCapturesRawScreenRows",
			Description: "Claude startup/working panes emit raw terminal rows before final completion",
		},
		{
			ID:          CertWorkingDirectory,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuWorkingDirectory",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "proves Claude Code and its MCP bridge run in the requested cwd",
			RealE2E:     true,
		},
		{
			ID:          CertTrustAuthPrompts,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuWorkingDirectory",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "fresh Claude workspace trust setup is handled before launching in a requested cwd",
			RealE2E:     true,
		},
		{
			ID:          CertNativeSystemPrompt,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationNativeSystemPrompt",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "system prompt reaches Claude through native system-prompt transport",
			RealE2E:     true,
		},
		{
			ID:          CertPromptPaste,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationLargePastedPromptSubmits",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "large pasted prompt submits correctly through tmux",
			RealE2E:     true,
		},
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuMCPBridgeContract",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "Claude Code calls a real MCP bridge tool",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationNoInternalTools",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_INTEGRATION=1"},
			Description: "Claude Code bridge-only mode does not expose internal shell/file tools",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolLiveInput,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "live validation input during a slow MCP tool does not complete the foreground turn",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolFalseIdle,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "slow MCP activity keeps Claude classified active even if prompt-looking UI appears",
			RealE2E:     true,
		},
		{
			ID:          CertDoneDetection,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "slow MCP plus live input does not falsely complete while active",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxRealFinalExtractionFromTmuxVertexJudgeE2E",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1", "GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "real Claude Code tmux turn is captured and Vertex judges final extraction quality, formatting, and TUI noise removal",
			RealE2E:     true,
		},
		{
			ID:          CertMultiTurn,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuPersistentInteractiveMultiTurn",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "persistent Claude Code tmux session keeps native multi-turn memory",
			RealE2E:     true,
		},
		{
			ID:          CertStaleDraftCleanup,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationPersistentClearsStaleDraftBeforeNextTurn",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "stale prompt drafts are cleared before the next backend turn",
			RealE2E:     true,
		},
		{
			ID:          CertLiveInput,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationHaikuLiveInputAndEscape",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_LIVE_E2E=1"},
			Description: "live input routes into the active Claude Code tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertCancellation,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationPersistentCancelDoesNotLeaveBusySessionReusable",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "canceled Claude Code persistent sessions are not reused while busy",
			RealE2E:     true,
		},
		{
			ID:          CertPersistentCancelReuse,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
			TestName:    "TestClaudeCodeTmuxIntegrationPersistentCancelDoesNotLeaveBusySessionReusable",
			Env:         []string{"RUN_CLAUDE_CODE_TMUX_PERSISTENT_E2E=1"},
			Description: "a canceled persistent Claude pane is not reused unless it is prompt-ready",
			RealE2E:     true,
		},
		{
			ID:          CertLifecyclePolicy,
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
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
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
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
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_integration_test.go",
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
			TestFile:    "pkg/adapters/claudecode/claudecode_experimental_adapter_test.go",
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
			ID:          CertFreshLaunch,
			TestFile:    "pkg/adapters/codexcli/codexcli_real_contract_test.go",
			TestName:    "TestCodexCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1"},
			Description: "starts Codex CLI tmux transport, reaches ready, and streams terminal-only chunks",
			RealE2E:     true,
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
			TestName:    "TestCodexCLIRealFinalExtractionFromTmuxVertexJudgeE2E",
			Env:         []string{"RUN_CODEX_CLI_REAL_E2E=1", "RUN_CODEX_CLI_INTERACTIVE_E2E=1", "GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "real Codex CLI tmux turn is captured and Vertex judges final extraction quality, formatting, and TUI noise removal",
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
		// CertMCPBridge — first cursor certification on file. Proves the
		// adapter actually wires cursor's structured (--print) path through
		// an MCP bridge subprocess and surfaces the tool's output. Until
		// this was registered, cursor's UsesMCPBridge:true claim was
		// unverified, and a real workflow run (HDFC read-credentials,
		// 2026-05-24) silently produced 0 tokens / 0 tool calls because
		// nobody noticed the bridge path wasn't being exercised.
		//
		// More cursor certifications (tmux suite, working dir, trust,
		// resume, native system prompt, etc.) are tracked as
		// knownCertificationGaps in coding_agent_contract_test.go and will
		// be added as their e2e tests are written.
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_structured_integration_test.go",
			TestName:    "TestCursorCLIStructuredMCPBridge",
			Env:         []string{"RUN_CURSOR_CLI_STREAM_JSON_E2E=1"},
			Description: "Cursor CLI calls a real MCP bridge tool through its structured (--print) path",
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
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/cursorcli/cursorcli_real_contract_test.go",
			TestName:    "TestCursorCLIRealFinalExtractionFromTmuxVertexJudgeE2E",
			Env:         []string{"RUN_CURSOR_CLI_REAL_E2E=1", "RUN_CURSOR_CLI_INTERACTIVE_E2E=1", "GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "real Cursor CLI tmux turn is captured and Vertex judges final extraction quality, formatting, and shell/TUI transcript removal",
			RealE2E:     true,
		},
	},
	ProviderAgyCLI: {
		{
			ID:          CertFreshLaunch,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "starts Antigravity CLI tmux transport, reaches ready, and streams terminal-only chunks",
			RealE2E:     true,
		},
		{
			ID:          CertStartupTerminalVisibility,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "Antigravity startup/working panes emit raw terminal rows before final completion",
			RealE2E:     true,
		},
		{
			ID:          CertResumeCompactionStartup,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealNativeResumeAfterTmuxLossContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "Antigravity --conversation relaunch starts cleanly and accepts the next prompt without a blocking resume menu",
			RealE2E:     true,
		},
		{
			ID:          CertTrustAuthPrompts,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealAuthPromptSurfacedBeforePromptContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "Antigravity startup detects a real auth/login prompt before submitting user input and surfaces an actionable error",
			RealE2E:     true,
		},
		{
			ID:          CertNativeSystemPrompt,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealSystemPromptRulesContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "system instructions are loaded from workspace-scoped Antigravity rules, not pasted as user text",
			RealE2E:     true,
		},
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealMCPBridgeContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "Antigravity CLI loads workspace-scoped .agents/mcp_config.json and calls a real MCP bridge tool",
			RealE2E:     true,
		},
		{
			ID:          CertPromptPaste,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealLargePastedPromptSubmits",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "large multiline user prompt submits correctly through Antigravity tmux paste",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealBridgeOnlyToolsContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "workspace hooks deny Antigravity built-in read/list/search/command tools while an MCP bridge write remains available",
			RealE2E:     true,
		},
		{
			ID:          CertWorkingDirectory,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealWorkingDirectoryMCPContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "Antigravity MCP bridge tools run from the adapter-supplied working directory",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolFalseIdle,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealSlowToolFalseIdleContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "Antigravity tmux completion waits for a slow MCP tool result instead of treating quiet output as done",
			RealE2E:     true,
		},
		{
			ID:          CertSlowToolLiveInput,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealSlowToolLiveInputDoesNotCompleteContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "live validation input during an Antigravity slow MCP tool does not complete the foreground turn",
			RealE2E:     true,
		},
		{
			ID:          CertDoneDetection,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealSlowToolLiveInputDoesNotCompleteContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "Antigravity slow MCP plus queued live input is not parsed as a completed final response",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealFinalExtractionFromTmuxVertexJudgeE2E",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1", "GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "real Antigravity CLI tmux turn is captured and Vertex judges final extraction quality, formatting, and MCP/thought/TUI noise removal",
			RealE2E:     true,
		},
		{
			ID:          CertMultiTurn,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "persistent Antigravity CLI tmux session is reused across completed turns",
			RealE2E:     true,
		},
		{
			ID:          CertStaleDraftCleanup,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealPersistentClearsStaleDraftBeforeNextTurn",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "stale Antigravity prompt drafts are cleared before the next backend-controlled turn is pasted",
			RealE2E:     true,
		},
		{
			ID:          CertLifecyclePolicy,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealInteractiveTmuxFullContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "persistent Antigravity chat sessions remain registered after a completed turn",
			RealE2E:     true,
		},
		{
			ID:          CertLiveInput,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealInteractiveLiveInputAndEscapeContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "live input routes into the active Antigravity CLI tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertCancellation,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealCancellationClosesSessionContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "context cancellation interrupts an active Antigravity slow-tool turn and returns an error",
			RealE2E:     true,
		},
		{
			ID:          CertPersistentCancelReuse,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealCancellationClosesSessionContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "canceled Antigravity persistent sessions are closed and retries start a fresh usable tmux session",
			RealE2E:     true,
		},
		{
			ID:          CertBoundedRetention,
			TestFile:    "pkg/adapters/agycli/agycli_cleanup_test.go",
			TestName:    "TestCleanupAgyCLIInteractiveSessionsDoesNotBlockOnBusySession",
			Description: "bounded Antigravity cleanup/retention path can drain without blocking active sessions",
		},
		{
			ID:          CertParallelIsolation,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealInteractiveParallelIsolation",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "parallel Antigravity tmux sessions keep distinct session state and tmux sessions",
			RealE2E:     true,
		},
		{
			ID:          CertCleanup,
			TestFile:    "pkg/adapters/agycli/agycli_cleanup_test.go",
			TestName:    "TestCleanupAgyCLIInteractiveSessionsDoesNotBlockOnBusySession",
			Description: "cleanup does not deadlock on busy persistent Antigravity CLI sessions",
		},
		{
			ID:          CertSessionLoss,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealNativeResumeAfterTmuxLossContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "simulates Antigravity tmux loss after capturing a provider-native conversation id",
			RealE2E:     true,
		},
		{
			ID:          CertSessionLossRecovery,
			TestFile:    "pkg/adapters/agycli/agycli_real_contract_test.go",
			TestName:    "TestAgyCLIRealNativeResumeAfterTmuxLossContract",
			Env:         []string{"RUN_AGY_CLI_REAL_E2E=1", "RUN_AGY_CLI_INTERACTIVE_E2E=1"},
			Description: "relaunches Antigravity with --conversation and recalls a canary without app history replay",
			RealE2E:     true,
		},
		{
			ID:          CertParallelStartupQueue,
			TestFile:    "pkg/adapters/internal/tmuxlaunch/tmuxlaunch_test.go",
			TestName:    "TestAcquireQueuesConcurrentStarts",
			Description: "tmux startup acquisition serializes concurrent provider launches when configured",
		},
	},
	ProviderGeminiCLI: {
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/geminicli/geminicli_stream_integration_test.go",
			TestName:    "TestGeminiCLIRealStreamJSONMCPBridgeContract",
			Env:         []string{"RUN_GEMINI_CLI_STREAM_JSON_E2E=1"},
			Description: "Gemini CLI calls a real MCP bridge tool through its stream-json path",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/geminicli/geminicli_deny_behavioral_e2e_test.go",
			TestName:    "TestGeminiCLIRealDenyBuiltinHookActuallyFires",
			Env:         []string{"RUN_GEMINI_CLI_REAL_E2E=1", "GEMINI_API_KEY"},
			Description: "Gemini CLI live tmux run logs a built-in read_file hook denial and prevents sentinel leakage",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/geminicli/geminicli_adapter_test.go",
			TestName:    "TestGeminiFinalExtractionVertexJudgeE2E",
			Env:         []string{"GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "Vertex judge validates Gemini final extraction quality, formatting, and MCP/tool panel noise removal",
			RealE2E:     true,
		},
	},
	ProviderOpenCodeCLI: {
		{
			ID:          CertMCPBridge,
			TestFile:    "pkg/adapters/opencodecli/opencodecli_structured_integration_test.go",
			TestName:    "TestOpenCodeCLIStructuredMCPBridge",
			Env:         []string{"RUN_OPENCODE_CLI_REAL_E2E=1"},
			Description: "OpenCode CLI calls a real MCP bridge tool through its structured run path",
			RealE2E:     true,
		},
		{
			ID:          CertBridgeOnlyTools,
			TestFile:    "pkg/adapters/opencodecli/opencodecli_deny_behavioral_e2e_test.go",
			TestName:    "TestOpenCodeCLIRealDenyBuiltinHookActuallyFires",
			Env:         []string{"RUN_OPENCODE_CLI_REAL_E2E=1"},
			Description: "OpenCode CLI real run surfaces tools-deny evidence and prevents built-in read sentinel leakage",
			RealE2E:     true,
		},
		{
			ID:          CertFinalExtraction,
			TestFile:    "pkg/adapters/opencodecli/opencodecli_adapter_test.go",
			TestName:    "TestOpenCodeFinalExtractionVertexJudgeE2E",
			Env:         []string{"GEMINI_API_KEY or VERTEX_API_KEY or GOOGLE_API_KEY"},
			Description: "Vertex judge validates OpenCode structured final extraction quality and tool-event noise removal",
			RealE2E:     true,
		},
	},
}

// RequiredCodingAgentCertificationIDs returns the proof IDs implied by the
// provider's claimed capabilities.
func RequiredCodingAgentCertificationIDs(contract CodingAgentProviderContract) []CodingAgentCertificationID {
	seen := make(map[CodingAgentCertificationID]struct{}, len(requiredTmuxCertificationIDs))
	if contract.Transport == CodingAgentTransportTmux {
		for _, id := range requiredTmuxCertificationIDs {
			seen[id] = struct{}{}
		}
	} else {
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
	sort.Slice(certs, func(i, j int) bool {
		if certs[i].ID == certs[j].ID {
			return certs[i].TestName < certs[j].TestName
		}
		return certs[i].ID < certs[j].ID
	})
	return certs
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
