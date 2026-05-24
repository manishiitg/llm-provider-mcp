package claudecode

import (
	"encoding/json"
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

	// Pre-approve the MCP server names in ~/.claude.json projects entry
	// so Claude Code's "New MCP server found in .mcp.json — approve?"
	// startup prompt does not appear. Without this pre-approval, the
	// tmux adapter cannot dismiss the prompt and the session times out.
	// Errors are silently ignored — the worst case is the operator
	// gets prompted and the session times out, which is no worse than
	// not doing this at all.
	preApproveClaudeMCPServersForWorkingDir(workingDir, mcpJSON)

	return path, nil
}

// preApproveClaudeMCPServersForWorkingDir parses the operator-supplied
// MCP JSON to extract server names, then ensures each is listed in
// ~/.claude.json's projects.<workingDir>.enabledMcpjsonServers array.
// This is the same pre-approval mechanism Claude Code uses internally
// when the operator clicks "Use this MCP server" — we just bypass the
// interactive prompt by recording the consent up front.
//
// The function follows the same path-resolution conventions as
// preTrustClaudeWorkingDir (records under raw AND symlink-resolved
// paths so macOS /var → /private/var aliases match).
func preApproveClaudeMCPServersForWorkingDir(workingDir, mcpJSON string) {
	serverNames := extractClaudeMCPServerNames(mcpJSON)
	if len(serverNames) == 0 {
		return
	}
	// Parse the raw mcpServers map so we can ALSO install it under
	// projects.<dir>.mcpServers (which Claude Code uses to decide
	// what's "new" vs "known"). Failure to parse is non-fatal: we
	// still write enabledMcpjsonServers, which is what we depend on
	// most.
	var docForServers struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	_ = json.Unmarshal([]byte(mcpJSON), &docForServers)
	parsedMCPServers := map[string]any{}
	for name, raw := range docForServers.MCPServers {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			parsedMCPServers[name] = v
		}
	}

	paths := []string{workingDir}
	if resolved, err := filepath.EvalSymlinks(workingDir); err == nil {
		seen := false
		for _, p := range paths {
			if p == resolved {
				seen = true
				break
			}
		}
		if !seen {
			paths = append(paths, resolved)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	configPath := filepath.Join(home, ".claude.json")

	preTrustClaudeMu.Lock()
	defer preTrustClaudeMu.Unlock()

	raw, readErr := os.ReadFile(configPath)
	var config map[string]any
	if readErr == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &config)
	}
	if config == nil {
		config = map[string]any{}
	}

	projects, _ := config["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	for _, p := range paths {
		entry, _ := projects[p].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}
		existing, _ := entry["enabledMcpjsonServers"].([]any)
		seen := make(map[string]bool, len(existing))
		for _, v := range existing {
			if s, ok := v.(string); ok {
				seen[s] = true
			}
		}
		merged := make([]any, 0, len(existing)+len(serverNames))
		merged = append(merged, existing...)
		for _, name := range serverNames {
			if !seen[name] {
				merged = append(merged, name)
				seen[name] = true
			}
		}
		entry["enabledMcpjsonServers"] = merged
		// Also populate the per-project mcpServers map with the actual
		// server configs. Claude Code uses the diff between
		// projects.<dir>.mcpServers and the workspace .mcp.json to
		// decide whether the server is "new" and trigger the discovery
		// prompt. By installing the same config under both, the prompt
		// is suppressed because there's no diff.
		entry["mcpServers"] = parsedMCPServers
		projects[p] = entry
	}
	config["projects"] = projects

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(configPath, out, 0o600)
}

// extractClaudeMCPServerNames returns the top-level keys of the
// "mcpServers" object in the supplied JSON, or an empty slice if the
// JSON is malformed or lacks the expected shape. Server names are
// returned in stable iteration order is not guaranteed; callers should
// not depend on ordering.
func extractClaudeMCPServerNames(mcpJSON string) []string {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mcpJSON), &doc); err != nil {
		return nil
	}
	names := make([]string, 0, len(doc.MCPServers))
	for name := range doc.MCPServers {
		names = append(names, name)
	}
	return names
}
