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
//   - <workingDir>/.codex/config.toml  — [mcp_servers.NAME] tables
//                                        (only when mcpServersJSON is
//                                        provided via WithMCPServers)
//   - <workingDir>/.codex/hooks.json   — PreToolUse deny entry
//                                        pointing at the deny script
//   - <workingDir>/.codex/hooks/deny-builtin.sh — POSIX deny script
//                                        that exits 2 on built-in
//                                        tool invocations
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
func writeCodexProjectArtifacts(workingDir, systemPrompt, mcpServersJSON string, denyBuiltins bool) (func(), error) {
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
		cu, err := writeCodexProjectAgentsFile(workingDir, systemPrompt)
		if err != nil {
			rollback()
			return noop, fmt.Errorf("codex AGENTS.md: %w", err)
		}
		if cu != nil {
			cleanups = append(cleanups, cu)
		}
	}

	if strings.TrimSpace(mcpServersJSON) != "" {
		cu, err := writeCodexProjectMCPConfigTOML(workingDir, mcpServersJSON)
		if err != nil {
			rollback()
			return noop, fmt.Errorf("codex .codex/config.toml: %w", err)
		}
		if cu != nil {
			cleanups = append(cleanups, cu)
		}
	}

	if denyBuiltins {
		cu, err := writeCodexProjectDenyBuiltinHooks(workingDir)
		if err != nil {
			rollback()
			return noop, fmt.Errorf("codex deny-builtin hooks: %w", err)
		}
		if cu != nil {
			cleanups = append(cleanups, cu)
		}
	}

	return rollback, nil
}

// writeCodexProjectMCPConfigTOML projects the operator-supplied MCP
// servers JSON into <workingDir>/.codex/config.toml using codex's
// [mcp_servers.NAME] table format. Pre-existing operator content is
// captured and restored on cleanup.
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
func writeCodexProjectMCPConfigTOML(workingDir, mcpServersJSON string) (func(), error) {
	noop := func() {}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" || strings.TrimSpace(mcpServersJSON) == "" {
		return noop, nil
	}

	var servers map[string]codexMCPServerSpec
	if err := json.Unmarshal([]byte(mcpServersJSON), &servers); err != nil {
		return noop, fmt.Errorf("parse MCP servers JSON: %w", err)
	}
	if len(servers) == 0 {
		return noop, nil
	}

	codexDir := filepath.Join(workingDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return noop, fmt.Errorf("create .codex dir: %w", err)
	}
	codexDirCreatedByUs := dirIsEmptyOrJustCreated(codexDir)

	path := filepath.Join(codexDir, "config.toml")
	var priorContent []byte
	priorExisted := false
	if data, err := os.ReadFile(path); err == nil {
		priorContent = data
		priorExisted = true
	} else if !os.IsNotExist(err) {
		return noop, fmt.Errorf("read pre-existing .codex/config.toml: %w", err)
	}

	toml := renderCodexMCPServersTOML(servers)
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

// writeCodexProjectDenyBuiltinHooks installs codex's PreToolUse hook
// configuration that denies built-in tool calls (Bash, apply_patch) and
// forces the model to use MCP servers instead. Two files land:
//
//   - <workingDir>/.codex/hooks.json
//   - <workingDir>/.codex/hooks/deny-builtin.sh
//
// Both are byte-restored on cleanup if they pre-existed; otherwise
// removed. The deny script exits 2 with a stderr reason — codex's
// hook contract treats exit 2 as "System Block", aborting the tool
// call (per developers.openai.com/codex/hooks).
func writeCodexProjectDenyBuiltinHooks(workingDir string) (func(), error) {
	noop := func() {}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return noop, nil
	}

	codexDir := filepath.Join(workingDir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		return noop, fmt.Errorf("create .codex dir: %w", err)
	}
	codexDirCreatedByUs := dirIsEmptyOrJustCreated(codexDir)

	hooksDir := filepath.Join(codexDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return noop, fmt.Errorf("create .codex/hooks dir: %w", err)
	}
	hooksDirCreatedByUs := dirIsEmptyOrJustCreated(hooksDir)

	scriptPath := filepath.Join(hooksDir, "deny-builtin.sh")
	priorScript, scriptExisted, err := readPriorFileForRestore(scriptPath)
	if err != nil {
		return noop, fmt.Errorf("read pre-existing deny-builtin.sh: %w", err)
	}
	scriptBody := "#!/bin/sh\n# mlp-session: deny built-in tool calls; force MCP server usage.\n# Auto-removed at session cleanup.\necho \"Built-in tools disabled by orchestrator policy; use MCP servers instead.\" >&2\nexit 2\n"
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o700); err != nil {
		return noop, fmt.Errorf("write deny-builtin.sh: %w", err)
	}

	hooksPath := filepath.Join(codexDir, "hooks.json")
	priorHooks, hooksExisted, err := readPriorFileForRestore(hooksPath)
	if err != nil {
		// Roll back the script write before bubbling up.
		if scriptExisted {
			_ = os.WriteFile(scriptPath, priorScript, 0o700)
		} else {
			_ = os.Remove(scriptPath)
		}
		return noop, fmt.Errorf("read pre-existing hooks.json: %w", err)
	}
	hooksConfig := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "^(Bash|apply_patch)$",
					"hooks": []map[string]any{
						{
							"type":          "command",
							"command":       scriptPath,
							"statusMessage": "Enforcing MCP-only tool policy (built-ins disabled by orchestrator)",
						},
					},
				},
			},
		},
	}
	hooksJSON, _ := json.MarshalIndent(hooksConfig, "", "  ")
	if err := os.WriteFile(hooksPath, hooksJSON, 0o600); err != nil {
		// Roll back the script write before bubbling up.
		if scriptExisted {
			_ = os.WriteFile(scriptPath, priorScript, 0o700)
		} else {
			_ = os.Remove(scriptPath)
		}
		return noop, fmt.Errorf("write hooks.json: %w", err)
	}

	return func() {
		if hooksExisted {
			_ = os.WriteFile(hooksPath, priorHooks, 0o600)
		} else {
			_ = os.Remove(hooksPath)
		}
		if scriptExisted {
			_ = os.WriteFile(scriptPath, priorScript, 0o700)
		} else {
			_ = os.Remove(scriptPath)
		}
		if hooksDirCreatedByUs {
			_ = os.Remove(hooksDir)
		}
		if codexDirCreatedByUs {
			_ = os.Remove(codexDir)
		}
	}, nil
}

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

// renderCodexMCPServersTOML emits codex's expected [mcp_servers.NAME]
// TOML block format from the parsed server map. Names are sorted to
// keep output deterministic so byte-compare tests are stable.
func renderCodexMCPServersTOML(servers map[string]codexMCPServerSpec) string {
	var b strings.Builder
	b.WriteString("# mlp-session: orchestrator-generated codex MCP server config.\n")
	b.WriteString("# Auto-removed at session cleanup. Pre-existing content is byte-restored.\n\n")

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

// readPriorFileForRestore captures any pre-existing file at path so a
// later cleanup can byte-restore it. Returns (nil, false, nil) when the
// file did not exist; (content, true, nil) when it did; non-nil error
// only on genuine I/O failures (not IsNotExist).
func readPriorFileForRestore(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
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
