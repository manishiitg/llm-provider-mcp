package llmproviders

import (
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	claudecodeadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	codexcli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	cursorcli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	picli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/picli"
)

func WithOpenRouterUsage() CallOption {
	return func(opts *CallOptions) {
		// Set the usage parameter in the request metadata (not CallOptions metadata)
		// This will be passed to the actual HTTP request body
		if opts.Metadata == nil {
			opts.Metadata = &llmtypes.Metadata{
				Usage: &llmtypes.UsageMetadata{Include: true},
			}
		} else {
			if opts.Metadata.Usage == nil {
				opts.Metadata.Usage = &llmtypes.UsageMetadata{Include: true}
			} else {
				opts.Metadata.Usage.Include = true
			}
		}
	}
}

// WithMCPConfig sets the MCP configuration JSON string for the Claude Code adapter session.
func WithMCPConfig(config string) llmtypes.CallOption {
	return claudecodeadapter.WithMCPConfig(config)
}

// WithMCPReadyFile sets the path the session's MCP server (bridge) creates once
// it answers the CLI's tools/list handshake. On a freshly created interactive
// session the adapter holds the first prompt until this file appears, closing
// the cold-turn race where the model runs its opening turn before its MCP tools
// have connected. Bounded and best-effort (proceeds on timeout).
func WithMCPReadyFile(path string) llmtypes.CallOption {
	return claudecodeadapter.WithMCPReadyFile(path)
}

// WithDangerouslySkipPermissions enables the --dangerously-skip-permissions flag for the Claude Code CLI.
// CAUTION: This allows the agent to execute any tool without user confirmation.
func WithDangerouslySkipPermissions() llmtypes.CallOption {
	return claudecodeadapter.WithDangerouslySkipPermissions()
}

// WithClaudeCodeSettings sets the --settings flag for the Claude Code CLI.
// It accepts either a JSON string or a file path.
func WithClaudeCodeSettings(settings string) llmtypes.CallOption {
	return claudecodeadapter.WithClaudeCodeSettings(settings)
}

// WithClaudeCodeTools sets the --tools flag for the Claude Code CLI.
// Use "" to disable all built-in tools.
func WithClaudeCodeTools(tools string) llmtypes.CallOption {
	return claudecodeadapter.WithClaudeCodeTools(tools)
}

// WithAllowedTools sets the --allowed-tools flag for the Claude Code CLI.
// Example: "mcp__api-bridge__*" to allow all tools from the bridge.
func WithAllowedTools(tools string) llmtypes.CallOption {
	return claudecodeadapter.WithAllowedTools(tools)
}

// WithMaxTurns sets the --max-turns flag for the Claude Code CLI.
// Limits the number of agentic turns. Claude Code exits with an error when the limit is reached.
func WithMaxTurns(maxTurns int) llmtypes.CallOption {
	return claudecodeadapter.WithMaxTurns(maxTurns)
}

// WithResumeSessionID sets the --resume flag so the Claude Code CLI resumes
// an existing session instead of starting a new one.
func WithResumeSessionID(id string) llmtypes.CallOption {
	return claudecodeadapter.WithResumeSessionID(id)
}

// WithClaudeCodeInteractiveSessionID links a Claude Code tmux run to
// the owning application session so live follow-up input can be sent to it.
func WithClaudeCodeInteractiveSessionID(id string) llmtypes.CallOption {
	return claudecodeadapter.WithInteractiveSessionID(id)
}

// WithClaudeCodePersistentInteractiveSession keeps an interactive Claude Code
// tmux session alive across completed turns for normal chat. Workflow runs
// should keep the default per-turn lifecycle.
func WithClaudeCodePersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return claudecodeadapter.WithPersistentInteractiveSession(enabled)
}

// WithClaudeCodeWorkingDir sets the process working directory for Claude Code.
func WithClaudeCodeWorkingDir(dir string) llmtypes.CallOption {
	return claudecodeadapter.WithWorkingDir(dir)
}

