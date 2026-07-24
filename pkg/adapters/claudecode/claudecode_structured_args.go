package claudecode

import "strings"

// claudeStructuredDisallowedTools is the hard DENYLIST for structured (print)
// mode. --allowedTools is only a permission whitelist, and
// --dangerously-skip-permissions overrides it (found live: claude fell back to
// its native Bash when the bridge tool kept failing), so claude's built-in
// shell/file tools are denied outright — the print-mode analogue of the tmux
// deny hooks — forcing every tool call through the MCP bridge.
const claudeStructuredDisallowedTools = "Bash,Read,Edit,Write,MultiEdit,NotebookEdit,Glob,Grep,WebFetch,Task"

// buildClaudeStructuredArgs constructs the argv for a `claude -p` structured
// (stream-json) turn and returns the session id the turn runs under. Extracted
// from the adapter so the containment + resume flag SHAPE can be
// regression-tested without launching the CLI (see
// TestBuildClaudeStructuredArgs). The prompt is delivered via stdin (not argv),
// and MCP-config file writing + skill projection are disk side-effects done by
// the caller (which passes the resolved mcpConfigPath / freshSessionID in).
//
// Resume is the invariant this pins: a resume turn uses --resume <priorID> and
// runs under that id; a fresh turn mints an id and uses --session-id <id> so it
// can be surfaced and resumed next turn. That capture-and-resume symmetry is
// what carries context across structured turns.
func buildClaudeStructuredArgs(modelID, systemPrompt, allowedTools, mcpConfigPath, resumeSessionID, freshSessionID, workingDir string) (args []string, sessionID string) {
	// --output-format stream-json REQUIRES --verbose on current claude builds.
	args = []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
	if strings.TrimSpace(modelID) != "" {
		args = append(args, "--model", modelID)
	}
	if strings.TrimSpace(systemPrompt) != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	if allowedTools != "" {
		args = append(args, "--allowedTools", allowedTools)
		args = append(args, "--disallowedTools", claudeStructuredDisallowedTools)
	}
	if mcpConfigPath != "" {
		args = append(args, "--mcp-config", mcpConfigPath, "--strict-mcp-config")
	}
	if resumeSessionID != "" {
		sessionID = resumeSessionID
		args = append(args, "--resume", resumeSessionID)
	} else {
		sessionID = freshSessionID
		args = append(args, "--session-id", freshSessionID)
	}
	if workingDir != "" {
		args = append(args, "--add-dir", workingDir)
	}
	return args, sessionID
}
