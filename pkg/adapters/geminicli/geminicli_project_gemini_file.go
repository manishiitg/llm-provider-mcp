package geminicli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// writeGeminiProjectInstructionFile is the OFF-by-default companion to
// the GEMINI_SYSTEM_MD injection: when MetadataKeyWriteProjectInstructionFile
// is set, the adapter ALSO writes the per-session system prompt to
// <workingDir>/GEMINI.md (gemini-cli's project-context convention) so
// downstream tooling or operators auditing the workspace can see the
// prompt. The returned cleanup restores any pre-existing GEMINI.md
// byte-for-byte (or deletes the file we created) so operator-owned
// content survives successful runs.
//
// Empty workingDir is treated as a no-op: returns a non-nil cleanup so
// callers don't have to nil-check, and never touches the orchestrator's
// own cwd.
//
// Risk caveat: GEMINI.md is a single-file convention. If the
// orchestrator process crashes between write and cleanup, the
// operator's pre-existing GEMINI.md is destroyed. Off-by-default keeps
// the blast radius bounded.
func writeGeminiProjectInstructionFile(workingDir, systemPrompt string, restorePrior bool) (func(), error) {
	noop := func() {}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return noop, nil
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return noop, nil
	}

	path := filepath.Join(workingDir, "GEMINI.md")

	var priorContent []byte
	priorExisted := false
	if restorePrior {
		if data, err := os.ReadFile(path); err == nil {
			priorContent = data
			priorExisted = true
		} else if !os.IsNotExist(err) {
			return noop, fmt.Errorf("read pre-existing GEMINI.md: %w", err)
		}
	}

	body := "<!-- mlp-session-instructions: orchestrator-generated per-session system prompt. Auto-removed at session cleanup. -->\n\n" + systemPrompt
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return noop, fmt.Errorf("write GEMINI.md: %w", err)
	}

	cleanup := func() {
		if priorExisted {
			_ = os.WriteFile(path, priorContent, 0o600)
			return
		}
		_ = os.Remove(path)
	}
	return cleanup, nil
}

// geminiWriteProjectInstructionFromOptions reads the project-artifact
// projection flag from call options. Now defaults to TRUE: when the
// metadata key is unset, we project GEMINI.md + .gemini/settings.json
// + .gemini/hooks/deny-builtin.sh into the workflow's working
// directory, matching the behavior of the other tmux-backed CLIs
// (agy/cursor/claude-code) which always project their rule files.
//
// The earlier OFF-by-default rationale (operator might own GEMINI.md
// at the workflow root) does not apply to workflow folders under
// workspace-docs/Workflow/ — those are orchestrator-managed, not
// user-edited; there is no pre-existing operator content to destroy.
// Callers can still pass WithGeminiWriteProjectInstructionFile(false)
// explicitly to opt out for non-workflow workingDirs.
func geminiWriteProjectInstructionFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return true
	}
	v, ok := opts.Metadata.Custom[MetadataKeyWriteProjectInstructionFile]
	if !ok {
		return true
	}
	enabled, _ := v.(bool)
	return enabled
}

// geminiRestoreProjectFilesFromOptions reads the OFF-by-default feature
// flag for preserving operator-owned project artifacts (GEMINI.md,
// .gemini/settings.json, deny script) across a session. Returns false when
// the key is unset: the default writes fresh and deletes on cleanup, never
// restoring pre-existing content. Callers opt into the legacy byte-restore
// behavior with WithRestoreProjectFiles(true).
func geminiRestoreProjectFilesFromOptions(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return false
	}
	enabled, _ := opts.Metadata.Custom[MetadataKeyRestoreProjectFiles].(bool)
	return enabled
}