// WithClaudeCodeWriteProjectInstructionFile controls whether the adapter
// ALSO projects the per-session system prompt into <workingDir>/CLAUDE.md
// (Claude Code's project-instructions convention), in addition to the
// --system-prompt-file injection. ON by default; pass false to opt out
// for repos where you want to preserve an operator-authored CLAUDE.md
// even across crash windows. Any pre-existing CLAUDE.md is byte-restored
// on session cleanup; a process crash between write and cleanup
// destroys the operator's prior content.
func WithClaudeCodeWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return claudecodeadapter.WithWriteProjectInstructionFile(enabled)
}

// WithClaudeCodeProjectInstructionOnly makes the adapter inject the
// per-session system prompt SOLELY via <workingDir>/CLAUDE.md and skip the
// --system-prompt-file flag. OFF by default. Claude Code auto-loads CLAUDE.md
// as project instructions, so the prompt is still applied — but only once,
// avoiding the doubled system prompt that otherwise results from passing the
// same bytes through both --system-prompt-file and CLAUDE.md. Requires the
// CLAUDE.md projection (on by default) and a working dir; if the projection is
// disabled or its write fails, the adapter falls back to --system-prompt-file.
func WithClaudeCodeProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return claudecodeadapter.WithProjectInstructionOnly(enabled)
}

// WithClaudeCodeEffort sets the --effort flag for the Claude Code CLI.
// Values: "low", "medium", "high", "max"
func WithClaudeCodeEffort(level string) llmtypes.CallOption {
	return claudecodeadapter.WithEffort(level)
}

// --- Codex CLI Wrapper Functions ---

// WithCodexResumeSessionID sets the session ID to resume via `codex exec resume`.
func WithCodexResumeSessionID(id string) llmtypes.CallOption {
	return codexcli.WithResumeSessionID(id)
}

// WithCursorResumeSessionID sets the --resume flag so cursor-agent resumes
// the chat by session id (the value cursor emits in its stream-json init
// event, also the directory name under ~/.cursor/chats/<md5(cwd)>/<id>).
// Mirrors the claude-code / gemini / codex equivalents.
func WithCursorResumeSessionID(id string) llmtypes.CallOption {
	return cursorcli.WithResumeSessionID(id)
}

// WithCodexInteractiveSessionID links a Codex CLI interactive run to the
// owning application session so live follow-up input can be sent to it.
func WithCodexInteractiveSessionID(id string) llmtypes.CallOption {
	return codexcli.WithInteractiveSessionID(id)
}

// WithCodexPersistentInteractiveSession keeps a Codex CLI tmux TUI alive across
// completed interactive chat turns.
func WithCodexPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return codexcli.WithPersistentInteractiveSession(enabled)
}

// WithCodexWriteProjectInstructionFile controls whether the codex
// adapter ALSO projects the per-session system prompt into
// <workingDir>/AGENTS.md (codex's project-instructions convention), in
// addition to the -c model_instructions_file injection. ON by default;
// pass false to opt out. Cleanup byte-restores any pre-existing
// operator AGENTS.md; a process crash between write and cleanup
// destroys the operator's prior content.
func WithCodexWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return codexcli.WithWriteProjectInstructionFile(enabled)
}

// WithCodexProjectInstructionOnly carries the per-session system prompt solely
// via the projected AGENTS.md and skips the codex developer_instructions /
// model_instructions_file CLI override, so the prompt is applied once instead
// of doubled. OFF by default. Falls back to the CLI override if the projection
// is disabled or its write fails.
func WithCodexProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return codexcli.WithProjectInstructionOnly(enabled)
}

// WithCodexApprovalPolicy sets the approval_policy config override for the Codex CLI.
// Values: "never" (auto-approve all), "on-request" (model decides), "untrusted" (most restrictive)
func WithCodexApprovalPolicy(policy string) llmtypes.CallOption {
	return codexcli.WithApprovalPolicy(policy)
}

// WithCodexStructuredTransport selects `codex exec --json` (per-turn, one-shot,
// no tmux dependency) instead of the tmux interactive transport. OFF by
// default — see docs/coding_sdk_tmux_contract.md: tmux is the normal product
// path; structured is for callers with no live-steering/terminal-view need
// (e.g. unattended workflow steps).
func WithCodexStructuredTransport(enabled bool) llmtypes.CallOption {
	return codexcli.WithCodexStructuredTransport(enabled)
}

