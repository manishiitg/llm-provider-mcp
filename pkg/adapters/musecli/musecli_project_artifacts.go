package musecli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Project-instruction projection for muse, mirroring codex's
// writeCodexProjectAgentsFile: the per-session system prompt is written to
// <workingDir>/AGENTS.md, which muse auto-loads as project instructions in
// a trusted workspace. Muse has no --system-prompt flag, so in
// project-instruction-only mode this file is the SOLE carrier — the adapter
// skips typing the preamble inline and the turn stays small.
//
// Byte-restore semantics match codex: with restorePrior the cleanup puts an
// operator's pre-existing AGENTS.md back byte-for-byte, otherwise the
// projected file is deleted. Either way a failed write returns an error so
// the caller can fall back to inline typing and the prompt is never lost.
func writeMuseProjectAgentsFile(workingDir, systemPrompt string, restorePrior bool) (func(), error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure muse working dir: %w", err)
	}
	path := filepath.Join(workingDir, "AGENTS.md")
	var previous []byte
	existed := false
	if restorePrior {
		data, readErr := os.ReadFile(path)
		if readErr == nil {
			previous, existed = data, true
		} else if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("read existing AGENTS.md: %w", readErr)
		}
	}
	body := "<!-- mlp-session-instructions: orchestrator-generated per-session system prompt. Restored on cleanup. -->\n\n" + systemPrompt
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return nil, fmt.Errorf("write AGENTS.md: %w", err)
	}
	return func() {
		if existed {
			_ = os.WriteFile(path, previous, 0o600)
		} else {
			_ = os.Remove(path)
		}
	}, nil
}

// museInlinePrompt is the legacy concatenation: system texts joined ahead
// of the human turn. Used by the exec lane and as the file-only fallback
// when AGENTS.md projection is disabled or fails.
func museInlinePrompt(system []string, human string) string {
	if len(system) > 0 {
		return strings.Join(system, "\n\n") + "\n\n" + human
	}
	return human
}

// projectMuseAgentsForTurn projects the system prompt for bounded
// (non-pooled) turns, which boot and tear down one TUI per turn. Returns
// the teardown restore and whether the file carries this turn; on any
// failure both are nil/false and the caller types inline instead.
func projectMuseAgentsForTurn(workdir string, system []string, wantAgents, restorePrior bool) (func(), bool) {
	if !wantAgents || len(system) == 0 {
		return nil, false
	}
	restore, err := writeMuseProjectAgentsFile(workdir, strings.Join(system, "\n\n"), restorePrior)
	if err != nil {
		return nil, false
	}
	return restore, true
}
