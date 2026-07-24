package cursorcli

// buildCursorStructuredArgs constructs the argv for a `cursor-agent --print`
// structured (stream-json) turn. Extracted from the adapter so the containment
// (--mode) and resume flag SHAPE can be regression-tested without launching the
// CLI (see TestBuildCursorStructuredArgs). MCP-config file writing and skill
// projection are disk side-effects done by the caller — they touch .cursor/,
// not argv — so this function is pure.
//
// Resume is the invariant this pins: a resume turn adds --resume <priorID>
// (id already trimmed by the caller). The mode value carries structured mode's
// bridge-only containment: with no explicit --mode, a deny-builtins request
// resolves to "ask" (cursor refuses natural-language writes rather than
// executing them) since a one-shot print launch has no hook mechanism to
// install the tmux-style .cursor/hooks.json denylist.
func buildCursorStructuredArgs(workingDir, modelToUse, mode, sandbox string, approveMCPs bool, resumeID, prompt string) []string {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--trust",
		"--force",
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