// WithCursorStructuredTransport selects `cursor-agent --print --output-format
// stream-json` instead of the tmux interactive transport. OFF by default —
// see WithCodexStructuredTransport doc comment for the rationale.
func WithCursorStructuredTransport(enabled bool) llmtypes.CallOption {
	return cursorcli.WithCursorStructuredTransport(enabled)
}

// WithPiStructuredTransport selects `pi --print --mode json` instead of the
// tmux interactive transport. OFF by default — see
// WithCodexStructuredTransport doc comment for the rationale.
func WithPiStructuredTransport(enabled bool) llmtypes.CallOption {
	return picli.WithPiStructuredTransport(enabled)
}

// WithCodexReasoningEffort sets the model_reasoning_effort for the Codex CLI.
// Values: "none", "minimal", "low", "medium", "high", "xhigh"
func WithCodexReasoningEffort(effort string) llmtypes.CallOption {
	return codexcli.WithReasoningEffort(effort)
}

// WithCodexDisableShellTool disables the built-in shell tool in Codex CLI.
func WithCodexDisableShellTool() llmtypes.CallOption {
	return codexcli.WithDisableShellTool()
}

// WithCodexFullAuto enables --full-auto mode for the Codex CLI.
func WithCodexFullAuto() llmtypes.CallOption {
	return codexcli.WithFullAuto()
}

// WithCodexSandbox sets the --sandbox flag for the Codex CLI.
// Values: "read-only", "workspace-write", "danger-full-access"
func WithCodexSandbox(sandbox string) llmtypes.CallOption {
	return codexcli.WithSandbox(sandbox)
}

// WithCodexConfigOverrides passes arbitrary -c key=value overrides to the Codex CLI.
func WithCodexConfigOverrides(overrides []string) llmtypes.CallOption {
	return codexcli.WithConfigOverrides(overrides)
}

// WithCodexMCPServers materializes per-session MCP server configuration in a
// temporary Codex profile TOML instead of passing it through -c arguments.
func WithCodexMCPServers(mcpJSON string) llmtypes.CallOption {
	return codexcli.WithMCPServers(mcpJSON)
}

// WithCodexProjectDirID sets the --cd flag for the Codex CLI working directory.
func WithCodexProjectDirID(dir string) llmtypes.CallOption {
	return codexcli.WithProjectDirID(dir)
}

// WithCodexEnableFeatures enables one or more Codex CLI features (comma-separated).
func WithCodexEnableFeatures(features string) llmtypes.CallOption {
	return codexcli.WithEnableFeatures(features)
}

// WithCursorWorkingDir sets the Cursor Agent CLI workspace/cwd for tmux launch.
func WithCursorWorkingDir(dir string) llmtypes.CallOption {
	return cursorcli.WithWorkingDir(dir)
}

// WithCursorInteractiveSessionID links a Cursor Agent CLI tmux run to the
// owning application session for live follow-up input.
func WithCursorInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return cursorcli.WithInteractiveSessionID(sessionID)
}

// WithCursorPersistentInteractiveSession keeps the Cursor Agent CLI tmux
// session alive across turns.
func WithCursorPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return cursorcli.WithPersistentInteractiveSession(enabled)
}

// WithCursorMCPConfig writes a temporary/restored .cursor/mcp.json before
// launching Cursor Agent CLI.
func WithCursorMCPConfig(config string) llmtypes.CallOption {
	return cursorcli.WithMCPConfig(config)
}

// WithCursorProjectConfig writes a temporary/restored .cursor/cli.json before
// launching Cursor Agent CLI.
func WithCursorProjectConfig(config string) llmtypes.CallOption {
	return cursorcli.WithProjectConfig(config)
}

// WithCursorForce enables Cursor Agent CLI's --force flag.
func WithCursorForce() llmtypes.CallOption {
	return cursorcli.WithForce()
}

// WithCursorApproveMCPs enables Cursor Agent CLI's --approve-mcps flag, which
// auto-accepts the "approve this MCP server?" TUI consent dialog so bridge
// tool calls do not stall waiting for a human operator. Only useful when an
// MCP config is also provided (see WithCursorMCPConfig).
func WithCursorApproveMCPs() llmtypes.CallOption {
	return cursorcli.WithApproveMCPs()
}

