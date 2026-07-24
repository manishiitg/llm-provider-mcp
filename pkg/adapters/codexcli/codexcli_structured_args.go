package codexcli

import (
	"fmt"
	"strings"
)

// buildCodexStructuredArgs constructs the argv for a `codex exec` structured
// (json) turn. It is the single source of truth for the resume-vs-fresh
// argument SHAPE, extracted from the adapter so the ordering can be
// regression-tested without launching the CLI (see
// TestBuildCodexStructuredArgs). Disk side-effects (session-profile file,
// skill projection) and cwd (cmd.Dir) stay with the caller — this function is
// pure.
//
// The load-bearing invariant this pins: on a RESUME turn, `codex exec resume`
// does NOT accept --profile / --sandbox / -C, so the MCP profile must be
// supplied via the GLOBAL --profile flag placed BEFORE the "exec" subcommand,
// the sandbox via a global `-c sandbox_mode=...` override, and cwd via cmd.Dir.
// On a FRESH turn the same three concerns are expressed as subcommand-level
// flags placed AFTER "exec". Getting that ordering wrong silently breaks resume
// (the flags are rejected or ignored), which is exactly the regression this
// builder + its test exist to catch.
func buildCodexStructuredArgs(resumeSessionID, sessionProfile, sandboxMode, workingDir, modelToUse string, configOverrides []string, prompt string) []string {
	var args []string
	if resumeSessionID != "" {
		if sessionProfile != "" {
			args = append(args, "--profile", sessionProfile) // GLOBAL: layers $CODEX_HOME/<name>.config.toml
		}
		args = append(args, "-c", fmt.Sprintf("sandbox_mode=%q", sandboxMode)) // GLOBAL: resume has no --sandbox
		args = append(args, "exec", "resume", resumeSessionID, "--json", "--skip-git-repo-check")
		if modelToUse != "" && modelToUse != "codex-cli" {
			args = append(args, "--model", modelToUse)
		}
	} else {
		args = append(args, "exec", "--json", "--skip-git-repo-check")
		if workingDir != "" {
			args = append(args, "-C", workingDir)
		}
		if modelToUse != "" && modelToUse != "codex-cli" {
			args = append(args, "--model", modelToUse)
		}
		args = append(args, "--sandbox", sandboxMode)
		if sessionProfile != "" {
			args = append(args, "--profile", sessionProfile)
		}
	}
	for _, override := range configOverrides {
		if strings.TrimSpace(override) != "" {
			args = append(args, "-c", override)
		}
	}
	args = append(args, prompt)
	return args
}
