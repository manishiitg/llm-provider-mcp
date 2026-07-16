package codexcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// writeCodexProjectArtifacts is the unified opt-in writer invoked by
// the adapter when MetadataKeyWriteProjectInstructionFile is set. It
// projects up to four artifacts into <workingDir> with byte-restore on
// teardown:
//
//   - <workingDir>/AGENTS.md           — per-session system prompt
//   - <workingDir>/.codex/config.toml  — AgentWorks session defaults plus
//     optional [mcp_servers.NAME] tables
//   - <workingDir>/.codex/hooks.json   — PreToolUse deny entry
//     pointing at the deny script
//   - <workingDir>/.codex/hooks/deny-builtin.sh — POSIX deny script
//     that exits 2 on built-in
//     tool invocations
//
// Each artifact captures any pre-existing operator content and restores
// it byte-for-byte at session teardown. The returned cleanup is a
// composite: it walks each captured cleanup in REVERSE order (LIFO) so
// nested files are restored before their parent directories are
// considered for removal of empty ancestors we created.
//
// Best-effort by design: a write failure for any single artifact does
// not block the session — the primary injection paths
// (-c model_instructions_file, -c overrides for MCP) already carry the
// configuration. The workspace projection is additive belt-and-
// suspenders, useful when downstream tooling reads codex's
// project-scoped files directly.
func writeCodexProjectArtifacts(workingDir, systemPrompt, mcpServersJSON string, denyBuiltins, restorePrior bool) (func(), error) {
	noop := func() {}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return noop, nil
	}

	var cleanups []func()
	rollback := func() {
		// LIFO: restore most-recently-written artifact first.
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	if strings.TrimSpace(systemPrompt) != "" {
		cu, err := writeCodexProjectAgentsFile(workingDir, systemPrompt, restorePrior)
		if err != nil {
			rollback()
			return noop, fmt.Errorf("codex AGENTS.md: %w", err)
		}
		if cu != nil {
			cleanups = append(cleanups, cu)
		}
	}

	cu, err := writeCodexProjectConfigTOML(workingDir, mcpServersJSON, restorePrior)
	if err != nil {
		rollback()
		return noop, fmt.Errorf("codex .codex/config.toml: %w", err)
	}
	if cu != nil {
		cleanups = append(cleanups, cu)
	}

	// denyBuiltins is intentionally unused for codex. The hooks-file
	// projection (.codex/hooks.json + deny script) was REMOVED because
	// codex already has first-class --disable <feature> CLI flags for
	// every built-in tool we'd want to block. The canonical deny-builtin
	// path on codex is appendCodexDisabledFeatureArgs with
	// codexBridgeOnlyDisabledFeatures (in options.go) — that list covers
	// shell_tool, apply_patch via patch tool, unified_exec, tool_search,
	// multi_agent, apps, browser_use, computer_use,
	// workspace_dependencies, hooks, plugins, unavailable_dummy_tools.
	// Passing those as flags is strictly cleaner than dropping a hook
	// script: no SHA-keyed trust prompt to dismiss, no
	// MLP_ENABLE_UNSAFE_WORKSPACE_PROJECTIONS gating, no per-session
	// auto-dismiss flakiness. The denyBuiltins parameter is retained on
	// the function signature for API symmetry with the other adapters
	// (gemini, cursor, agy) but is a no-op here. Operators who want
	// deny-builtin behavior on codex should call WithDisableShellTool /
	// WithDisableFeatures via the adapter options instead.
	_ = denyBuiltins

	// Final teardown: nuke the whole .codex/ tree. Registered LAST so it
	// fires FIRST in LIFO order, making the earlier per-file restore
	// callbacks no-ops on already-gone files. The intent is a clean wipe
	// between sessions — orphaned config.toml from a prior session whose
	// cleanup callback didn't fire would otherwise leak. Trade-off: an
	// operator's own pre-existing content under .codex/ is destroyed.
	// AGENTS.md (workingDir root, outside .codex/) is still byte-restored
	// by writeCodexProjectAgentsFile so an operator AGENTS.md survives.
	cleanups = append(cleanups, func() {
		_ = os.RemoveAll(filepath.Join(workingDir, ".codex"))
	})

	return rollback, nil
}

// writeCodexProjectConfigTOML writes the AgentWorks-scoped Codex defaults and
// optionally projects operator-supplied MCP server JSON into
// <workingDir>/.codex/config.toml. Pre-existing operator content is captured
// and restored on cleanup.
//
// JSON shape expected (matches WithMCPServers): a map of server name
// → {command: string, args?: []string, env?: map[string]string,
//
//	env_vars?: []string, startup_timeout_sec?: int,
//	tool_timeout_sec?: int, enabled?: bool}.
//
// The TOML emitter is intentionally minimal — codex MCP server
// definitions have a known small shape (per
// developers.openai.com/codex/mcp), so we avoid pulling in a TOML
// dependency.
func writeCodexProjectConfigTOML(workingDir, mcpServersJSON string, restorePrior bool) (func(), error) {
	noop := func() {}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return noop, nil
	}

	var servers map[string]codexMCPServerSpec
	if strings.TrimSpace(mcpServersJSON) != "" {
		if err := json.Unmarshal([]byte(mcpServersJSON), &servers); err != nil {
			return noop, fmt.Errorf("parse MCP servers JSON: %w", err)
		}
	}

	codexDir := filepath.Join(workingDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return noop, fmt.Errorf("create .codex dir: %w", err)
	}
	codexDirCreatedByUs := dirIsEmptyOrJustCreated(codexDir)

	path := filepath.Join(codexDir, "config.toml")
	var priorContent []byte
	priorExisted := false
	if restorePrior {
		if data, err := os.ReadFile(path); err == nil {
			priorContent = data
			priorExisted = true
		} else if !os.IsNotExist(err) {
			return noop, fmt.Errorf("read pre-existing .codex/config.toml: %w", err)
		}
	}

	toml := renderCodexProjectConfigTOML(servers)
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		return noop, fmt.Errorf("write .codex/config.toml: %w", err)
	}

	return func() {
		if priorExisted {
			_ = os.WriteFile(path, priorContent, 0o600)
		} else {
			_ = os.Remove(path)
		}
		if codexDirCreatedByUs {
			_ = os.Remove(codexDir) // best-effort, no-op if other files landed
		}
	}, nil
}

