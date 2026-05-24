package geminicli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeGeminiProjectArtifacts is the unified opt-in writer the adapter
// invokes when MetadataKeyWriteProjectInstructionFile is set. It
// projects up to three artifacts into <workingDir> with byte-restore
// on session teardown:
//
//   - <workingDir>/GEMINI.md                        — per-session system prompt
//   - <workingDir>/.gemini/settings.json            — merged mcpServers
//                                                     (from operator-supplied
//                                                     projectSettingsJSON, if
//                                                     any) + hooks.BeforeTool
//                                                     deny entry
//   - <workingDir>/.gemini/hooks/deny-builtin.sh    — POSIX deny script that
//                                                     exits 2 (Gemini's
//                                                     "System Block")
//
// Per geminicli.com/docs/hooks, gemini-cli loads hook configuration
// from .gemini/settings.json under the "hooks" key (BeforeTool event)
// and treats exit code 2 from a hook command as a System Block —
// aborting the tool call. So the deny script + hook entry is the
// strong lever for forcing MCP-only tool routing.
//
// The MCP server list is left as-is from the operator's
// MetadataKeyProjectSettings JSON (which already speaks gemini's
// settings.json schema); we only ADD the hooks key, we never strip
// or rewrite mcpServers.
//
// Best-effort by design: a write failure for any single artifact does
// not block the session because the GEMINI_SYSTEM_MD env injection and
// the temp-dir .gemini/settings.json (loaded via --include-directories)
// already carry the configuration. The workspace projection is
// additive belt-and-suspenders.
func writeGeminiProjectArtifacts(workingDir, systemPrompt, projectSettingsJSON string, denyBuiltins bool) (func(), error) {
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
		cu, err := writeGeminiProjectInstructionFile(workingDir, systemPrompt)
		if err != nil {
			rollback()
			return noop, fmt.Errorf("gemini GEMINI.md: %w", err)
		}
		if cu != nil {
			cleanups = append(cleanups, cu)
		}
	}

	// settings.json + hooks/deny-builtin.sh are co-managed: the script
	// path is referenced from settings.json, so we write the script
	// first (so the path exists) and roll back together if either step
	// fails.
	if denyBuiltins || strings.TrimSpace(projectSettingsJSON) != "" {
		cu, err := writeGeminiProjectSettingsAndHooks(workingDir, projectSettingsJSON, denyBuiltins)
		if err != nil {
			rollback()
			return noop, fmt.Errorf("gemini .gemini/settings.json + hooks: %w", err)
		}
		if cu != nil {
			cleanups = append(cleanups, cu)
		}
	}

	return rollback, nil
}

// writeGeminiProjectSettingsAndHooks merges the operator-supplied
// projectSettingsJSON (already in gemini's settings.json schema) with a
// hooks.BeforeTool deny entry pointing at a freshly-installed deny
// script. Both files are byte-restored on cleanup if they pre-existed.
//
// The deny script matches gemini's built-in tool names (read_file,
// write_file, shell, edit) and exits 2 — gemini treats exit 2 as
// System Block, aborting the tool call. MCP server tools are NOT in
// the matcher, so they execute normally.
func writeGeminiProjectSettingsAndHooks(workingDir, projectSettingsJSON string, denyBuiltins bool) (func(), error) {
	noop := func() {}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return noop, nil
	}

	geminiDir := filepath.Join(workingDir, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		return noop, fmt.Errorf("create .gemini dir: %w", err)
	}
	geminiDirCreatedByUs := geminiDirIsEmptyOrJustCreated(geminiDir)

	hooksDir := filepath.Join(geminiDir, "hooks")
	hooksDirCreatedByUs := false
	scriptPath := filepath.Join(hooksDir, "deny-builtin.sh")
	var priorScript []byte
	scriptExisted := false

	if denyBuiltins {
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			return noop, fmt.Errorf("create .gemini/hooks dir: %w", err)
		}
		hooksDirCreatedByUs = geminiDirIsEmptyOrJustCreated(hooksDir)

		var err error
		priorScript, scriptExisted, err = geminiReadPriorFileForRestore(scriptPath)
		if err != nil {
			return noop, fmt.Errorf("read pre-existing deny-builtin.sh: %w", err)
		}
		scriptBody := "#!/bin/sh\n# mlp-session: deny built-in tool calls; force MCP server usage.\n# Auto-removed at session cleanup.\necho \"Built-in tools disabled by orchestrator policy; use MCP servers instead.\" >&2\nexit 2\n"
		if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o700); err != nil {
			return noop, fmt.Errorf("write deny-builtin.sh: %w", err)
		}
	}

	settingsPath := filepath.Join(geminiDir, "settings.json")
	priorSettings, settingsExisted, err := geminiReadPriorFileForRestore(settingsPath)
	if err != nil {
		// Roll back the script write before bubbling up.
		if denyBuiltins {
			if scriptExisted {
				_ = os.WriteFile(scriptPath, priorScript, 0o700)
			} else {
				_ = os.Remove(scriptPath)
			}
		}
		return noop, fmt.Errorf("read pre-existing settings.json: %w", err)
	}

	settings := map[string]any{}
	if strings.TrimSpace(projectSettingsJSON) != "" {
		if err := json.Unmarshal([]byte(projectSettingsJSON), &settings); err != nil {
			// Don't roll back script — settings.json never got written,
			// but we want to keep the deny script for the session's
			// hook lookup path. We just won't carry the operator's
			// invalid JSON forward.
			return noop, fmt.Errorf("parse projectSettingsJSON: %w", err)
		}
	}

	if denyBuiltins {
		// Merge with any operator-supplied hooks block. If they
		// already declared a BeforeTool entry, ours is appended so
		// both fire — gemini executes hooks in declared order.
		hooks, _ := settings["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		existingBefore, _ := hooks["BeforeTool"].([]any)
		denyEntry := map[string]any{
			"matcher": "^(read_file|write_file|shell|edit|grep|search_file_content|web_fetch)$",
			"hooks": []map[string]any{
				{
					"name":        "mlp-session-deny-builtin",
					"type":        "command",
					"command":     scriptPath,
					"timeout":     5000,
					"description": "Enforcing MCP-only tool policy (built-ins disabled by orchestrator)",
				},
			},
		}
		hooks["BeforeTool"] = append(existingBefore, denyEntry)
		settings["hooks"] = hooks
	}

	settingsJSON, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, settingsJSON, 0o600); err != nil {
		// Roll back the script write.
		if denyBuiltins {
			if scriptExisted {
				_ = os.WriteFile(scriptPath, priorScript, 0o700)
			} else {
				_ = os.Remove(scriptPath)
			}
		}
		return noop, fmt.Errorf("write settings.json: %w", err)
	}

	return func() {
		if settingsExisted {
			_ = os.WriteFile(settingsPath, priorSettings, 0o600)
		} else {
			_ = os.Remove(settingsPath)
		}
		if denyBuiltins {
			if scriptExisted {
				_ = os.WriteFile(scriptPath, priorScript, 0o700)
			} else {
				_ = os.Remove(scriptPath)
			}
		}
		if hooksDirCreatedByUs {
			_ = os.Remove(hooksDir)
		}
		if geminiDirCreatedByUs {
			_ = os.Remove(geminiDir)
		}
	}, nil
}

func geminiReadPriorFileForRestore(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func geminiDirIsEmptyOrJustCreated(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
