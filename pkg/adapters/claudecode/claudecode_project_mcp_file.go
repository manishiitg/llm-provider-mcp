package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// claudeProjectFileRestores is a process-wide registry of byte-restore
// payloads keyed by absolute file path. When writeClaudeCodeProjectMCPFile
// (and any future opt-in project-artifact writer) installs content into a
// file the operator may have already owned, it stores the prior bytes
// here. removeFiles consults this map first: if a path is registered, it
// restores the prior bytes (and deletes the entry) instead of removing
// the file. This lets us re-use the existing tempFiles []string slice
// for both pure-temp paths and byte-restore paths without changing
// buildClaudeArgs's signature or the dozen-ish cleanup callsites.
//
// We use sync.Map because session teardown can run from a goroutine that
// races acquireClaudePersistentInteractiveSession on a different owner.
var claudeProjectFileRestores sync.Map // map[string][]byte

// writeClaudeCodeProjectMCPFile is the OFF-by-default counterpart to
// writeClaudeCodeProjectRuleFile: when MetadataKeyWriteProjectInstructionFile
// is set AND MetadataKeyMCPConfig carries a JSON document, also drop the
// MCP server list at <workingDir>/.mcp.json (Claude Code's project-scoped
// MCP convention). On cleanup, byte-restore any pre-existing operator
// .mcp.json so user-owned configuration is preserved across runs.
//
// Returns the absolute path (so the caller can append it to tempFiles
// for the existing removeFiles cleanup flow) plus a nil error. An empty
// workingDir is treated as a no-op (returns ""). Errors are reserved for
// the actual write — empty inputs short-circuit.
//
// Risk caveat: .mcp.json is a single-file convention; if the
// orchestrator process crashes between write and cleanup, the
// operator's pre-existing .mcp.json is destroyed. The OFF-by-default
// flag keeps the blast radius bounded to callers that explicitly accept
// this trade-off.
func writeClaudeCodeProjectMCPFile(workingDir, mcpJSON string) (string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return "", nil
	}
	if strings.TrimSpace(mcpJSON) == "" {
		return "", nil
	}

	path := filepath.Join(workingDir, ".mcp.json")

	if prior, err := os.ReadFile(path); err == nil {
		claudeProjectFileRestores.Store(path, prior)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read pre-existing .mcp.json: %w", err)
	}

	if err := os.WriteFile(path, []byte(mcpJSON), 0o600); err != nil {
		// If we registered a prior payload above but the write failed,
		// drop the registry entry — there's nothing to clean up later.
		claudeProjectFileRestores.Delete(path)
		return "", fmt.Errorf("write .mcp.json: %w", err)
	}
	return path, nil
}
