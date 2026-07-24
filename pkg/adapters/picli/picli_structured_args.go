package picli

// buildPiStructuredArgs constructs the argv for a `pi --print --mode json`
// structured turn. Extracted from the adapter so the session-continuity and
// containment flag SHAPE can be regression-tested without launching the CLI
// (see TestBuildPiStructuredArgs). The prompt is delivered via stdin (not
// argv), and skill projection is a disk side-effect done by the caller, which
// passes the resolved skillDir in here.
//
// Session continuity is the invariant this pins: pi persists a session under
// --session-id ("creating it if missing"), so BOTH a fresh turn (minted id)
// and a resume turn (prior id) pass --session-id — that symmetry is what makes
// turn 2 recall turn 1 instead of starting blank. bridgeOnly maps to
// --no-builtin-tools (disables pi's native bash/edit/write while leaving
// extensions enabled); it additionally needs an explicit `-e <mcp-extension>`
// because --no-builtin-tools also suppresses default MCP extension
// auto-discovery (an undocumented interaction, verified live against the real
// CLI). --approve marks a dynamic temp workspace trusted so project-local .pi
// resources (including the just-written .pi/mcp.json) are not silently ignored.
func buildPiStructuredArgs(sessionID string, bridgeOnly, mcpConfigSet bool, mcpExtension string, hasWorkingDir bool, skillDir string) []string {
	args := []string{"--print", "--mode", "json"}
	args = append(args, "--session-id", sessionID)
	if bridgeOnly {
		args = append(args, "--no-builtin-tools")
		if mcpConfigSet {
			args = append(args, "-e", mcpExtension)
		}
	}
	if hasWorkingDir {
		args = append(args, "--approve")
	}
	if skillDir != "" {
		args = append(args, "--skill", skillDir)
	}
	return args
}
