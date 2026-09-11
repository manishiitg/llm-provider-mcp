package musecli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Muse has no --mcp-config CLI flag (verified against `muse exec --help`):
// MCP servers come from the user-level settings file
// $XDG_CONFIG_HOME/muse/settings.json, {"schema_version": 1,
// "mcpServers": {"<name>": {"url": "<streamable-http>"}}}. There is no
// workspace-level equivalent, so the exec lane merges the caller's bridge
// servers into that file for the duration of one run and restores the
// previous content afterwards (merge, don't clobber).

// museSettingsPath resolves the user-level muse settings file the way the
// CLI does: $XDG_CONFIG_HOME wins when set, otherwise ~/.config (NOT
// os.UserConfigDir — on darwin that is ~/Library/Application Support, which
// the CLI does not read; proven live 2026-09-10 when a merge written there
// never reached the session). Tests redirect via XDG.
func museSettingsPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "muse", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for muse settings: %w", err)
	}
	return filepath.Join(home, ".config", "muse", "settings.json"), nil
}

// museApplyMCPConfig merges the "mcpServers" entries of configJSON (if any)
// into the user-level muse settings.json and forces tui.voice_enabled to
// false, returning a restore function that puts the previous settings back
// byte-exact. Voice input has no CLI flag or launch argument (verified
// against `muse --help`/`muse exec --help`) -- settings.json's
// "tui":{"voice_enabled":...} is the only control, so every launch through
// this integration overlays it off regardless of whether an MCP config is
// also mounted: AgentWorks turns are driven by prompt injection, and a
// stray Alt+V (or the CLI's own voice-input hint rendering) has no
// legitimate role in an automated session. Anything already present under a
// colliding server name, or any other existing "tui" field, is preserved
// across the run and reinstated by restore. All file errors abort the
// launch: a half-mounted overlay is worse than no run.
func museApplyMCPConfig(configJSON string) (func(), error) {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	configJSON = strings.TrimSpace(configJSON)
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &doc); err != nil {
			return nil, fmt.Errorf("muse MCP config is not valid JSON: %w", err)
		}
	}
	path, err := museSettingsPath()
	if err != nil {
		return nil, err
	}
	previous := map[string]json.RawMessage{}
	var previousRaw []byte
	var settings map[string]json.RawMessage
	if raw, readErr := os.ReadFile(path); readErr == nil {
		previousRaw = append([]byte(nil), raw...)
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, fmt.Errorf("existing muse settings.json is not a JSON object: %w", err)
		}
		if settings == nil {
			settings = map[string]json.RawMessage{}
		}
		if rawServers, ok := settings["mcpServers"]; ok {
			if err := json.Unmarshal(rawServers, &previous); err != nil {
				return nil, fmt.Errorf("existing muse settings.json mcpServers is not an object: %w", err)
			}
		}
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read muse settings.json: %w", readErr)
	} else {
		settings = map[string]json.RawMessage{}
	}
	merged := make(map[string]json.RawMessage, len(previous)+len(doc.MCPServers))
	for name, entry := range previous {
		merged[name] = entry
	}
	for name, entry := range doc.MCPServers {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("muse MCP config has an empty server name")
		}
		merged[name] = entry
	}
	if len(merged) > 0 {
		mergedRaw, err := json.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("marshal merged muse mcpServers: %w", err)
		}
		settings["mcpServers"] = mergedRaw
	}
	if _, ok := settings["schema_version"]; !ok {
		settings["schema_version"] = json.RawMessage("1")
	}
	tui := map[string]json.RawMessage{}
	if rawTUI, ok := settings["tui"]; ok {
		if err := json.Unmarshal(rawTUI, &tui); err != nil {
			return nil, fmt.Errorf("existing muse settings.json tui is not an object: %w", err)
		}
	}
	tui["voice_enabled"] = json.RawMessage("false")
	tuiRaw, err := json.Marshal(tui)
	if err != nil {
		return nil, fmt.Errorf("marshal muse settings.json tui: %w", err)
	}
	settings["tui"] = tuiRaw
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal muse settings.json: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create muse config dir: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write muse settings.json: %w", err)
	}
	// Restore is byte-exact: the pre-run snapshot goes back verbatim (or the
	// file is removed when we created it), so key order, formatting, and
	// trailing bytes survive the round trip. Deliberately unconditional — a
	// leaked mount impersonates user intent to every later CLI run, which is
	// worse than clobbering a concurrent external edit in this window.
	return func() {
		if previousRaw == nil {
			_ = os.Remove(path)
			return
		}
		_ = os.WriteFile(path, previousRaw, 0o600)
	}, nil
}
