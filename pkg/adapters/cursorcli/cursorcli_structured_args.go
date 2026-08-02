package cursorcli

// buildCursorStructuredArgs constructs the argv for a `cursor-agent --print`
// structured (stream-json) turn. Extracted from the adapter so the containment
// (--mode) and resume flag SHAPE can be regression-tested without launching the
// CLI (see TestBuildCursorStructuredArgs). MCP-config file writing and skill
// projection are disk side-effects done by the caller — they touch .cursor/,
// not argv — so this function is pure.
//
// Resume is the invariant this pins: a resume turn adds --resume <priorID>
// (id already trimmed by the caller).
//
// hooksInstalled reports that the caller wrote .cursor/hooks.json for this turn.
// --force is withheld when it has, because --force bypasses cursor hooks — with
// both set the denylist would be silently inert. Structured mode used to lack
// hooks entirely and contained the agent with "--mode ask" instead, which is
// read-only and cost every step its ability to write or drive a browser.
func buildCursorStructuredArgs(workingDir, modelToUse, mode, sandbox string, approveMCPs, hooksInstalled bool, resumeID, prompt string) []string {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--trust",
	}
	if !hooksInstalled {
		args = append(args, "--force")
	}
	if workingDir != "" {
		args = append(args, "--workspace", workingDir)
	}
	if modelToUse != "" {
		args = append(args, "--model", modelToUse)
	}
	if mode != "" {
		args = append(args, "--mode", mode)
	}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	if approveMCPs {
		args = append(args, "--approve-mcps")
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	args = append(args, prompt)
	return args
}