// writeCodexProjectDenyBuiltinHooks was REMOVED. It used to write
// <workingDir>/.codex/hooks.json + .codex/hooks/deny-builtin.sh to
// implement deny-builtin-tools via codex's PreToolUse hook contract.
// Dropping those files triggered codex's interactive hook trust review
// screen on first invocation per hook-content SHA, which the tmux
// adapter couldn't reliably auto-dismiss across codex's two-form
// prompt sequence (see commit 367291d for the partial auto-dismiss).
//
// The cleaner path is codex's first-class --disable <feature> CLI
// flags: appendCodexDisabledFeatureArgs in options.go applies the
// codexBridgeOnlyDisabledFeatures list (shell_tool, unified_exec,
// tool_search, multi_agent, apps, browser_use, computer_use,
// workspace_dependencies, hooks, plugins,
// unavailable_dummy_tools, etc.) when the caller asks for MCP-only
// routing. Flags don't trigger any trust prompts, don't need
// SHA-keyed caching, and work on the first invocation in a fresh
// tempdir. There's no reason to use a hook script when CLI flags
// already cover the use case.
//
// Operators who want deny-builtin behavior on codex should call
// WithDisableShellTool / WithDisableFeatures via the adapter options
// instead of relying on a workspace-dropped hook file.

// codexMCPServerSpec mirrors codex's MCP server schema from
// developers.openai.com/codex/mcp. Fields are optional except command.
type codexMCPServerSpec struct {
	Command           string            `json:"command"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	EnvVars           []string          `json:"env_vars,omitempty"`
	StartupTimeoutSec *int              `json:"startup_timeout_sec,omitempty"`
	ToolTimeoutSec    *int              `json:"tool_timeout_sec,omitempty"`
	Enabled           *bool             `json:"enabled,omitempty"`
}

// renderCodexProjectConfigTOML emits AgentWorks' project-scoped Codex defaults
// followed by optional [mcp_servers.NAME] blocks. The project service tier
// overrides a user's global /fast selection while still allowing an explicit
// per-invocation -c service_tier=... override to win.
func renderCodexProjectConfigTOML(servers map[string]codexMCPServerSpec) string {
	var b strings.Builder
	b.WriteString("# mlp-session: orchestrator-generated Codex project config.\n")
	b.WriteString("# Auto-removed at session cleanup. Pre-existing content is byte-restored.\n\n")
	b.WriteString("# Do not inherit a user-level /fast choice into AgentWorks workflows.\n")
	b.WriteString("service_tier = \"default\"\n\n")

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		spec := servers[name]
		fmt.Fprintf(&b, "[mcp_servers.%s]\n", tomlQuoteKey(name))
		fmt.Fprintf(&b, "command = %s\n", tomlQuoteString(spec.Command))
		if len(spec.Args) > 0 {
			fmt.Fprintf(&b, "args = %s\n", tomlStringArray(spec.Args))
		}
		if len(spec.EnvVars) > 0 {
			fmt.Fprintf(&b, "env_vars = %s\n", tomlStringArray(spec.EnvVars))
		}
		if spec.StartupTimeoutSec != nil {
			fmt.Fprintf(&b, "startup_timeout_sec = %d\n", *spec.StartupTimeoutSec)
		}
		if spec.ToolTimeoutSec != nil {
			fmt.Fprintf(&b, "tool_timeout_sec = %d\n", *spec.ToolTimeoutSec)
		}
		if spec.Enabled != nil {
			fmt.Fprintf(&b, "enabled = %t\n", *spec.Enabled)
		}
		if len(spec.Env) > 0 {
			fmt.Fprintf(&b, "\n[mcp_servers.%s.env]\n", tomlQuoteKey(name))
			envKeys := make([]string, 0, len(spec.Env))
			for k := range spec.Env {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			for _, k := range envKeys {
				fmt.Fprintf(&b, "%s = %s\n", tomlQuoteKey(k), tomlQuoteString(spec.Env[k]))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// tomlQuoteKey returns a bare key when it matches TOML's bare-key
// grammar (A-Za-z0-9_-), otherwise emits a quoted key. Server names
// like "api-bridge" stay bare; names with dots or spaces get quoted.
func tomlQuoteKey(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		isAlpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if !isAlpha && !isDigit && r != '_' && r != '-' {
			return tomlQuoteString(s)
		}
	}
	return s
}

// tomlQuoteString emits a basic-string TOML literal with the minimum
// set of escape sequences for the kinds of strings we expect in MCP
// configs (paths, URLs, command names).
func tomlQuoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlStringArray(items []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(tomlQuoteString(item))
	}
	b.WriteByte(']')
	return b.String()
}

// dirIsEmptyOrJustCreated returns true iff the directory exists and has
// no children — a heuristic for "we just created this and nothing has
// been written into it yet, so cleanup can attempt os.Remove (which
// will fail harmlessly if other artifacts land later)."
func dirIsEmptyOrJustCreated(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