// WithCursorAutoApproveWebSearch allows Cursor Agent CLI's TUI approval
// prompts for web search and opening URLs in an already-user-initiated agent
// turn. It does not enable --force.
func WithCursorAutoApproveWebSearch() llmtypes.CallOption {
	return cursorcli.WithAutoApproveWebSearch()
}

// WithCursorDenyBuiltinTools installs a per-session .cursor/hooks.json
// that denies cursor's built-in Shell and Read tools via the
// beforeShellExecution + beforeReadFile hook events. The agent must then
// route those actions through the MCP bridge instead — pair with
// WithCursorMCPConfig so api-bridge.execute_shell_command and
// api-bridge.read_file are available. Cleanup at session teardown
// restores any pre-existing hooks.json the operator had in their
// workspace. This is the "hard lever" for bridge-only tool usage that
// the soft system-prompt coaching can't enforce reliably.
func WithCursorDenyBuiltinTools(enabled bool) llmtypes.CallOption {
	return cursorcli.WithDenyBuiltinTools(enabled)
}

// WithCursorMode sets Cursor Agent CLI's --mode flag. "ask" and "plan" are
// both read-only at the CLI level. Leave empty for normal agent mode.
//
// DEPRECATED FOR "ask" — prefer WithCursorDenyBuiltinTools(true) instead.
// Ask mode is a conversational stance that hard-refuses natural-language
// write requests with "Switch to Agent mode and ask…"; the orchestrator
// no longer uses it. To force the agent through the MCP bridge instead
// of cursor's built-in Read/Shell, install cursor hooks via
// WithCursorDenyBuiltinTools(true). "plan" mode remains a valid use of
// this option for read-only planning sessions.
func WithCursorMode(mode string) llmtypes.CallOption {
	return cursorcli.WithMode(mode)
}

// WithCursorSandbox sets Cursor Agent CLI's --sandbox flag. Supported values
// are "enabled" and "disabled".
func WithCursorSandbox(mode string) llmtypes.CallOption {
	return cursorcli.WithSandbox(mode)
}

// WithPiWorkingDir sets the Pi CLI workspace/cwd for tmux launch.
func WithPiWorkingDir(dir string) llmtypes.CallOption {
	return picli.WithWorkingDir(dir)
}

// WithPiInteractiveSessionID links a Pi CLI tmux run to the owning application
// session for live follow-up input.
func WithPiInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return picli.WithInteractiveSessionID(sessionID)
}

// WithPiPersistentInteractiveSession keeps the Pi CLI tmux session alive
// across turns.
func WithPiPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return picli.WithPersistentInteractiveSession(enabled)
}

// WithPiResumeSessionID resumes a Pi native session created with --session-id.
func WithPiResumeSessionID(sessionID string) llmtypes.CallOption {
	return picli.WithResumeSessionID(sessionID)
}

// WithPiProvider overrides Pi's provider routing while keeping model selection
// separate. Model IDs can also be provider-qualified, e.g.
// google/gemini-3.5-flash.
func WithPiProvider(provider string) llmtypes.CallOption {
	return picli.WithProvider(provider)
}

// WithPiMCPConfig writes a Pi project MCP config override into .pi/mcp.json
// for the adapter-owned working directory.
func WithPiMCPConfig(config string) llmtypes.CallOption {
	return picli.WithMCPConfig(config)
}

// WithPiBridgeOnlyTools disables Pi's built-in tools while leaving explicit
// extension/custom tools, including the MCP adapter, enabled.
func WithPiBridgeOnlyTools(enabled bool) llmtypes.CallOption {
	return picli.WithBridgeOnlyTools(enabled)
}

// WithPiMCPExtension overrides the Pi extension source used for MCP support.
// The default is npm:pi-mcp-adapter.
func WithPiMCPExtension(source string) llmtypes.CallOption {
	return picli.WithMCPExtension(source)
}

// WithPiStatuslineExtension overrides the Pi statusline extension source.
// The default is npm:@narumitw/pi-statusline@0.8.0. Pass "off", "false",
// "0", or "none" to disable the adapter-managed statusline extension.
func WithPiStatuslineExtension(source string) llmtypes.CallOption {
	return picli.WithStatuslineExtension(source)
}

// LLM Configuration Management Functions

// LLMDefaultsResponse represents the response structure for LLM defaults
